package telemetry

// Integration test for telemetry rollups and retention. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16 (see
// portalapi/device_move_integration_test.go for the docker line).

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestRollupAndRetention(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping telemetry rollup test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Bring everything else in the shared test database up to date, and
	// restart the backfill so this test's old rows are picked up.
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE telemetry_rollup_state SET backfill_to = NULL, backfill_done = false WHERE id = 1`)

	cust := q(`INSERT INTO customers (name) VALUES ('telemetry-rollups') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'tr-' || $1::text, 'tr', '\x00') RETURNING id::text`, cust)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol)
	          VALUES ($1, $2, 'codec', 'Codec', 'cisco_roomos') RETURNING id::text`, cust, col)

	// reading at noon UTC, daysAgo days back, at hour h; inserted_at NULL
	// for readings from before rollups existed.
	reading := func(daysAgo, hour int, status, metrics string, legacy bool) {
		t.Helper()
		ts := `date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - make_interval(days => $4) + make_interval(hours => $5)`
		if legacy {
			exec(`INSERT INTO telemetry (customer_id, device_id, status, metrics, ts, inserted_at)
			      VALUES ($1, $2, $3, $6::jsonb, `+ts+`, NULL)`, cust, dev, status, daysAgo, hour, metrics)
		} else {
			exec(`INSERT INTO telemetry (customer_id, device_id, status, metrics, ts)
			      VALUES ($1, $2, $3, $6::jsonb, `+ts+`)`, cust, dev, status, daysAgo, hour, metrics)
		}
	}
	type daily struct {
		samples, online, calls, occupied, display int
		signal                                    bool
		peak                                      *int
	}
	summary := func(daysAgo int) (daily, bool) {
		t.Helper()
		var d daily
		err := conn.QueryRow(ctx, `
			SELECT samples, online_samples, call_hours, occupied_hours, display_on_hours, usage_signal, peak_people
			  FROM telemetry_daily
			 WHERE device_id = $1 AND day = (now() AT TIME ZONE 'UTC')::date - $2::int`, dev, daysAgo).Scan(
			&d.samples, &d.online, &d.calls, &d.occupied, &d.display, &d.signal, &d.peak)
		if err == pgx.ErrNoRows {
			return d, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return d, true
	}

	// Readings from before migration 0060 are backfilled by reading time.
	reading(3, 9, "online", `{"active_calls": 1, "people_count": 2}`, true)
	reading(40, 9, "online", `{"power_status": "on"}`, true)
	if err := Backfill(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if d, ok := summary(3); !ok || d.samples != 1 || d.calls != 1<<9 || d.occupied != 1<<9 || !d.signal || d.peak == nil || *d.peak != 2 {
		t.Errorf("backfilled day = %+v %v", d, ok)
	}
	if d, ok := summary(40); !ok || d.display != 1<<9 || d.signal {
		t.Errorf("backfilled old day = %+v %v", d, ok)
	}

	// New readings merge into today's summary; offline ones count as
	// samples only.
	reading(0, 1, "online", `{"active_calls": 1, "people_count": 3}`, false)
	reading(0, 1, "offline", `{"active_calls": 1, "people_count": 9}`, false)
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	reading(0, 2, "online", `{"active_calls": 0, "people_count": 5, "power_status": "active"}`, false)
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	d, ok := summary(0)
	if !ok || d.samples != 3 || d.online != 2 || d.calls != 1<<1 || d.occupied != 1<<1|1<<2 ||
		d.display != 1<<2 || !d.signal || d.peak == nil || *d.peak != 5 {
		t.Errorf("today = %+v %v; want 3 samples, 2 online, call at 01, people at 01 and 02, display at 02, peak 5", d, ok)
	}
	// Rolling up again with nothing new changes nothing.
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	if again, _ := summary(0); again != d && again.samples != d.samples {
		t.Errorf("second rollup changed today: %+v → %+v", d, again)
	}

	// A reading that arrives late (a Collector replaying its buffer) is
	// counted on the day it was taken.
	reading(2, 14, "online", `{"active_calls": 2}`, false)
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	if d, ok := summary(2); !ok || d.calls != 1<<14 {
		t.Errorf("late reading = %+v %v", d, ok)
	}

	// Pruning: raw older than the retention period goes once summarised;
	// an old reading that arrived after the last rollup stays until rolled up.
	reading(35, 10, "online", `{}`, false) // arrived now, not yet rolled up
	exec(`INSERT INTO telemetry_daily (customer_id, device_id, day, samples) VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date - 401, 1)`, cust, dev)
	if err := Prune(ctx, conn, 30); err != nil {
		t.Fatal(err)
	}
	raw := func(daysAgo int) string {
		return q(`SELECT count(*)::text FROM telemetry WHERE device_id = $1
		          AND ts::date = (now() AT TIME ZONE 'UTC')::date - $2::int`, dev, daysAgo)
	}
	if n := raw(40); n != "0" {
		t.Errorf("40-day-old summarised reading not pruned (%s left)", n)
	}
	if n := raw(35); n != "1" {
		t.Errorf("unsummarised old reading = %s, want kept until rolled up", n)
	}
	if n := raw(3); n != "1" {
		t.Errorf("recent reading = %s, want kept", n)
	}
	if _, ok := summary(40); !ok {
		t.Error("40-day summary pruned; summaries are kept 400 days")
	}
	if _, ok := summary(401); ok {
		t.Error("401-day summary not pruned")
	}
	if err := Rollup(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, conn, 30); err != nil {
		t.Fatal(err)
	}
	if n := raw(35); n != "0" {
		t.Errorf("rolled-up old reading = %s, want pruned", n)
	}
	if d, ok := summary(35); !ok || d.samples != 1 {
		t.Errorf("pruned reading's summary = %+v %v, want it counted", d, ok)
	}
}
