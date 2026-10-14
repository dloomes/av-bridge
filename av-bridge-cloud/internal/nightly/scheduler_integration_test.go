package nightly

// Integration test for the scheduler's real power dispatch against a real
// Postgres. Runs only when TEST_DATABASE_URL points at a disposable
// Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/nightly/ -run TestScheduler -v

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
	"github.com/dloomes/av-bridge-cloud/internal/devicestatus"
)

func TestSchedulerRealPowerDispatch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping nightly scheduler integration test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('nightly-sched-test') RETURNING id::text`)
	exec(`INSERT INTO nightly_schedule (customer_id, power_off_time, power_on_time, days_of_week, timezone, enabled)
	      VALUES ($1, '19:00', '07:30', ARRAY[1,2,3,4,5,6,7], 'UTC', true)`, cust)
	t.Cleanup(func() {
		su.Exec(context.Background(), `UPDATE nightly_schedule SET enabled = false WHERE customer_id = $1`, cust)
	})
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	failRoom, okRoom := room("Boardroom"), room("Huddle")
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'sched-' || $1::uuid::text, 'c', '\x00') RETURNING id::text`, cust)
	device := func(roomID, name, caps string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol, capabilities)
		          VALUES ($1, $2, $3::uuid, $4 || '-' || $3::text, $4, 'x', NULLIF($5, '')::jsonb) RETURNING id::text`,
			cust, col, roomID, name, caps)
	}
	both := `{"power":{"on":true,"off":true}}`
	display := device(failRoom, "Display", both)
	device(failRoom, "PDU", `{"power":{"on":false,"off":false}}`) // can't power: left out
	device(failRoom, "Camera", "")                                // no capabilities reported
	screen := device(okRoom, "Screen", both)
	// What the portal shows: effective status, with a live collector and
	// the devices reporting offline (as switched-off devices do).
	exec(`UPDATE collectors SET last_seen_at = now() WHERE id = $1`, col)
	exec(`UPDATE devices SET latest_status = 'offline', last_seen_at = now() WHERE collector_id = $1`, col)
	effective := func(dev string) string {
		return q(`SELECT `+devicestatus.EffectiveStatusSQL+` FROM devices d
		          LEFT JOIN collectors c ON c.id = d.collector_id WHERE d.id = $1`, dev)
	}

	clock := time.Date(2030, 1, 7, 19, 1, 0, 0, time.UTC)
	s := NewScheduler(pool, Config{TickInterval: time.Minute, GraceWindow: 30 * time.Minute, WarmupSeconds: 60}, quiet)
	s.now = func() time.Time { return clock }

	runOf := func(roomID string) (id, phase, status, reason string) {
		t.Helper()
		err := su.QueryRow(ctx, `SELECT id::text, phase, status, COALESCE(failure_reason, '') FROM nightly_run WHERE room_id = $1`,
			roomID).Scan(&id, &phase, &status, &reason)
		if err != nil {
			t.Fatalf("run for room: %v", err)
		}
		return
	}
	cmds := func(runID, action string) []string {
		t.Helper()
		rows, err := su.Query(ctx, `SELECT d.name FROM commands c JOIN devices d ON d.id = c.device_id
		                            WHERE c.submitted_by = $1 AND c.name = $2 ORDER BY d.name`, "nightly-schedule:"+runID, action)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}
	finishCmds := func(runID, action, status, errText string) {
		t.Helper()
		exec(`UPDATE commands SET status = $3, error = NULLIF($4, '') WHERE submitted_by = $1 AND name = $2`,
			"nightly-schedule:"+runID, action, status, errText)
	}
	step := func(runID string, idx int) (passed bool, errText string, ok bool) {
		var e *string
		err := su.QueryRow(ctx, `SELECT passed, error FROM nightly_step_result WHERE run_id = $1 AND step_index = $2`,
			runID, idx).Scan(&passed, &e)
		if e != nil {
			errText = *e
		}
		return passed, errText, err == nil
	}

	// Pretend the run has been in its phase for d (phase times come from
	// the database clock; the scheduler's clock is faked).
	inPhaseFor := func(runID string, d time.Duration) {
		t.Helper()
		exec(`UPDATE nightly_run SET phase_changed_at = $2 WHERE id = $1`, runID, clock.Add(-d))
	}
	// 19:01 — runs are created and power-off is queued, only to devices
	// that declare power-off.
	s.tick(ctx)
	failRun, phase, _, _ := runOf(failRoom)
	okRun, _, _, _ := runOf(okRoom)
	if phase != "scheduled_off" {
		t.Fatalf("phase %s, want scheduled_off", phase)
	}
	if got := cmds(failRun, "power_off"); len(got) != 1 || got[0] != "Display" {
		t.Fatalf("power_off queued to %v, want only Display", got)
	}

	// Commands still pending: the run waits.
	s.tick(ctx)
	if _, phase, _, _ = runOf(failRoom); phase != "scheduled_off" {
		t.Fatalf("run moved on before its commands finished (phase %s)", phase)
	}

	// Display refuses power-off: recorded, but the night carries on.
	finishCmds(failRun, "power_off", "failed", "display unreachable")
	finishCmds(okRun, "power_off", "succeeded", "")
	s.tick(ctx)
	if _, phase, _, _ = runOf(failRoom); phase != "off" {
		t.Fatalf("after power-off settled: phase %s, want off", phase)
	}
	if passed, e, ok := step(failRun, powerOffStepIndex); !ok || passed || !strings.Contains(e, "Display: display unreachable") {
		t.Errorf("power-off step: ok=%v passed=%v error=%q", ok, passed, e)
	}
	if passed, _, ok := step(okRun, powerOffStepIndex); !ok || !passed {
		t.Errorf("ok room power-off step: ok=%v passed=%v", ok, passed)
	}
	// The screen switched off: it reads switched_off, not offline. The
	// display refused, so it's just offline.
	if got := effective(screen); got != "switched_off" {
		t.Errorf("switched-off screen reads %q, want switched_off", got)
	}
	if got := effective(display); got != "offline" {
		t.Errorf("display whose power-off failed reads %q, want offline", got)
	}

	// 07:31 next morning — power-on queued.
	clock = time.Date(2030, 1, 8, 7, 31, 0, 0, time.UTC)
	s.tick(ctx)
	if _, phase, _, _ = runOf(failRoom); phase != "scheduled_on" {
		t.Fatalf("morning: phase %s, want scheduled_on", phase)
	}
	if got := cmds(failRun, "power_on"); len(got) != 1 {
		t.Fatalf("power_on queued to %v", got)
	}

	// A failed power-on fails the run with the reason.
	finishCmds(failRun, "power_on", "failed", "no response")
	finishCmds(okRun, "power_on", "succeeded", "")
	s.tick(ctx)
	if _, phase, status, reason := runOf(failRoom); status != "failed" || !strings.Contains(reason, "Display: no response") {
		t.Errorf("failed power-on: phase %s status %s reason %q", phase, status, reason)
	}

	// The other room warms up for the full 60 seconds from power-on, not
	// from the evening before.
	if _, phase, _, _ = runOf(okRoom); phase != "waking" {
		t.Fatalf("ok room after power-on: phase %s, want waking", phase)
	}
	inPhaseFor(okRun, 10*time.Second)
	s.tick(ctx)
	if _, phase, _, _ = runOf(okRoom); phase != "waking" {
		t.Fatalf("warm-up ended early (phase %s)", phase)
	}
	inPhaseFor(okRun, 31*time.Second)
	s.tick(ctx)
	if _, phase, _, _ = runOf(okRoom); phase != "warming" {
		t.Fatalf("phase %s, want warming", phase)
	}
	if got := effective(screen); got != "switched_off" {
		t.Errorf("during the warm-up the screen reads %q, want switched_off", got)
	}
	inPhaseFor(okRun, 31*time.Second)
	s.tick(ctx)
	if _, phase, status, _ := runOf(okRoom); phase != "ready" || status != "succeeded" {
		t.Errorf("ok room: phase %s status %s, want ready / succeeded", phase, status)
	}
	// Still switched off through the warm-up; awake once the room is ready.
	if got := effective(screen); got != "offline" {
		t.Errorf("after the warm-up the screen reads %q, want offline (it's still not reporting)", got)
	}

	// A collector that never answers: past the settle timeout the
	// unfinished commands count as failed and the run moves on.
	room3 := room("Training")
	device(room3, "Projector", both)
	clock = time.Date(2030, 1, 8, 19, 1, 0, 0, time.UTC)
	s.tick(ctx)
	run3, _, _, _ := runOf(room3)
	// The settle timeout runs on the database clock.
	exec(`UPDATE nightly_run SET phase_changed_at = now() - make_interval(secs => $2) WHERE id = $1`,
		run3, (powerSettleTimeout + time.Minute).Seconds())
	s.tick(ctx)
	if _, phase, _, _ := runOf(room3); phase != "off" {
		t.Errorf("stuck power-off: phase %s, want off after the settle timeout", phase)
	}
	if passed, e, ok := step(run3, powerOffStepIndex); !ok || passed || !strings.Contains(e, "no response from the Collector") {
		t.Errorf("stuck power-off step: ok=%v passed=%v error=%q", ok, passed, e)
	}
}
