package nightly

// Integration test: Room Readiness steps with a device that has no Collector
// (Poly Lens managed). Same TEST_DATABASE_URL harness as ping_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestNoCollectorReadiness(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping no-collector readiness test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('no-collector-nightly-fake') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Boardroom') RETURNING id::text`, cust, b)
	lens := q(`INSERT INTO devices (customer_id, room_id, source, platform, name, type, protocol, latest_status, last_seen_at, capabilities)
	           VALUES ($1, $2, 'poly_lens', 'windows', 'Lens Room', 'conferencing', 'poly_lens', 'online', now(), '{"power":{"on":true,"off":true}}')
	           RETURNING id::text`, cust, room)
	if _, err := su.Exec(ctx, `INSERT INTO telemetry (customer_id, device_id, status, metrics, ts)
	                           VALUES ($1, $2, 'online', '{"people_count": 3}', now() - interval '10 seconds')`, cust, lens); err != nil {
		t.Fatal(err)
	}

	e := NewExecutor(pool, ExecutorConfig{Enabled: true, DryRun: true}, quiet, ctx)
	run := RunContext{RunID: "00000000-0000-0000-0000-00000000f003", CustomerID: cust, RoomID: room, RoomName: "Boardroom", ForceRealDispatch: true}
	mkStep := func(v map[string]any) step {
		raw, _ := json.Marshal(v)
		var s step
		json.Unmarshal(raw, &s)
		s.Raw = raw
		return s
	}
	target := map[string]any{"device_id": lens}
	const skipped = "skipped: managed by Poly Lens"

	// Power steps skip it.
	for _, typ := range []string{"power_on", "power_off"} {
		r := e.dispatchStep(ctx, run, mkStep(map[string]any{"type": typ, "target": map[string]any{"scope": "room"}}))
		actual, _ := r.Actual.(map[string]any)
		if !r.Passed || actual["note"] != skipped {
			t.Errorf("%s: passed=%v actual=%v error=%q", typ, r.Passed, r.Actual, r.Error)
		}
	}

	// Ping skips it.
	r := e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "ping", "target": target}))
	actual, _ := r.Actual.(map[string]any)
	if !r.Passed || actual["Lens Room"] != skipped {
		t.Errorf("ping: passed=%v actual=%v error=%q", r.Passed, r.Actual, r.Error)
	}

	// A device command through dispatchOne is skipped, not failed.
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "device_command", "target": target, "command": "mute"}))
	if !r.Passed || r.Error != "" {
		t.Errorf("device_command: passed=%v error=%q actual=%v", r.Passed, r.Error, r.Actual)
	}

	// A check uses its latest report, without queueing a poll.
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "check_metric", "target": target,
		"metric": "people_count", "operator": "gt", "threshold": 1}))
	if !r.Passed {
		t.Errorf("check_metric: %s", r.Error)
	}
	if len(r.Readings) != 1 || r.Readings[0].Source != "last_report" {
		t.Errorf("check_metric readings: %+v", r.Readings)
	}
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "expect_status", "target": target, "status": "online"}))
	if !r.Passed {
		t.Errorf("expect_status: %s", r.Error)
	}

	if n := q(`SELECT count(*)::text FROM commands WHERE device_id = $1`, lens); n != "0" {
		t.Errorf("commands rows for the Lens device = %s, want 0", n)
	}

	// A Neat Pulse room says so; a room mixing Lens and Neat says "no Collector".
	neatRoom := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Huddle') RETURNING id::text`, cust, b)
	neat := q(`INSERT INTO devices (customer_id, room_id, source, platform, name, type, latest_status, last_seen_at)
	           VALUES ($1, $2, 'neat_pulse', 'android', 'Neat Room', 'zoom_room', 'online', now()) RETURNING id::text`, cust, neatRoom)
	nrun := run
	nrun.RoomID, nrun.RoomName = neatRoom, "Huddle"
	r = e.dispatchStep(ctx, nrun, mkStep(map[string]any{"type": "power_on", "target": map[string]any{"scope": "room"}}))
	if actual, _ := r.Actual.(map[string]any); !r.Passed || actual["note"] != "skipped: managed by Neat Pulse" {
		t.Errorf("neat power_on: passed=%v actual=%v", r.Passed, r.Actual)
	}
	r = e.dispatchStep(ctx, nrun, mkStep(map[string]any{"type": "ping", "target": map[string]any{"device_id": neat}}))
	if actual, _ := r.Actual.(map[string]any); !r.Passed || actual["Neat Room"] != "skipped: managed by Neat Pulse" {
		t.Errorf("neat ping: passed=%v actual=%v", r.Passed, r.Actual)
	}
	if _, err := su.Exec(ctx, `UPDATE devices SET room_id = $1 WHERE id = $2`, neatRoom, lens); err != nil {
		t.Fatal(err)
	}
	r = e.dispatchStep(ctx, nrun, mkStep(map[string]any{"type": "power_off", "target": map[string]any{"scope": "room"}}))
	if actual, _ := r.Actual.(map[string]any); !r.Passed || actual["note"] != "skipped: no Collector" {
		t.Errorf("mixed power_off: passed=%v actual=%v", r.Passed, r.Actual)
	}
}

func TestSkippedMsg(t *testing.T) {
	for src, want := range map[string]string{
		"poly_lens": "skipped: managed by Poly Lens", "neat_pulse": "skipped: managed by Neat Pulse", "": "skipped: no Collector",
	} {
		if got := skippedMsg(src); got != want {
			t.Errorf("skippedMsg(%q) = %q", src, got)
		}
	}
	if got := skippedReason([]resolvedDevice{{source: "neat_pulse"}, {source: "neat_pulse"}}); got != "skipped: managed by Neat Pulse" {
		t.Errorf("shared = %q", got)
	}
	if got := skippedReason([]resolvedDevice{{source: "neat_pulse"}, {source: "poly_lens"}}); got != "skipped: no Collector" {
		t.Errorf("mixed = %q", got)
	}
}
