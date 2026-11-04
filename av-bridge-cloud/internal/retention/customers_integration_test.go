package retention

// Integration test for the day-30 customer deletion. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16. The pass runs as
// app_admin, the role the cloud runs retention as, so a missing grant (or a
// table the app may not delete from, such as audit_log) fails here.

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// logBuffer collects log output for assertions.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestDeleteDueCustomers(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping customer deletion test")
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

	// seed gives a customer a hierarchy, a warm-standby Collector pair, a
	// device with telemetry, alerts, tickets, audit entries, users with
	// roles and sessions, and the tables app_admin has no DELETE on.
	// Returns the primary Collector.
	seed := func(cust string) string {
		reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'R') RETURNING id::text`, cust)
		loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'L') RETURNING id::text`, cust, reg)
		bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B') RETURNING id::text`, cust, loc)
		room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Rm') RETURNING id::text`, cust, bld)
		primary := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, building_id)
		              VALUES ($1::uuid, 'del-p-' || $1::text, 'p', '\x00', $2) RETURNING id::text`, cust, bld)
		q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, standby_for)
		   VALUES ($1::uuid, 'del-s-' || $1::text, 's', '\x00', $2) RETURNING id::text`, cust, primary)
		dev := q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol)
		          VALUES ($1, $2, $3, 'd', 'D', 'ping') RETURNING id::text`, cust, primary, room)
		// Readings and their daily summary on one UTC day (yesterday, well
		// inside raw retention), so a telemetry rollup run by a parallel
		// test merges into the existing summary row instead of adding one.
		for i := 0; i < 3; i++ {
			exec(`INSERT INTO telemetry (customer_id, device_id, ts, status)
			      VALUES ($1, $2, (((now() AT TIME ZONE 'UTC')::date - 1) + time '12:00') AT TIME ZONE 'UTC' + make_interval(mins => $3), 'online')`, cust, dev, i)
		}
		exec(`INSERT INTO telemetry_daily (customer_id, device_id, day, samples)
		      VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date - 1, 96)`, cust, dev)
		alert := q(`INSERT INTO alerts (customer_id, device_id, alert_key, severity, status, opened_at)
		            VALUES ($1, $2, 'offline', 'warning', 'open', now()) RETURNING id::text`, cust, dev)
		ch := q(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, 'm', 'email', 'ops@example.test') RETURNING id::text`, cust)
		exec(`INSERT INTO alert_tickets (customer_id, alert_id, channel_id) VALUES ($1, $2, $3)`, cust, alert, ch)
		exec(`INSERT INTO firmware_targets (customer_id, make, model) VALUES ($1, 'Cisco', 'Room Kit')`, cust)
		for i := 0; i < 2; i++ {
			exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind) VALUES ($1, 'a', 'x.y', 'device')`, cust)
		}
		role := q(`INSERT INTO roles (customer_id, name) VALUES ($1, 'admin') RETURNING id::text`, cust)
		for _, who := range []string{"a", "b"} {
			u := q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1::uuid, $2 || '-' || $1::text || '@example.test', 'x') RETURNING id::text`, cust, who)
			exec(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, u, role)
			exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`, u, "del-"+u)
		}
		return primary
	}
	// What seed puts in each customer_id table (plus the customers row).
	seeded := map[string]int64{
		"customers": 1, "regions": 1, "locations": 1, "buildings": 1, "rooms": 1,
		"collectors": 2, "devices": 1, "telemetry": 3, "telemetry_daily": 1,
		"alerts": 1, "notification_channels": 1, "alert_tickets": 1, "firmware_targets": 1,
		"audit_log": 2, "roles": 1, "users": 2,
	}

	offboard := func(name, deleteAfter string) string {
		c := q(`INSERT INTO customers (name, offboarding_started_at, offboarding_started_by, delete_after)
		        VALUES ($1, now() - interval '31 days', 'vendor@example.test', now() + $2::interval) RETURNING id::text`, name, deleteAfter)
		exec(`INSERT INTO customer_deletions (customer_id, customer_name, started_at, started_by)
		      VALUES ($1, $2, now() - interval '31 days', 'vendor@example.test')`, c, name)
		return c
	}
	x := offboard("deletion-x", "-1 hour")                                          // due
	y := offboard("deletion-y", "1 day")                                            // offboarding, not yet due
	z := q(`INSERT INTO customers (name) VALUES ('deletion-z') RETURNING id::text`) // active
	// Blocked: due, but another customer's Collector stands by for one of
	// its Collectors, so the RESTRICT link can't be cleared from its side.
	// Its deletion must fail on its own and not stop X's.
	blocked := offboard("deletion-blocked", "-2 hours")
	for _, c := range []string{x, y, z} {
		seed(c)
	}
	bPrimary := seed(blocked)
	q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, standby_for)
	               VALUES ($1::uuid, 'del-x-' || $1::text, 'xs', '\x00', $2) RETURNING id::text`, z, bPrimary)
	t.Cleanup(func() {
		left := []string{blocked, y, z}
		conn.Exec(ctx, `UPDATE collectors SET standby_for = NULL WHERE customer_id = ANY($1::uuid[])`, left)
		conn.Exec(ctx, `DELETE FROM customers WHERE id = ANY($1::uuid[])`, left)
		conn.Exec(ctx, `DELETE FROM customer_deletions WHERE customer_id = ANY($1::uuid[])`, left)
	})

	tables, err := db.CustomerIDTables(ctx, conn, "customer_deletions")
	if err != nil {
		t.Fatal(err)
	}
	countAll := func(c string) map[string]int64 {
		t.Helper()
		out := map[string]int64{"customers": 0}
		var n int64
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM customers WHERE id = $1`, c).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out["customers"] = n
		for _, tb := range tables {
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{tb}.Sanitize()+` WHERE customer_id = $1`, c).Scan(&n); err != nil {
				t.Fatalf("count %s: %v", tb, err)
			}
			out[tb] = n
		}
		return out
	}
	xBefore := countAll(x)
	for tb, n := range seeded {
		if xBefore[tb] != n {
			t.Fatalf("seed: %s has %d rows for X, want %d", tb, xBefore[tb], n)
		}
	}
	yBefore, zBefore, bBefore := countAll(y), countAll(z), countAll(blocked)

	// Run the pass as app_admin.
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("app_admin", "app_admin_dev")
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var logs logBuffer
	svc := New(pool, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := svc.Pass(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}

	// X: every row gone, from every customer_id table.
	for tb, n := range countAll(x) {
		if n != 0 {
			t.Errorf("X: %d rows left in %s", n, tb)
		}
	}
	if n := q(`SELECT count(*)::text FROM user_sessions s WHERE s.token_hash LIKE 'del-%' AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = s.user_id)`); n != "0" {
		t.Errorf("orphan sessions left: %s", n)
	}

	// X's certificate: deleted_at set, row counts as seeded (and zero
	// for every other table).
	var deleted bool
	var raw []byte
	if err := conn.QueryRow(ctx, `SELECT deleted_at IS NOT NULL, row_counts FROM customer_deletions WHERE customer_id = $1`, x).
		Scan(&deleted, &raw); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Error("X: certificate deleted_at not set")
	}
	var counts map[string]int64
	if err := json.Unmarshal(raw, &counts); err != nil {
		t.Fatalf("row_counts %s: %v", raw, err)
	}
	if len(counts) != len(xBefore) {
		t.Errorf("row_counts has %d tables, want %d: %s", len(counts), len(xBefore), raw)
	}
	for tb, want := range xBefore {
		if got, ok := counts[tb]; !ok || got != want {
			t.Errorf("row_counts[%s] = %d (present %v), want %d", tb, got, ok, want)
		}
	}
	if _, ok := counts["customer_deletions"]; ok {
		t.Error("row_counts includes customer_deletions, which is kept")
	}

	// Y (not due), Z (active) and the blocked customer: untouched.
	for name, c := range map[string]struct {
		id     string
		before map[string]int64
	}{"Y": {y, yBefore}, "Z": {z, zBefore}, "blocked": {blocked, bBefore}} {
		after := countAll(c.id)
		for tb, n := range c.before {
			if after[tb] != n {
				t.Errorf("%s: %s has %d rows, had %d", name, tb, after[tb], n)
			}
		}
		if n := q(`SELECT count(*)::text FROM customer_deletions WHERE customer_id = $1 AND (deleted_at IS NOT NULL OR row_counts IS NOT NULL)`, c.id); n != "0" {
			t.Errorf("%s: certificate written", name)
		}
	}
	// The blocked customer's standby link was rolled back with the rest.
	if n := q(`SELECT count(*)::text FROM collectors WHERE customer_id = $1 AND standby_for IS NOT NULL`, blocked); n != "1" {
		t.Errorf("blocked: standby links = %s, want 1 (rolled back)", n)
	}
	if out := logs.String(); !strings.Contains(out, "retention: customer delete failed") || !strings.Contains(out, blocked) {
		t.Errorf("no warning for the blocked customer; logs:\n%s", out)
	}
}
