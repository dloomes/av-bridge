package ingest

// Integration test: offline alerts are held back for devices Room Readiness
// switched off overnight. Same TEST_DATABASE_URL harness as
// resilience_integration_test.go.

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestOfflineAlertsHeldOvernight(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping overnight alert integration test")
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
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

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

	cust := q(`INSERT INTO customers (name) VALUES ('overnight-alerts') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Boardroom') RETURNING id::text`, cust, b)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'oa-' || $1::uuid::text, 'c', '\x00') RETURNING id::text`, cust)
	device := func(name string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol)
		          VALUES ($1, $2, $3, $4, $4, 'x') RETURNING id::text`, cust, col, room, name)
	}
	display := device("Display")
	pdu := device("PDU")         // no power control: never sent power_off
	refused := device("Monitor") // power_off failed: it wasn't switched off
	// The scheduler marks the devices it switched off (the display); the
	// PDU has no power control and the monitor's power-off failed, so
	// neither is marked.
	run := q(`INSERT INTO nightly_run (customer_id, room_id, scheduled_at, phase, status, started_at)
	          VALUES ($1, $2, now(), 'off', 'in_progress', now()) RETURNING id::text`, cust, room)
	exec(`UPDATE devices SET switched_off_by_run = $2 WHERE id = $1`, display, run)

	// fire sends a device_offline alert event as the Collector would, and
	// reports whether it opened an alert (and so notified anyone).
	fire := func(dev string) bool {
		t.Helper()
		var out alertOutcome
		if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			var e error
			out, e = handleAlertEvent(ctx, tx, cust, dev, eventDTO{
				EventType: "alert:device_offline", Timestamp: time.Now(),
				Payload: map[string]any{"severity": "critical", "message": "offline"},
			})
			return e
		}); err != nil {
			t.Fatalf("handleAlertEvent: %v", err)
		}
		open := q(`SELECT count(*)::text FROM alerts WHERE device_id = $1 AND status = 'open'`, dev)
		if (out.notifyEvent != nil) != (open != "0") {
			t.Fatalf("notify outcome %v but %s open alerts", out.notifyEvent != nil, open)
		}
		return out.notifyEvent != nil
	}

	// While it's marked switched off, no alert.
	if fire(display) {
		t.Fatal("alert opened for a display Room Readiness switched off")
	}
	if !fire(pdu) {
		t.Error("a device that wasn't switched off should still alert overnight")
	}
	if !fire(refused) {
		t.Error("a device whose power-off failed should still alert overnight")
	}

	// Once the warm-up is over the scheduler clears the mark, and the
	// display alerts if it's still offline.
	exec(`UPDATE devices SET switched_off_by_run = NULL WHERE id = $1`, display)
	if !fire(display) {
		t.Error("after the warm-up an offline display should alert")
	}
}
