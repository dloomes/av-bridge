package nightly

// Integration test: background loops skip a customer whose offboarding has
// started (db/offboarding.go). Drives one pass each of the Collector health
// watcher, the alert dispatcher, the nightly scheduler and the digest
// finder. Lives in package nightly because the scheduler's tick and clock
// are unexported. The passes are global, so it asserts only on its own
// customer's rows. Runs only when TEST_DATABASE_URL is set:
//
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55440/postgres?sslmode=disable \
//	  go test ./internal/nightly/ -run TestBackgroundSkipsOffboarding -v

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/collectorhealth"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

// recordingSenders records every send instead of delivering it.
type recordingSenders struct {
	mu   sync.Mutex
	sent []notify.AlertEvent
}

func (r *recordingSenders) Send(_ context.Context, _ notify.Channel, evt notify.AlertEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, evt)
	return nil
}

func (r *recordingSenders) sentFor(customerID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.sent {
		if e.CustomerID == customerID {
			n++
		}
	}
	return n
}

func TestBackgroundSkipsOffboarding(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping offboarding background test")
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

	// An offboarding customer with a due power schedule and readiness
	// check (04:43 UTC, a time no other test uses), a powerable device on a
	// stale Collector and an alert channel.
	cust := q(`INSERT INTO customers (name, offboarding_started_at, offboarding_started_by, delete_after)
	           VALUES ('offboarding-bg-test', now(), 'test', now() + interval '30 days') RETURNING id::text`)
	exec(`INSERT INTO nightly_schedule (customer_id, power_off_time, power_on_time, days_of_week, timezone, enabled)
	      VALUES ($1, '04:43', '07:30', ARRAY[1,2,3,4,5,6,7], 'UTC', true)`, cust)
	routine := q(`INSERT INTO nightly_test_routine (customer_id, name, steps)
	              VALUES ($1, 'Standard', '[{"type":"section","label":"Start"}]') RETURNING id::text`, cust)
	exec(`INSERT INTO readiness_schedule (customer_id, enabled, test_routine_id, run_time, days_of_week)
	      VALUES ($1, true, $2, '04:43', ARRAY[1,2,3,4,5,6,7])`, cust, routine)
	t.Cleanup(func() {
		su.Exec(context.Background(), `UPDATE nightly_schedule SET enabled = false WHERE customer_id = $1`, cust)
		su.Exec(context.Background(), `UPDATE readiness_schedule SET enabled = false WHERE customer_id = $1`, cust)
	})
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Boardroom') RETURNING id::text`, cust, b)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1::uuid, 'offb-bg-' || $1::uuid::text, 'c', '\x00', now() - interval '2 hours') RETURNING id::text`, cust)
	exec(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol, capabilities)
	      VALUES ($1, $2, $3, 'display-1', 'Display', 'x', '{"power":{"on":true,"off":true}}')`, cust, col, room)
	channel := q(`INSERT INTO notification_channels (customer_id, name, type, target, min_severity)
	              VALUES ($1, 'Ops', 'webhook', 'http://127.0.0.1:9/hook', 'info') RETURNING id::text`, cust)

	count := func(sql string) string { return q(sql, cust) }

	// Collector health: a stale Collector opens no alert.
	senders := &recordingSenders{}
	dispatcher := notify.NewDispatcher(pool, senders, quiet)
	collectorhealth.NewWatcher(pool, dispatcher, time.Minute, 5*time.Minute, quiet).Sweep(ctx)
	if n := count(`SELECT count(*)::text FROM alerts WHERE customer_id = $1`); n != "0" {
		t.Errorf("collector health opened %s alerts for an offboarding customer", n)
	}

	// Dispatcher: an alert event for the customer is not sent anywhere.
	dispatcher.Dispatch(notify.AlertEvent{AlertID: "x", CustomerID: cust, CollectorID: col,
		AlertKey: "collector_offline", Severity: "critical", Message: "test", OpenedAt: time.Now()})
	time.Sleep(500 * time.Millisecond) // Dispatch sends from a goroutine
	if n := senders.sentFor(cust); n != 0 {
		t.Errorf("dispatcher sent %d notifications for an offboarding customer", n)
	}
	if v := q(`SELECT (last_sent_at IS NOT NULL)::text FROM notification_channels WHERE id = $1`, channel); v != "false" {
		t.Error("dispatcher recorded a send on the offboarding customer's channel")
	}

	// Nightly scheduler: the due power cycle and readiness check start no
	// run and queue no command.
	clock := time.Date(2030, 1, 8, 4, 44, 0, 0, time.UTC)
	s := NewScheduler(pool, Config{TickInterval: time.Minute, GraceWindow: 30 * time.Minute, WarmupSeconds: 60}, quiet)
	s.now = func() time.Time { return clock }
	s.tick(ctx)
	if n := count(`SELECT count(*)::text FROM nightly_run WHERE customer_id = $1`); n != "0" {
		t.Errorf("scheduler created %s runs for an offboarding customer", n)
	}
	if n := count(`SELECT count(*)::text FROM commands WHERE customer_id = $1`); n != "0" {
		t.Errorf("scheduler queued %s commands for an offboarding customer", n)
	}
	if n := count(`SELECT count(*)::text FROM alerts WHERE customer_id = $1`); n != "0" {
		t.Errorf("scheduler opened %s alerts for an offboarding customer", n)
	}

	// Digest: not due for an offboarding customer.
	ds := NewDigestSender(pool, DigestConfig{}, quiet)
	ds.now = func() time.Time { return clock.Add(6 * time.Hour) }
	due, err := ds.findDueCustomers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range due {
		if c.customerID == cust {
			t.Error("digest is due for an offboarding customer")
		}
	}
}
