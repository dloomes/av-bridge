package nightly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

// Room alerts from finished runs (migration 0066). A readiness run (the
// health check) or a power cycle that fails opens an alert on the room,
// naming what failed; the next one of the same kind that succeeds resolves
// it. Runs finish in several places (the executor, the scheduler, the
// stuck-run sweep), so rather than hook each one, every tick picks up runs
// that have finished since and haven't been reflected yet.
//
// Notifications go out for newly opened alerts through the usual channels;
// ServiceNow incidents are opened, and resolved with the alert, by the
// dispatcher's ticket sync like any other alert's.

const (
	alertKeyReadinessFailed = "readiness_failed"
	alertKeyPowerFailed     = "power_cycle_failed"
	runAlertSeverity        = "warning"
	runAlertBatch           = 500
	// Failed steps named in an alert's message; the rest are counted.
	runAlertMaxSteps = 5
)

// SetDispatcher lets failed runs notify (nil: alerts are recorded only).
func (s *Scheduler) SetDispatcher(d *notify.Dispatcher) { s.dispatcher = d }

type finishedRun struct {
	id, customerID, roomID, roomName string
	kind, status, reason             string
}

func (s *Scheduler) syncRunAlerts(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT nr.id::text, nr.customer_id::text, nr.room_id::text, r.name,
		       nr.kind, nr.status, COALESCE(nr.failure_reason, '')
		  FROM nightly_run nr
		  JOIN rooms r ON r.id = nr.room_id
		 WHERE nr.completed_at IS NOT NULL AND nr.alert_checked_at IS NULL
		 ORDER BY nr.completed_at
		 LIMIT $1`, runAlertBatch)
	if err != nil {
		return err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (finishedRun, error) {
		var f finishedRun
		err := r.Scan(&f.id, &f.customerID, &f.roomID, &f.roomName, &f.kind, &f.status, &f.reason)
		return f, err
	})
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := s.reflectRun(ctx, run); err != nil {
			s.log.Warn("room alert sync failed", "run", run.id, "error", err)
			continue // retried next tick
		}
	}
	return nil
}

func (s *Scheduler) reflectRun(ctx context.Context, run finishedRun) error {
	key := alertKeyPowerFailed
	if run.kind == "readiness" {
		key = alertKeyReadinessFailed
	}
	switch run.status {
	case "failed":
		msg, failures, err := s.runFailureMessage(ctx, run)
		if err != nil {
			return err
		}
		payload := map[string]any{"run_id": run.id, "run_kind": run.kind, "failures": failures}
		if run.reason != "" {
			payload["reason"] = run.reason
		}
		id, opened, openedAt, err := s.raiseRoomAlert(ctx, run, key, msg, payload)
		if err != nil {
			return err
		}
		if opened && s.dispatcher != nil {
			s.dispatcher.Dispatch(notify.AlertEvent{
				AlertID: id, CustomerID: run.customerID,
				RoomID: run.roomID, RoomName: run.roomName,
				AlertKey: key, Severity: runAlertSeverity, Message: msg,
				OpenedAt: openedAt, Payload: payload,
			})
		}
	case "succeeded":
		if _, err := s.pool.Exec(ctx, `
			UPDATE alerts SET status = 'resolved', resolved_at = now(), resolved_by = 'auto:recovered'
			 WHERE room_id = $1 AND alert_key = $2 AND status <> 'resolved'`, run.roomID, key); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE nightly_run SET alert_checked_at = now() WHERE id = $1`, run.id)
	return err
}

// raiseRoomAlert updates the room's unresolved alert of this kind (open or
// acknowledged: a repeat failure isn't a new incident) or opens one.
func (s *Scheduler) raiseRoomAlert(ctx context.Context, run finishedRun, key, msg string, payload map[string]any) (id string, opened bool, openedAt time.Time, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE alerts SET message = $3, payload = $4
		 WHERE room_id = $1 AND alert_key = $2 AND status <> 'resolved'
		 RETURNING id::text, opened_at`, run.roomID, key, msg, payload).Scan(&id, &openedAt)
	if err == nil {
		return id, false, openedAt, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, time.Time{}, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO alerts (customer_id, room_id, alert_key, severity, message, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, opened_at`,
		run.customerID, run.roomID, key, runAlertSeverity, msg, payload).Scan(&id, &openedAt)
	return id, err == nil, openedAt, err
}

// runFailureMessage says what failed: the failed steps by name (with
// their error) for a readiness run, or the run's reason.
func (s *Scheduler) runFailureMessage(ctx context.Context, run finishedRun) (string, []map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT step_name, COALESCE(error, '')
		  FROM nightly_step_result
		 WHERE run_id = $1 AND NOT passed
		 ORDER BY step_index`, run.id)
	if err != nil {
		return "", nil, err
	}
	failures, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]string, error) {
		var name, e string
		err := r.Scan(&name, &e)
		return map[string]string{"step": name, "error": e}, err
	})
	if err != nil {
		return "", nil, err
	}

	what := "Power cycle failed"
	if run.kind == "readiness" {
		what = "Room Readiness checks failed"
	}
	var parts []string
	for i, f := range failures {
		if i == runAlertMaxSteps {
			parts = append(parts, fmt.Sprintf("and %d more", len(failures)-runAlertMaxSteps))
			break
		}
		if f["error"] != "" {
			parts = append(parts, f["step"]+" ("+f["error"]+")")
		} else {
			parts = append(parts, f["step"])
		}
	}
	detail := strings.Join(parts, "; ")
	if detail == "" {
		detail = run.reason
	}
	if detail == "" {
		return fmt.Sprintf("%s in %s", what, run.roomName), failures, nil
	}
	return fmt.Sprintf("%s in %s: %s", what, run.roomName, detail), failures, nil
}
