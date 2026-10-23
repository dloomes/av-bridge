package nightly

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Readiness checks — the routine half of Room Readiness, separate from the
// power schedule. Each room with a readiness check (its own, or the
// customer default) gets a readiness run at its run time on its days: the
// run waits while the room's power run is still in progress, then hands
// the routine to the executor, which finishes the run.
//
//   pending  — created at the run time; waiting to start
//   testing  — the executor is running the routine
//   ready / failed — terminal (set by the executor, or here if the
//                    routine can't run at all)

// readinessWaitStepIndex is the step that records a wait for the power
// cycle. Negative so it sorts before the routine's steps (0, 1, ...).
const readinessWaitStepIndex = -1

// readinessRoom is one room's effective readiness check.
type readinessRoom struct {
	customerID, roomID, roomName string
	routineID                    string
	runTime                      time.Duration
	daysOfWeek                   []int
	timezone                     string
	excludedUntil                *time.Time
}

func (s *Scheduler) collectReadinessRooms(ctx context.Context) ([]readinessRoom, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rs.customer_id::text, r.id::text, r.name,
		       COALESCE(rrc.test_routine_id, rs.test_routine_id)::text,
		       COALESCE(rrc.run_time, rs.run_time),
		       COALESCE(rrc.days_of_week, rs.days_of_week),
		       COALESCE(ns.timezone, 'Europe/London'),
		       rnc.excluded_until
		  FROM readiness_schedule rs
		  JOIN rooms r                       ON r.customer_id = rs.customer_id
		  LEFT JOIN room_readiness_config rrc ON rrc.room_id = r.id
		  LEFT JOIN room_nightly_config rnc   ON rnc.room_id = r.id
		  LEFT JOIN nightly_schedule ns       ON ns.customer_id = rs.customer_id
		 WHERE rs.enabled = true
		   AND COALESCE(rrc.test_routine_id, rs.test_routine_id) IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []readinessRoom
	for rows.Next() {
		var (
			rr      readinessRoom
			runTime time.Time
			days    []int32
		)
		if err := rows.Scan(&rr.customerID, &rr.roomID, &rr.roomName, &rr.routineID,
			&runTime, &days, &rr.timezone, &rr.excludedUntil); err != nil {
			return nil, err
		}
		rr.runTime = timeOfDayToDuration(runTime)
		rr.daysOfWeek = int32sToIntSlice(days)
		out = append(out, rr)
	}
	return out, rows.Err()
}

// createDueReadinessRuns creates a pending readiness run for each room
// whose run time has come today (within the grace window) and that
// doesn't have one yet. Excluded rooms are skipped, as for the power
// schedule.
func (s *Scheduler) createDueReadinessRuns(ctx context.Context) error {
	rooms, err := s.collectReadinessRooms(ctx)
	if err != nil {
		return err
	}
	books, err := s.loadDateBooks(ctx)
	if err != nil {
		return err
	}
	nowUTC := s.wallNow().UTC()
	for _, rr := range rooms {
		loc, err := time.LoadLocation(rr.timezone)
		if err != nil {
			s.log.Warn("readiness: bad timezone, skipping room", "room", rr.roomID, "tz", rr.timezone)
			continue
		}
		nowLocal := nowUTC.In(loc)
		if rr.excludedUntil != nil &&
			!nowLocal.Truncate(24*time.Hour).After(rr.excludedUntil.Truncate(24*time.Hour)) {
			continue
		}
		// A closed one-off date skips the check; an open one runs it even
		// on a day the readiness schedule doesn't.
		if d, ok := books[rr.customerID].lookup(rr.roomID, nowLocal); ok {
			if d.closed {
				continue
			}
		} else if !containsInt(rr.daysOfWeek, isoWeekday(nowLocal)) {
			continue
		}
		due := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(),
			int(rr.runTime/time.Hour), int((rr.runTime%time.Hour)/time.Minute), 0, 0, loc).UTC()
		if nowUTC.Before(due) || nowUTC.Sub(due) > s.cfg.GraceWindow {
			continue
		}
		var runID string
		err = s.pool.QueryRow(ctx, `
			INSERT INTO nightly_run (customer_id, room_id, kind, routine_id, scheduled_at, phase, status, phase_changed_at)
			SELECT $1::uuid, $2::uuid, 'readiness', $3::uuid, $4::timestamptz, 'pending', 'in_progress', now()
			 WHERE NOT EXISTS (
			   SELECT 1 FROM nightly_run
			    WHERE room_id = $2::uuid AND kind = 'readiness' AND status = 'in_progress')
			ON CONFLICT (room_id, kind, scheduled_at) DO NOTHING
			RETURNING id::text`, rr.customerID, rr.roomID, rr.routineID, due).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // today's run exists, or one is still in progress
		}
		if err != nil {
			s.log.Warn("readiness: insert run failed", "room", rr.roomID, "error", err)
			continue
		}
		s.log.Info("readiness: run created", "run", runID, "room", rr.roomName,
			"scheduled_at", due.Format(time.RFC3339))
	}
	return nil
}

