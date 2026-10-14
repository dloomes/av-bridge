package nightly

// Integration test for readiness checks (readiness.go) against a real
// Postgres. Same TEST_DATABASE_URL harness as scheduler_integration_test.go.

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestReadinessChecks(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping readiness integration test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("app_admin", "app_admin_dev")
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	cust := q(`INSERT INTO customers (name) VALUES ('readiness-sched-test') RETURNING id::text`)
	// Timezone lives with the shared settings; the power schedule is off.
	exec(`INSERT INTO nightly_schedule (customer_id, timezone, enabled) VALUES ($1, 'UTC', false)`, cust)
	check := q(`INSERT INTO nightly_test_routine (customer_id, name, steps)
	            VALUES ($1, 'Standard', '[{"type":"section","label":"Start"}]') RETURNING id::text`, cust)
	empty := q(`INSERT INTO nightly_test_routine (customer_id, name, steps)
	            VALUES ($1, 'Empty', '[]') RETURNING id::text`, cust)
	exec(`INSERT INTO readiness_schedule (customer_id, enabled, test_routine_id, run_time, days_of_week)
	      VALUES ($1, true, $2, '07:45', ARRAY[1,2,3,4,5,6,7])`, cust, check)
	t.Cleanup(func() {
		su.Exec(context.Background(), `UPDATE readiness_schedule SET enabled = false WHERE customer_id = $1`, cust)
	})
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	plain := room("Boardroom")   // default routine and time
	later := room("Training")    // own time, 08:00
	excluded := room("Workshop") // excluded today
	busy := room("Court 1")      // power cycle still running at 07:45
	stuck := room("Court 2")     // power cycle never finishes
	hollow := room("Huddle")     // routine with no steps
	exec(`INSERT INTO room_readiness_config (customer_id, room_id, run_time) VALUES ($1, $2, '08:00')`, cust, later)
	exec(`INSERT INTO room_readiness_config (customer_id, room_id, test_routine_id) VALUES ($1, $2, $3)`, cust, hollow, empty)
	exec(`INSERT INTO room_nightly_config (customer_id, room_id, excluded_until, excluded_reason)
	      VALUES ($1, $2, '2030-01-07', 'planned_maintenance')`, cust, excluded)
	// Power runs still warming up; phase times on the test's clock. The
	// busy room's ends when the test says, the stuck room's never.
	powerRun := func(roomID, since string) string {
		return q(`INSERT INTO nightly_run (customer_id, room_id, kind, scheduled_at, phase, status, started_at, phase_changed_at)
		          VALUES ($1, $2, 'power', '2030-01-06T19:00:00Z', 'warming', 'in_progress', now(), $3::timestamptz)
		          RETURNING id::text`, cust, roomID, since)
	}
	busyPower := powerRun(busy, "2030-01-07T07:46:00Z")
	powerRun(stuck, "2030-01-08T00:00:00Z")

	clock := time.Date(2030, 1, 7, 7, 46, 0, 0, time.UTC)
	s := NewScheduler(pool, Config{TickInterval: time.Minute, GraceWindow: 30 * time.Minute, WarmupSeconds: 60}, quiet)
	s.now = func() time.Time { return clock }
	s.SetExecutor(NewExecutor(pool, ExecutorConfig{Enabled: true, DryRun: true, StepTimeout: time.Minute}, quiet, ctx))

	runOf := func(roomID string) (id, phase, status, reason string, ok bool) {
		err := su.QueryRow(ctx, `SELECT id::text, phase, status, COALESCE(failure_reason, '') FROM nightly_run
		                          WHERE room_id = $1 AND kind = 'readiness'`, roomID).Scan(&id, &phase, &status, &reason)
		return id, phase, status, reason, err == nil
	}
	// The executor finishes runs on its own goroutine.
	waitFinished := func(roomID string) (phase, status, reason string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			_, phase, status, reason, _ = runOf(roomID)
			if status != "in_progress" {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("readiness run for %s didn't finish", roomID)
		return
	}

	// 07:46 — the default 07:45 check runs and passes.
	s.tick(ctx)
	if phase, status, reason := waitFinished(plain); status != "succeeded" || phase != "ready" {
		t.Errorf("boardroom: %s / %s (%s)", phase, status, reason)
	}
	if _, _, _, _, ok := runOf(later); ok {
		t.Error("a room with its own 08:00 check ran at 07:45")
	}
	if _, _, _, _, ok := runOf(excluded); ok {
		t.Error("an excluded room was checked")
	}
	// The room's power cycle is still running: the check waits, and says so.
	_, phase, _, _, ok := runOf(busy)
	if !ok || phase != "pending" {
		t.Fatalf("busy room: phase %q (exists %v), want pending", phase, ok)
	}
	busyRun, _, _, _, _ := runOf(busy)
	if n := q(`SELECT count(*)::text FROM nightly_step_result WHERE run_id = $1 AND step_index = -1`, busyRun); n != "1" {
		t.Errorf("wait step rows: %s, want 1", n)
	}
	// Empty routine: nothing to check, so the run fails with the reason.
	if _, status, reason := waitFinished(hollow); status != "failed" || !strings.Contains(reason, "no steps") {
		t.Errorf("empty routine: %s (%s)", status, reason)
	}

	// 07:50 — the power cycle has finished: the check starts, recording
	// how long it waited.
	exec(`UPDATE nightly_run SET status = 'succeeded', phase = 'ready', completed_at = now() WHERE id = $1`, busyPower)
	clock = time.Date(2030, 1, 7, 7, 50, 0, 0, time.UTC)
	s.tick(ctx)
	if _, status, reason := waitFinished(busy); status != "succeeded" {
		t.Errorf("busy room after the power cycle: %s (%s)", status, reason)
	}
	if got := q(`SELECT actual->>'waited_seconds' || '/' || (actual->>'power_cycle_finished')
	             FROM nightly_step_result WHERE run_id = $1 AND step_index = -1`, busyRun); got != "300/true" {
		t.Errorf("wait step: %s, want 300/true", got)
	}

	// 08:01 — the room with its own time runs.
	clock = time.Date(2030, 1, 7, 8, 1, 0, 0, time.UTC)
	s.tick(ctx)
	if _, status, reason := waitFinished(later); status != "succeeded" {
		t.Errorf("training at its own time: %s (%s)", status, reason)
	}

	// 08:16 — still waiting is fine; 08:16 is past the 30-minute grace for
	// the stuck room's 07:45 check, so it starts anyway and says so.
	clock = time.Date(2030, 1, 7, 8, 16, 0, 0, time.UTC)
	s.tick(ctx)
	if _, status, _ := waitFinished(stuck); status != "succeeded" {
		t.Errorf("stuck room after the grace window: %s", status)
	}
	stuckRun, _, _, _, _ := runOf(stuck)
	if got := q(`SELECT actual->>'power_cycle_finished' FROM nightly_step_result WHERE run_id = $1 AND step_index = -1`, stuckRun); got != "false" {
		t.Errorf("stuck room wait step: power_cycle_finished=%s, want false", got)
	}

	// Each room got exactly one readiness run, and no power runs were made.
	if n := q(`SELECT count(*)::text FROM nightly_run nr JOIN rooms r ON r.id = nr.room_id
	           WHERE r.customer_id = $1 AND nr.kind = 'readiness'`, cust); n != "5" {
		t.Errorf("readiness runs: %s, want 5", n)
	}
}
