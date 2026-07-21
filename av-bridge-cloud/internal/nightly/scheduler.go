// Package nightly drives the Room Readiness lifecycle: enacting scheduled
// power-off / power-on cycles per room and (eventually) executing test
// routines after warm-up.
//
// Slice 3 (this file): the scheduler + state-machine skeleton. Dispatches
// power commands in DRY-RUN mode by default — logs "would send X to
// device Y" but doesn't hit the device or its adapter. Real command
// dispatch is a follow-up slice; the split lets us prove timing +
// state-machine correctness against real customer data first, without
// risking a stray reboot on someone's boardroom display.
//
// Design reference: docs/nightly-lifecycle-spec.md
//
// Runs as app_admin (BYPASSRLS) because it operates across every tenant
// on a schedule of its own, not on behalf of a request.
package nightly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config bundles the scheduler's tunables. All exposed via env vars in
// config.FromEnv so operators can dial the loop cadence and warm-up
// timing without a rebuild.
type Config struct {
	// TickInterval — how often the scheduler wakes to look at the fleet.
	// 60s is fine for room-level events (worst case a room powers off
	// 59s late). Sub-minute cadence just wastes cycles.
	TickInterval time.Duration

	// GraceWindow — how far past a scheduled time we'll still act on it.
	// A cloud restart at 19:03 should still catch the 19:00 power-off.
	// Too long and a stale schedule (customer changed 19:00 → 20:00 at
	// 19:30) fires a stale event. 30m is the sweet spot.
	GraceWindow time.Duration

	// WarmupSeconds — after power-on completes, how long to wait before
	// declaring the room ready (or in Phase B, before starting the test
	// routine). Displays typically need 30-60s to reach steady state.
	WarmupSeconds int

	// DryRun — when true, no device commands are dispatched. The state
	// machine still advances (with simulated instant power ack) and rows
	// still land in nightly_run / nightly_step_result. Log lines carry a
	// [dry-run] tag so grep-ability is trivial.
	DryRun bool
}

// Scheduler is the top-level orchestrator. One instance per cloud process.
type Scheduler struct {
	pool *pgxpool.Pool
	cfg  Config
	log  *slog.Logger

	// now overrides time.Now for tests. Nil in prod → uses time.Now().
	now func() time.Time
}

func NewScheduler(pool *pgxpool.Pool, cfg Config, log *slog.Logger) *Scheduler {
	return &Scheduler{pool: pool, cfg: cfg, log: log}
}

