package nightly

// Integration test for room alerts from finished runs. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16 (see
// portalapi/device_move_integration_test.go for the docker line).

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestRunAlerts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping room alert test")
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
	pool, err := pgxpool.New(ctx, as("app_admin", "app_admin_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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

	cust := q(`INSERT INTO customers (name) VALUES ('room-alerts') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Crown Court') RETURNING id::text`, cust, loc)
	other := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Annexe') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Court 3') RETURNING id::text`, cust, bld)

	run := func(kind, status, reason string, failedSteps ...string) string {
		t.Helper()
		id := q(`INSERT INTO nightly_run (customer_id, room_id, kind, phase, status, scheduled_at, completed_at, failure_reason)
		         VALUES ($1, $2, $3, $4, $5, now(), now(), NULLIF($6, '')) RETURNING id::text`,
			cust, room, kind, map[string]string{"failed": "failed", "succeeded": "ready"}[status], status, reason)
		for i, s := range failedSteps {
			name, e, _ := strings.Cut(s, "|")
			q(`INSERT INTO nightly_step_result (customer_id, run_id, step_index, step_name, step_type, passed, error, started_at)
			   VALUES ($1, $2, $3, $4, 'check_metric', false, NULLIF($5, ''), now()) RETURNING run_id::text`,
				cust, id, i+1, name, e)
		}
		return id
	}
	s := NewScheduler(pool, Config{}, quiet)
	sync := func() {
		t.Helper()
		if err := s.syncRunAlerts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	alerts := func(key string) string {
		return q(`SELECT COALESCE(string_agg(status || ':' || message, ' | ' ORDER BY opened_at), 'none')
		            FROM alerts WHERE room_id = $1 AND alert_key = $2`, room, key)
	}

	// A failed health check opens one room alert, naming the failed steps.
	run("readiness", "failed", "", "Judge mic level|-62 dB, want > -40", "Codec online|offline")
	sync()
	if got := alerts(alertKeyReadinessFailed); got !=
		"open:Room Readiness checks failed in Court 3: Judge mic level (-62 dB, want > -40); Codec online (offline)" {
		t.Errorf("after a failed check: %s", got)
	}

	// Failing again, even once acknowledged, updates that alert, not a new one.
	q(`UPDATE alerts SET status = 'acknowledged' WHERE room_id = $1 RETURNING id::text`, room)
	run("readiness", "failed", "", "Codec online|offline")
	sync()
	if got := alerts(alertKeyReadinessFailed); got != "acknowledged:Room Readiness checks failed in Court 3: Codec online (offline)" {
		t.Errorf("after a repeat failure: %s", got)
	}

	// A passing check resolves it; a power cycle failure is its own alert.
	run("readiness", "succeeded", "")
	run("power", "failed", "missed power-on window")
	sync()
	if got := alerts(alertKeyReadinessFailed); !strings.HasPrefix(got, "resolved:") {
		t.Errorf("after a passing check: %s", got)
	}
	if got := alerts(alertKeyPowerFailed); got != "open:Power cycle failed in Court 3: missed power-on window" {
		t.Errorf("power cycle failure: %s", got)
	}
	if n := q(`SELECT count(*)::text FROM nightly_run WHERE room_id = $1 AND alert_checked_at IS NULL`, room); n != "0" {
		t.Errorf("%s runs left unreflected", n)
	}

	// A site-limited user sees the room's alerts only if the room is in scope.
	count := func(scope db.Scope) string {
		var n string
		if err := st.WithTenantScope(ctx, cust, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*)::text FROM alerts WHERE room_id = $1`, room).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(db.Scope{Buildings: []string{bld}}); n != "2" {
		t.Errorf("in-scope user sees %s room alerts, want 2", n)
	}
	if n := count(db.Scope{Buildings: []string{other}}); n != "0" {
		t.Errorf("out-of-scope user sees %s room alerts, want 0", n)
	}
}