// startReadinessRuns starts each pending readiness run, unless its room's
// power run is still in progress: then it waits (recording the wait as a
// step) for up to the grace window, so a routine never tests a room that's
// still powering on.
func (s *Scheduler) startReadinessRuns(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT nr.id::text, nr.customer_id::text, nr.room_id::text, r.name,
		       nr.routine_id::text, nr.scheduled_at,
		       EXISTS (SELECT 1 FROM nightly_run p
		                WHERE p.room_id = nr.room_id AND p.kind = 'power'
		                  AND p.status = 'in_progress')
		  FROM nightly_run nr
		  JOIN rooms r ON r.id = nr.room_id
		 WHERE nr.kind = 'readiness' AND nr.status = 'in_progress' AND nr.phase = 'pending'`)
	if err != nil {
		return err
	}
	type pendingRun struct {
		id, customerID, roomID, roomName string
		routineID                        *string
		scheduledAt                      time.Time
		powerBusy                        bool
	}
	var pending []pendingRun
	for rows.Next() {
		var p pendingRun
		if err := rows.Scan(&p.id, &p.customerID, &p.roomID, &p.roomName,
			&p.routineID, &p.scheduledAt, &p.powerBusy); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	nowUTC := s.wallNow().UTC()
	for _, p := range pending {
		waited := nowUTC.Sub(p.scheduledAt)
		if p.powerBusy {
			if err := s.noteReadinessWait(ctx, p.id, p.customerID); err != nil {
				s.log.Warn("readiness: recording wait failed", "run", p.id, "error", err)
			}
			if waited < s.cfg.GraceWindow {
				continue
			}
			s.log.Warn("readiness: power cycle still running after the wait; starting anyway",
				"run", p.id, "room", p.roomName)
		}
		if err := s.endReadinessWait(ctx, p.id, waited, !p.powerBusy); err != nil {
			s.log.Warn("readiness: recording wait failed", "run", p.id, "error", err)
		}

		// Started now: the run's duration measures the routine, not the wait.
		if _, err := s.pool.Exec(ctx,
			`UPDATE nightly_run SET phase = 'testing', started_at = now(), phase_changed_at = now() WHERE id = $1`,
			p.id); err != nil {
			s.log.Warn("readiness: start failed", "run", p.id, "error", err)
			continue
		}
		if s.executor == nil || !s.executor.cfg.Enabled {
			s.finishReadiness(ctx, p.id, "failed", "readiness checks are switched off on this platform")
			continue
		}
		took, err := s.executor.MaybeStart(ctx, RunContext{
			RunID: p.id, CustomerID: p.customerID, RoomID: p.roomID, RoomName: p.roomName,
			RoutineID: p.routineID,
		})
		switch {
		case err != nil:
			s.finishReadiness(ctx, p.id, "failed", "couldn't start the routine: "+err.Error())
		case !took:
			// No routine any more, or one with no steps: nothing to check.
			s.finishReadiness(ctx, p.id, "failed", "the routine has no steps")
		default:
			s.log.Info("readiness: routine started", "run", p.id, "room", p.roomName)
		}
	}
	return nil
}

// noteReadinessWait records, once, that the run is waiting for the room's
// power cycle.
func (s *Scheduler) noteReadinessWait(ctx context.Context, runID, customerID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO nightly_step_result (customer_id, run_id, step_index, step_name, step_type, passed, started_at)
		SELECT $1, $2, $3, 'Waited for the power cycle to finish', 'wait', true, now()
		 WHERE NOT EXISTS (SELECT 1 FROM nightly_step_result WHERE run_id = $2 AND step_index = $3)`,
		customerID, runID, readinessWaitStepIndex)
	return err
}

// endReadinessWait completes the wait step, if the run waited.
func (s *Scheduler) endReadinessWait(ctx context.Context, runID string, waited time.Duration, finished bool) error {
	actual, _ := json.Marshal(map[string]any{
		"waited_seconds":       int(waited.Seconds()),
		"power_cycle_finished": finished,
	})
	_, err := s.pool.Exec(ctx, `
		UPDATE nightly_step_result SET completed_at = now(), actual = $3
		 WHERE run_id = $1 AND step_index = $2 AND completed_at IS NULL`,
		runID, readinessWaitStepIndex, actual)
	return err
}

func (s *Scheduler) finishReadiness(ctx context.Context, runID, phase, reason string) {
	s.log.Warn("readiness: run failed", "run", runID, "reason", reason)
	if _, err := s.pool.Exec(ctx, `
		UPDATE nightly_run SET phase = $2, status = 'failed', completed_at = now(), failure_reason = $3
		 WHERE id = $1`, runID, phase, reason); err != nil {
		s.log.Warn("readiness: finish failed", "run", runID, "error", err)
	}
}