// Run blocks until ctx is cancelled, ticking on cfg.TickInterval.
func (s *Scheduler) Run(ctx context.Context) {
	if s.cfg.TickInterval <= 0 {
		s.log.Warn("nightly scheduler disabled (tick_interval non-positive)")
		return
	}
	s.log.Info("nightly scheduler started",
		"tick", s.cfg.TickInterval,
		"grace", s.cfg.GraceWindow,
		"warmup_seconds", s.cfg.WarmupSeconds,
		"dry_run", s.cfg.DryRun,
	)
	t := time.NewTicker(s.cfg.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick is one pass of the scheduler. Exposed so tests can drive it
// deterministically without waiting on a ticker.
func (s *Scheduler) tick(ctx context.Context) {
	// Two-step: (a) create new runs for rooms whose scheduled_off is due
	// but has no run yet, then (b) advance every in-progress run through
	// its state machine based on wall-clock progress.
	//
	// Order matters: create-first lets a freshly-created run advance in
	// the same tick when DryRun collapses phases. In real dispatch mode
	// the create step just posts the power-off commands and the advance
	// step doesn't move the run until the commands complete.
	if err := s.createDueRuns(ctx); err != nil {
		s.log.Warn("nightly scheduler create-runs error", "error", err)
	}
	if err := s.advanceRuns(ctx); err != nil {
		s.log.Warn("nightly scheduler advance-runs error", "error", err)
	}
}

// roomView is a de-nested view of every enabled room's effective schedule
// — customer default COALESCE'd with per-room override. Loaded per tick,
// cheap for small fleets; if fleets grow past ~50k rooms this becomes a
// keyset scan instead.
type roomView struct {
	customerID    string
	roomID        string
	roomName      string
	powerOffTime  time.Duration // seconds into the day
	powerOnTime   time.Duration
	daysOfWeek    []int // ISO 1-7
	timezone      string
	excludedUntil *time.Time // if non-nil AND today or later → skip
}

func (s *Scheduler) collectEnabledRooms(ctx context.Context) ([]roomView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
		  ns.customer_id::text,
		  r.id::text                                        AS room_id,
		  r.name                                            AS room_name,
		  COALESCE(rnc.power_off_time, ns.power_off_time)   AS eff_power_off,
		  COALESCE(rnc.power_on_time,  ns.power_on_time)    AS eff_power_on,
		  COALESCE(rnc.days_of_week,   ns.days_of_week)     AS eff_days,
		  ns.timezone,
		  rnc.excluded_until
		FROM nightly_schedule ns
		JOIN rooms r                        ON r.customer_id = ns.customer_id
		LEFT JOIN room_nightly_config rnc   ON rnc.room_id = r.id
		WHERE ns.enabled = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roomView
	for rows.Next() {
		var (
			rv                     roomView
			off, on                time.Time
			days                   []int32
			excludedUntil          *time.Time
		)
		if err := rows.Scan(
			&rv.customerID, &rv.roomID, &rv.roomName,
			&off, &on, &days, &rv.timezone, &excludedUntil,
		); err != nil {
			return nil, err
		}
		rv.powerOffTime = timeOfDayToDuration(off)
		rv.powerOnTime = timeOfDayToDuration(on)
		rv.daysOfWeek = int32sToIntSlice(days)
		rv.excludedUntil = excludedUntil
		out = append(out, rv)
	}
	return out, rows.Err()
}

// createDueRuns inspects every enabled room and, if the room's most-recent
// power-off time within the grace window doesn't yet have a nightly_run
// row, creates one and marks it in the scheduled_off phase.
//
// The heavy lifting is timezone resolution — power_off_time is stored as
// a wall-clock time in the customer's timezone; we need to know the UTC
// instant that means at "today" in that zone.
func (s *Scheduler) createDueRuns(ctx context.Context) error {
	rooms, err := s.collectEnabledRooms(ctx)
	if err != nil {
		return err
	}
	nowUTC := s.wallNow().UTC()
	for _, rv := range rooms {
		loc, err := time.LoadLocation(rv.timezone)
		if err != nil {
			// Bad timezone in the row. Log once and skip — don't block
			// the whole fleet on one bad customer.
			s.log.Warn("nightly: bad timezone, skipping room",
				"customer", rv.customerID, "room", rv.roomID,
				"tz", rv.timezone, "error", err)
			continue
		}
		nowLocal := nowUTC.In(loc)

		// Manual exclusion — treat as fully skipped for both off and on.
		if rv.excludedUntil != nil {
			// excluded_until is inclusive: today or earlier means still
			// excluded. Compare dates (not instants).
			if !nowLocal.Truncate(24*time.Hour).After(rv.excludedUntil.Truncate(24 * time.Hour)) {
				continue
			}
		}

		// Only trigger power-off on a day the customer marked as active.
		todayISO := isoWeekday(nowLocal)
		if !containsInt(rv.daysOfWeek, todayISO) {
			continue
		}

		// Today's scheduled power-off instant, in local tz then converted
		// to UTC for storage / comparison with nightly_run.scheduled_at.
		scheduledLocal := time.Date(
			nowLocal.Year(), nowLocal.Month(), nowLocal.Day(),
			int(rv.powerOffTime/time.Hour),
			int((rv.powerOffTime%time.Hour)/time.Minute),
			0, 0, loc,
		)
		scheduledUTC := scheduledLocal.UTC()

		// If it's still in the future, nothing to do this tick.
		if nowUTC.Before(scheduledUTC) {
			continue
		}
		// If it's too far in the past, we missed the window (cloud was
		// down all evening). Skip — don't fire a stale event.
		if nowUTC.Sub(scheduledUTC) > s.cfg.GraceWindow {
			continue
		}

		// Insert the run row. Uniqueness on (room_id, scheduled_at) keeps
		// duplicate ticks harmless.
		res, err := s.pool.Exec(ctx, `
			INSERT INTO nightly_run (customer_id, room_id, scheduled_at, phase, status, started_at)
			VALUES ($1, $2, $3, 'scheduled_off', 'in_progress', now())
			ON CONFLICT (room_id, scheduled_at) DO NOTHING
		`, rv.customerID, rv.roomID, scheduledUTC)
		if err != nil {
			s.log.Warn("nightly: insert run failed",
				"room", rv.roomID, "scheduled_at", scheduledUTC, "error", err)
			continue
		}
		if res.RowsAffected() == 0 {
			continue // dedup — run already existed for this cycle
		}
		s.log.Info("nightly: new run created",
			"customer", rv.customerID, "room", rv.roomID, "room_name", rv.roomName,
			"scheduled_at", scheduledUTC.Format(time.RFC3339),
			"power_on_at_local", formatDuration(rv.powerOnTime),
			"dry_run", s.cfg.DryRun,
		)

		// Dispatch power-off commands. In dry-run we just log; in real
		// mode this'd enqueue commands via the command queue and let
		// the runner poll for completion.
		s.dispatchPower(ctx, rv, "power_off")
	}
	return nil
}

// advanceRuns walks every in-progress run through its state machine.
// The transition logic is:
//
//   scheduled_off  — commands dispatched, awaiting device confirmation
//                    (in dry-run: instant → off)
//   off            — powered off; wait until power_on time
//   scheduled_on   — commands dispatched, awaiting confirmation
//                    (in dry-run: instant → waking)
//   waking         — waiting for warm-up half (in dry-run: instant → warming)
//   warming        — final warm-up window before ready
//   ready          — terminal success
//   failed         — terminal failure
func (s *Scheduler) advanceRuns(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT nr.id::text, nr.customer_id::text, nr.room_id::text, r.name,
		       nr.phase, nr.scheduled_at, nr.started_at,
		       COALESCE(rnc.power_on_time, ns.power_on_time) AS eff_power_on,
		       COALESCE(rnc.days_of_week,  ns.days_of_week)  AS eff_days,
		       ns.timezone
		  FROM nightly_run nr
		  JOIN rooms r                       ON r.id = nr.room_id
		  JOIN nightly_schedule ns           ON ns.customer_id = nr.customer_id
		  LEFT JOIN room_nightly_config rnc  ON rnc.room_id = nr.room_id
		 WHERE nr.status = 'in_progress'
	`)
	if err != nil {
		return err
	}
	type liveRun struct {
		id, customerID, roomID, roomName string
		phase                            string
		scheduledAt, startedAt           time.Time
		powerOnTime                      time.Duration
		daysOfWeek                       []int
		timezone                         string
	}
	var runs []liveRun
	for rows.Next() {
		var (
			r          liveRun
			startedAt  *time.Time
			powerOn    time.Time
			days       []int32
		)
		if err := rows.Scan(
			&r.id, &r.customerID, &r.roomID, &r.roomName,
			&r.phase, &r.scheduledAt, &startedAt,
			&powerOn, &days, &r.timezone,
		); err != nil {
			rows.Close()
			return err
		}
		if startedAt != nil {
			r.startedAt = *startedAt
		}
		r.powerOnTime = timeOfDayToDuration(powerOn)
		r.daysOfWeek = int32sToIntSlice(days)
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	nowUTC := s.wallNow().UTC()
	for _, r := range runs {
		next, reason, ok := s.decideTransition(r.phase, r.scheduledAt, r.startedAt,
			r.powerOnTime, r.daysOfWeek, r.timezone, nowUTC)
		if !ok {
			continue // no transition due yet
		}

		switch next {
		case "off":
			s.log.Info("nightly: room powered off", "run", r.id, "room", r.roomID, "reason", reason)
			// dispatchPower was already called at scheduled_off create;
			// nothing to do here in dry-run other than move phase.
			if err := s.setPhase(ctx, r.id, next); err != nil {
				s.log.Warn("nightly: phase update failed", "run", r.id, "error", err)
			}
		case "scheduled_on":
			// Look up the room view to dispatch. Cheap re-query — the
			// fleet is small; avoiding the join complexity in advanceRuns.
			s.log.Info("nightly: powering room on", "run", r.id, "room", r.roomID, "reason", reason)
			if err := s.setPhase(ctx, r.id, next); err != nil {
				s.log.Warn("nightly: phase update failed", "run", r.id, "error", err)
				continue
			}
			s.dispatchPowerByRoom(ctx, r.customerID, r.roomID, r.roomName, "power_on")
		case "waking":
			s.log.Debug("nightly: room waking", "run", r.id, "room", r.roomID)
			if err := s.setPhase(ctx, r.id, next); err != nil {
				s.log.Warn("nightly: phase update failed", "run", r.id, "error", err)
			}
		case "warming":
			s.log.Debug("nightly: room warming", "run", r.id, "room", r.roomID)
			if err := s.setPhase(ctx, r.id, next); err != nil {
				s.log.Warn("nightly: phase update failed", "run", r.id, "error", err)
			}
		case "ready":
			s.log.Info("nightly: room ready", "run", r.id, "room", r.roomID, "reason", reason)
			if err := s.finish(ctx, r.id, "ready", "succeeded", ""); err != nil {
				s.log.Warn("nightly: finish failed", "run", r.id, "error", err)
			}
		case "failed":
			s.log.Warn("nightly: run failed", "run", r.id, "room", r.roomID, "reason", reason)
			if err := s.finish(ctx, r.id, "failed", "failed", reason); err != nil {
				s.log.Warn("nightly: finish failed", "run", r.id, "error", err)
			}
		}
	}
	return nil
}

// decideTransition is pure logic — no DB, no side effects — so it's easy
// to unit-test and easy to reason about the state machine as one place.
//
// Returns (nextPhase, reason, transitionDue). If transitionDue is false,
// the run stays in its current phase.
func (s *Scheduler) decideTransition(
	phase string,
	scheduledAt, startedAt time.Time,
	powerOnTime time.Duration,
	daysOfWeek []int,
	timezone string,
	nowUTC time.Time,
) (string, string, bool) {
	switch phase {
	case "scheduled_off":
		// In dry-run, power commands complete instantly → the run rolls
		// from scheduled_off to off on the very next tick after creation.
		// In real mode this transition would wait for command queue acks.
		if s.cfg.DryRun {
			return "off", "dry-run: power-off dispatched instantly", true
		}
		// Non-dry-run: wait for the command queue to confirm. For slice 3
		// we don't have that wiring yet, so leave the run in scheduled_off
		// (it'll never advance). Follow-up slice will read command results.
		return "", "", false

	case "off":
		// Compute the NEXT power-on instant after scheduled_at, on the
		// closest following day that's in daysOfWeek. That handles the
		// Fri-evening → Mon-morning weekend gap correctly.
		loc, err := time.LoadLocation(timezone)
		if err != nil {
			return "failed", "bad timezone: " + err.Error(), true
		}
		schedLocal := scheduledAt.In(loc)
		onLocal := nextPowerOnAfter(schedLocal, powerOnTime, daysOfWeek)
		onUTC := onLocal.UTC()
		if nowUTC.Before(onUTC) {
			return "", "", false
		}
		// Grace check — if we've missed the window by hours (cloud was
		// down over the weekend, say), fail the run rather than power
		// on late. Users don't want the office lights coming on at 2pm.
		if nowUTC.Sub(onUTC) > s.cfg.GraceWindow {
			return "failed",
				fmt.Sprintf("missed power-on window (scheduled %s, now %s)",
					onUTC.Format(time.RFC3339), nowUTC.Format(time.RFC3339)),
				true
		}
		return "scheduled_on", "power-on time reached", true

	case "scheduled_on":
		if s.cfg.DryRun {
			return "waking", "dry-run: power-on dispatched instantly", true
		}
		return "", "", false

	case "waking":
		// Wait half the warm-up window before moving to warming. Splits
		// the total warm-up so a curious watcher sees discrete phases
		// instead of a jump straight to warming. Slightly cosmetic but
		// matches the spec's phase list.
		if s.cfg.WarmupSeconds <= 0 {
			return "warming", "no warm-up configured", true
		}
		if !hasBeenIn(phase, startedAt, nowUTC, time.Duration(s.cfg.WarmupSeconds/2)*time.Second) {
			return "", "", false
		}
		return "warming", fmt.Sprintf("waking finished (%ds)", s.cfg.WarmupSeconds/2), true

	case "warming":
		// The second half of warm-up before we mark the room ready. In
		// Phase B this is where the test routine kicks off.
		if s.cfg.WarmupSeconds <= 0 {
			return "ready", "no warm-up configured", true
		}
		if !hasBeenIn(phase, startedAt, nowUTC, time.Duration(s.cfg.WarmupSeconds)*time.Second) {
			return "", "", false
		}
		return "ready", fmt.Sprintf("warm-up finished (%ds)", s.cfg.WarmupSeconds), true
	}
	return "", "", false
}

// hasBeenIn is a rough proxy: we don't have per-phase timestamps, only
// startedAt (when the run began). For slice 3 we treat "time-in-phase" as
// "time since the run started", so warm-up starts at power-off time. Not
// perfectly accurate but never fires early. A follow-up slice will add
// per-phase timestamps for finer-grained gating.
func hasBeenIn(_ string, startedAt, now time.Time, want time.Duration) bool {
	if startedAt.IsZero() {
		return false
	}
	return now.Sub(startedAt) >= want
}

// setPhase moves a run to a new phase while keeping status = in_progress.
func (s *Scheduler) setPhase(ctx context.Context, runID, phase string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE nightly_run SET phase = $2 WHERE id = $1`, runID, phase)
	return err
}

// finish flips a run terminal — sets phase + status + completed_at, plus
// an optional failure_reason for the failed path.
func (s *Scheduler) finish(ctx context.Context, runID, phase, status, reason string) error {
	var reasonArg any
	if reason == "" {
		reasonArg = nil
	} else {
		reasonArg = reason
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE nightly_run
		   SET phase = $2, status = $3, completed_at = now(), failure_reason = $4
		 WHERE id = $1
	`, runID, phase, status, reasonArg)
	return err
}

// dispatchPower is the seam for real command dispatch. In slice 3 it's
// dry-run: log the intended action and move on. Follow-up slice replaces
// the log line with an INSERT into the command queue.
func (s *Scheduler) dispatchPower(ctx context.Context, rv roomView, action string) {
	s.dispatchPowerByRoom(ctx, rv.customerID, rv.roomID, rv.roomName, action)
}

func (s *Scheduler) dispatchPowerByRoom(ctx context.Context, customerID, roomID, roomName, action string) {
	// Look up which devices in this room support the power action. Rooms
	// with zero capable devices still succeed the phase — the "room" is
	// notionally "powered off" even if we can't literally turn everything
	// off. That's the honest thing to log; alternatives lead to false
	// failure reports.
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, capabilities->>$2 AS supported
		  FROM devices
		 WHERE customer_id = $1
		   AND room_id     = $3
	`, customerID, action, roomID)
	if err != nil {
		s.log.Warn("nightly: dispatch device lookup failed",
			"customer", customerID, "room", roomID, "error", err)
		return
	}
	defer rows.Close()
	var total, capable int
	for rows.Next() {
		var (
			id, name string
			supRaw   *string
		)
		if err := rows.Scan(&id, &name, &supRaw); err != nil {
			continue
		}
		total++
		if supRaw == nil {
			// Adapter hasn't declared capabilities yet — treat as "we
			// don't know if it supports this", don't try. Once the
			// adapter capability slice lands, this branch will shrink.
			continue
		}
		capable++
		tag := "[dry-run]"
		if !s.cfg.DryRun {
			tag = "[dispatch]"
		}
		s.log.Info(tag+" "+action, "device", id, "device_name", name, "room", roomName)
		// Real command insert lives here in the follow-up slice:
		//   INSERT INTO commands (customer_id, device_id, action, status) VALUES (...)
	}
	if err := rows.Err(); err != nil {
		s.log.Warn("nightly: dispatch scan failed", "error", err)
		return
	}
	s.log.Info("nightly: dispatch summary",
		"action", action, "room", roomName,
		"total_devices", total, "capable_devices", capable,
		"dry_run", s.cfg.DryRun)
}

// ── helpers ────────────────────────────────────────────────────────────

// wallNow returns the current time, honouring the test override.
func (s *Scheduler) wallNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// timeOfDayToDuration turns a Postgres `time` (which pgx scans as a
// time.Time with a zero date) into a duration since midnight.
func timeOfDayToDuration(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour +
		time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second
}

func formatDuration(d time.Duration) string {
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%02d:%02d", h, m)
}

// isoWeekday returns 1-7 (Mon-Sun). Go's time.Weekday is Sun=0..Sat=6.
func isoWeekday(t time.Time) int {
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func int32sToIntSlice(xs []int32) []int {
	out := make([]int, len(xs))
	for i, v := range xs {
		out[i] = int(v)
	}
	return out
}

// nextPowerOnAfter — given a scheduled power-off local instant, the daily
// power-on time (duration since midnight), and the set of days_of_week the
// customer treats as active, return the local instant when power should
// come back on.
//
// Two cases:
//   1. Power-on time falls later on the same day as power-off (unusual in
//      prod — typical is 19:00 off / 07:30 on next morning — but valid,
//      and essential for testing). Return the same-day instant regardless
//      of whether today is in daysOfWeek: it's part of the cycle we
//      already committed to.
//   2. Same-day power-on time has already passed (the overnight case).
//      Walk forward day-by-day until we hit one in daysOfWeek. That
//      spans weekends correctly (Fri 19:00 off → Mon 07:30 on when
//      daysOfWeek = Mon-Fri).
//
// If daysOfWeek is empty (invalid config but don't panic), fall back to
// the next calendar day.
var errNoScheduledDay = errors.New("no scheduled day of week within a week")

func nextPowerOnAfter(scheduledOffLocal time.Time, powerOnTime time.Duration, daysOfWeek []int) time.Time {
	loc := scheduledOffLocal.Location()
	// Case 1: same-day power-on that hasn't happened yet.
	sameDay := time.Date(
		scheduledOffLocal.Year(), scheduledOffLocal.Month(), scheduledOffLocal.Day(),
		int(powerOnTime/time.Hour),
		int((powerOnTime%time.Hour)/time.Minute),
		0, 0, loc,
	)
	if sameDay.After(scheduledOffLocal) {
		return sameDay
	}
	// Case 2: overnight — walk forward until we find a scheduled day.
	// Build baseDay in the caller's timezone so day arithmetic doesn't
	// silently slip via UTC-anchored Truncate() near DST boundaries.
	baseDay := time.Date(
		scheduledOffLocal.Year(), scheduledOffLocal.Month(), scheduledOffLocal.Day(),
		0, 0, 0, 0, loc,
	)
	for i := 1; i <= 7; i++ {
		candidateDay := baseDay.AddDate(0, 0, i)
		if len(daysOfWeek) == 0 || containsInt(daysOfWeek, isoWeekday(candidateDay)) {
			return time.Date(
				candidateDay.Year(), candidateDay.Month(), candidateDay.Day(),
				int(powerOnTime/time.Hour),
				int((powerOnTime%time.Hour)/time.Minute),
				0, 0, loc,
			)
		}
	}
	// Shouldn't happen — every ISO weekday is exhausted in 7 iterations.
	return scheduledOffLocal.AddDate(0, 0, 1)
}

// keep errNoScheduledDay referenced so a lint pass doesn't complain if a
// future refactor drops the panic-recovery path.
var _ = errNoScheduledDay
