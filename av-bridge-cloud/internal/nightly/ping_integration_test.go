package nightly

// Integration test for the Ping step, against a real Postgres, with a
// stand-in Collector answering _ping commands. Same TEST_DATABASE_URL
// harness as fresh_integration_test.go.

import (
	"context"
	"encoding/json"
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

func TestPingStep(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping ping step integration test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('ping-test') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Boardroom') RETURNING id::text`, cust, b)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'ping-' || $1::uuid::text, 'c', '\x00') RETURNING id::text`, cust)
	device := func(name, typ, protocol string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, type, protocol, latest_status)
		          VALUES ($1, $2, $3, $4, $4, $5, $6, 'online') RETURNING id::text`, cust, col, room, name, typ, protocol)
	}
	display := device("Display", "display", "sony_bravia")
	codec := device("Codec", "conferencing", "cisco_roomos")
	device("Matrix", "control", "serial") // no network address: left out of a room ping
	oldCol := device("Old", "audio", "tesira")

	// Stand-in Collector: the display replies, the codec doesn't, and Old
	// is on a Collector that predates _ping (its adapter refuses it).
	answers := map[string]string{
		display: `{"raw":"ok","parsed":{"host":"10.0.0.5","packets_sent":3,"packets_recv":3,"packet_loss_pct":0,"avg_ms":4}}`,
		codec:   `{"raw":"none","parsed":{"host":"10.0.0.6","packets_sent":3,"packets_recv":0,"packet_loss_pct":100}}`,
	}
	collector, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	defer func() { close(stop); <-done }()
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			for dev, result := range answers {
				collector.Exec(context.Background(), `UPDATE commands SET status = 'succeeded', result = $2::jsonb, completed_at = now()
				   WHERE device_id = $1 AND name = '_ping' AND status = 'pending'`, dev, result)
			}
			collector.Exec(context.Background(), `UPDATE commands SET status = 'failed', error = 'tesira: unsupported command "_ping"', completed_at = now()
			   WHERE device_id = $1 AND name = '_ping' AND status = 'pending'`, oldCol)
		}
	}()

	e := NewExecutor(pool, ExecutorConfig{Enabled: true, DryRun: true}, quiet, ctx)
	pingTick = 50 * time.Millisecond
	run := RunContext{RunID: "00000000-0000-0000-0000-00000000f002", CustomerID: cust, RoomID: room, RoomName: "Boardroom"}
	mkStep := func(v map[string]any) step {
		raw, _ := json.Marshal(v)
		var s step
		json.Unmarshal(raw, &s)
		s.Raw = raw
		return s
	}

	// One device that replies passes, in dry-run too, with a fresh reading.
	r := e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "ping", "target": map[string]any{"device_id": display}}))
	if !r.Passed || r.DeviceID != display {
		t.Errorf("display ping: passed=%v device=%s error=%q", r.Passed, r.DeviceID, r.Error)
	}
	if len(r.Readings) != 1 || r.Readings[0].Source != "fresh" {
		t.Errorf("display readings: %+v", r.Readings)
	}

	// A response-time limit it misses fails it.
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "ping", "target": map[string]any{"device_id": display}, "max_response_ms": 2}))
	if r.Passed || !strings.Contains(r.Error, "4 ms response") {
		t.Errorf("response limit: passed=%v error=%q", r.Passed, r.Error)
	}

	// The whole room: the serial matrix is left out; the codec's silence
	// and the old Collector each fail it, and say why.
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "ping", "target": map[string]any{"scope": "room"}}))
	actual, _ := r.Actual.(map[string]any)
	if r.Passed || len(actual) != 3 || actual["Matrix"] != nil {
		t.Errorf("room ping: passed=%v actual=%v", r.Passed, actual)
	}
	for _, want := range []string{"Codec (10.0.0.6): no reply to 3 pings", "Old: the Collector needs updating"} {
		if !strings.Contains(r.Error, want) {
			t.Errorf("room ping error %q, missing %q", r.Error, want)
		}
	}
	if old, _ := actual["Old"].(map[string]any); old["error"] != "the Collector needs updating to run Ping steps" {
		t.Errorf("old Collector actual: %v", actual["Old"])
	}
	if strings.Contains(r.Error, "Display") {
		t.Errorf("room ping blamed the display: %q", r.Error)
	}

	// A type with nothing pingable in it is a failed step, not a pass.
	r = e.dispatchStep(ctx, run, mkStep(map[string]any{"type": "ping", "target": map[string]any{"device_type": "control"}}))
	if r.Passed || !strings.Contains(r.Error, "no devices with a network address") {
		t.Errorf("serial-only target: passed=%v error=%q", r.Passed, r.Error)
	}
}
