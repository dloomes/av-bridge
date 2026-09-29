package notify

// Integration test for the ServiceNow ticket lifecycle (migration 0048).
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16, e.g.
//
//	docker run -d --name snowtest -e POSTGRES_PASSWORD=pgtest -p 55433:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55433/postgres?sslmode=disable \
//	  go test ./internal/notify/ -run TestTicketLifecycle -v

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestTicketLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping ticket lifecycle integration test")
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

	f := newFakeServiceNow(t)
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := su.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	cust := q(`INSERT INTO customers (name) VALUES ('snow-test') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'col', '\x00'::bytea) RETURNING id::text`, cust)
	dev := q(`INSERT INTO devices (customer_id, collector_id, name, reported_id) VALUES ($1, $2, 'Court 3 Display', 'd3') RETURNING id::text`, cust, col)
	cfg, _ := json.Marshal(testChannel(f, map[string]any{"assignment_group": "AV Support"}).Config)
	chID := q(`INSERT INTO notification_channels (customer_id, name, type, target, config, min_severity)
	           VALUES ($1, 'SNOW', 'servicenow', $2, $3::jsonb, 'warning') RETURNING id::text`, cust, f.srv.URL, string(cfg))
	newAlert := func(key string) string {
		return q(`INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, status)
		          VALUES ($1, $2, $3, 'critical', 'Device has stopped responding', 'open') RETURNING id::text`, cust, dev, key)
	}

	d := NewDispatcher(st.AdminPool(), nil, quiet)
	d.SetServiceNow(testSN(f))
	channels, err := d.listChannels(ctx, cust, "critical")
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels %v %v", channels, err)
	}
	ch := channels[0]
	ticket := func(alertID string) (state, extID string, attempts int) {
		t.Helper()
		if err := su.QueryRow(ctx, `SELECT state, COALESCE(external_id,''), attempts FROM alert_tickets
		                             WHERE alert_id = $1 AND channel_id = $2`, alertID, chID).Scan(&state, &extID, &attempts); err != nil {
			t.Fatalf("ticket for %s: %v", alertID, err)
		}
		return
	}

	// 1. First open fails (ServiceNow unavailable): the ticket is kept as pending.
	a1 := newAlert("device_offline")
	evt := testEvent()
	evt.AlertID, evt.CustomerID, evt.DeviceID = a1, cust, dev
	f.failNext = 1
	if err := d.openTicket(ctx, ch, evt); err == nil {
		t.Fatal("expected the first open to fail")
	}
	if s, _, n := ticket(a1); s != "pending" || n != 1 {
		t.Fatalf("after failure: %s attempts=%d", s, n)
	}

	// 2. The sync retries and opens it.
	d.SyncTickets(ctx)
	if s, ext, n := ticket(a1); s != "open" || ext != "sys1" || n != 2 {
		t.Fatalf("after retry: %s %s attempts=%d", s, ext, n)
	}

	// 3. A repeat dispatch for the same alert doesn't raise a second incident.
	if err := d.openTicket(ctx, ch, evt); err != nil {
		t.Fatal(err)
	}
	if len(f.incidents) != 1 {
		t.Fatalf("%d incidents, want 1", len(f.incidents))
	}

	// 4. While the alert is open, the sync leaves the incident alone.
	d.SyncTickets(ctx)
	if s, _, _ := ticket(a1); s != "open" {
		t.Fatalf("open alert: ticket %s", s)
	}

	// 5. Alert resolves → incident resolved with a note.
	if _, err := su.Exec(ctx, `UPDATE alerts SET status='resolved', resolved_at=now(), resolved_by='auto:recovered' WHERE id=$1`, a1); err != nil {
		t.Fatal(err)
	}
	d.SyncTickets(ctx)
	if s, _, _ := ticket(a1); s != "resolved" {
		t.Fatalf("after resolve: %s", s)
	}
	if inc := f.incidents["sys1"]; inc["state"] != "6" || inc["close_notes"] != resolveNote("auto:recovered") {
		t.Errorf("incident after resolve: %v", inc)
	}

	// 6. An alert that clears before any incident could be raised is skipped.
	a2 := newAlert("device_degraded")
	evt2 := evt
	evt2.AlertID, evt2.AlertKey = a2, "device_degraded"
	f.failNext = 1
	_ = d.openTicket(ctx, ch, evt2)
	if _, err := su.Exec(ctx, `UPDATE alerts SET status='resolved', resolved_at=now(), resolved_by='operator' WHERE id=$1`, a2); err != nil {
		t.Fatal(err)
	}
	d.SyncTickets(ctx)
	if s, ext, _ := ticket(a2); s != "skipped" || ext != "" || len(f.incidents) != 1 {
		t.Errorf("cleared-before-open: %s %q, %d incidents", s, ext, len(f.incidents))
	}

	// 7. Tenants can read their own tickets and nobody else's.
	count := func(customer string) int {
		t.Helper()
		var n int
		if err := st.WithTenant(ctx, customer, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM alert_tickets`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(cust); n != 2 {
		t.Errorf("own tenant sees %d tickets, want 2", n)
	}
	if n := count("00000000-0000-0000-0000-000000000000"); n != 0 {
		t.Errorf("other tenant sees %d tickets, want 0", n)
	}
}
