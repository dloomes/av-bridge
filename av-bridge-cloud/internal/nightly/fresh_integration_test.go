package nightly

// Integration test for fresh readings in Room Readiness checks, against a
// real Postgres, with a stand-in Collector answering _poll commands. Same
// TEST_DATABASE_URL harness as scheduler_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestChecksUseFreshReadings(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping fresh-reading integration test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('fresh-test') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Boardroom') RETURNING id::text`, cust, b)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'fresh-' || $1::uuid::text, 'c', '\x00') RETURNING id::text`, cust)
	device := func(name, typ, status string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, type, protocol, latest_status)
		          VALUES ($1, $2, $3, $4, $4, $5, 'x', $6) RETURNING id::text`, cust, col, room, name, typ, status)
	}
	// The display's last report is from before power-on: offline.
	display := device("Display", "display", "offline")
	dsp := device("DSP", "audio", "online")
	run := RunContext{RunID: "00000000-0000-0000-0000-00000000f001", CustomerID: cust, RoomID: room, RoomName: "Boardroom"}

	// Stand-in Collector: answers _poll for the display and DSP with a
	// fresh reading; answers like an old Collector ("unknown command")
	// for anything listed in old.
	var mu sync.Mutex // guards old and fresh
	old := map[string]bool{}
	fresh := map[string]string{
		display: `{"raw":"polled","parsed":{"status":"online","metrics":{"power":"on"}}}`,
		dsp:     `{"raw":"polled","parsed":{"status":"online","metrics":{"input_level_dbfs":-22.5}}}`,
	}
	// Its own connection: a pgx.Conn can't be shared between goroutines.
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
			mu.Lock()
			for dev, result := range fresh {
				if old[dev] {
					collector.Exec(context.Background(), `UPDATE commands SET status = 'failed', error = 'unknown command: _poll'
					   WHERE device_id = $1 AND name = '_poll' AND status = 'pending'`, dev)
					continue
				}
				collector.Exec(context.Background(), `UPDATE commands SET status = 'succeeded', result = $2::jsonb
				   WHERE device_id = $1 AND name = '_poll' AND status = 'pending'`, dev, result)
			}
			mu.Unlock()
		}
	}()

	e := NewExecutor(pool, ExecutorConfig{Enabled: true, DryRun: true}, quiet, ctx)
	freshReadingTick = 50 * time.Millisecond
	mkStep := func(v map[string]any) step {
		raw, _ := json.Marshal(v)
		var s step
		json.Unmarshal(raw, &s)
		s.Raw = raw
		return s
	}
	expectOnline := mkStep(map[string]any{"type": "expect_status", "target": map[string]any{"device_type": "display"}, "status": "online"})
	micsHear := mkStep(map[string]any{"type": "check_metric", "target": map[string]any{"device_type": "audio"},
		"metric": "input_level_dbfs", "operator": "gt", "threshold": -40})

	// Fresh readings win over the stale last report, even in dry-run.
	if r := e.stepExpectStatus(ctx, run, expectOnline); !r.Passed {
		t.Errorf("expect_status with a fresh reading: %s", r.Error)
	}
	// The DSP has no telemetry stored at all; the fresh reading is enough.
	if r := e.stepCheckMetric(ctx, run, micsHear); !r.Passed {
		t.Errorf("check_metric with a fresh reading: %s", r.Error)
	}

	// An old Collector answers "unknown command": the step falls back to
	// the last report, and says so when it fails.
	mu.Lock()
	old[display] = true
	mu.Unlock()
	if r := e.stepExpectStatus(ctx, run, expectOnline); r.Passed || !strings.Contains(r.Error, "last report") {
		t.Errorf("fallback: passed=%v error=%q", r.Passed, r.Error)
	}
	exec(`UPDATE devices SET latest_status = 'online' WHERE id = $1`, display)
	if r := e.stepExpectStatus(ctx, run, expectOnline); !r.Passed {
		t.Errorf("fallback with an online last report: %s", r.Error)
	}

	// A Collector that never answers: the step waits only up to the
	// fresh-reading timeout, then uses the last report.
	mu.Lock()
	delete(fresh, display)
	mu.Unlock()
	freshReadingTimeout = 500 * time.Millisecond
	start := time.Now()
	r := e.stepExpectStatus(ctx, run, expectOnline)
	if !r.Passed || time.Since(start) > 3*time.Second {
		t.Errorf("unanswered polls: passed=%v after %s (%s)", r.Passed, time.Since(start), r.Error)
	}

	// PDUs aren't polled on demand (a full PDU poll is dozens of Telnet
	// logins): checks use their regular readings, which may be minutes old.
	pdu := q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, type, protocol, latest_status)
	          VALUES ($1, $2, $3, 'PDU', 'PDU', 'power', 'aten_pdu', 'online') RETURNING id::text`, cust, col, room)
	exec(`INSERT INTO telemetry (customer_id, device_id, status, metrics, ts)
	      VALUES ($1, $2, 'online', '{"total_power_w": 240}', now() - interval '5 minutes')`, cust, pdu)
	pduOnline := mkStep(map[string]any{"type": "expect_status", "target": map[string]any{"device_id": pdu}, "status": "online"})
	pduDraw := mkStep(map[string]any{"type": "check_metric", "target": map[string]any{"device_id": pdu},
		"metric": "total_power_w", "operator": "gt", "threshold": 100})
	start = time.Now()
	if r := e.stepExpectStatus(ctx, run, pduOnline); !r.Passed {
		t.Errorf("PDU expect_status from its last report: %s", r.Error)
	}
	if r := e.stepCheckMetric(ctx, run, pduDraw); !r.Passed {
		t.Errorf("PDU check_metric from a 5-minute-old reading: %s", r.Error)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("PDU checks took %s; they shouldn't wait for a poll", took)
	}
	if n := q(`SELECT count(*)::text FROM commands WHERE device_id = $1 AND name = '_poll'`, pdu); n != "0" {
		t.Errorf("PDU was polled on demand %s times, want 0", n)
	}
}
