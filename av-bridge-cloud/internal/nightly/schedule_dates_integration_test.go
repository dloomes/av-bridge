package nightly

// Integration test for one-off schedule dates against a real Postgres.
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16 (see
// scheduler_integration_test.go for the docker line).

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestScheduleDates(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping schedule dates test")
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

	// Weekdays, 19:00 off and 07:30 on. Dry run: power-off settles at once.
	cust := q(`INSERT INTO customers (name) VALUES ('schedule-dates-test') RETURNING id::text`)
	exec(`INSERT INTO nightly_schedule (customer_id, power_off_time, power_on_time, days_of_week, timezone, enabled)
	      VALUES ($1, '19:00', '07:30', ARRAY[1,2,3,4,5], 'UTC', true)`, cust)
	t.Cleanup(func() {
		su.Exec(context.Background(), `UPDATE nightly_schedule SET enabled = false WHERE customer_id = $1`, cust)
	})
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Crown Court') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	court1, court2 := room("Court 1"), room("Court 2")
	date := func(roomID, day string, closed bool, on, off string) {
		exec(`INSERT INTO schedule_date (customer_id, room_id, on_date, closed, power_on_time, power_off_time)
		      VALUES ($1, NULLIF($2, '')::uuid, $3::date, $4, NULLIF($5, '')::time, NULLIF($6, '')::time)`,
			cust, roomID, day, closed, on, off)
	}
	date(court2, "2030-01-07", true, "", "")        // Mon: Court 2 closed
	date(court1, "2030-01-09", false, "", "22:00")  // Wed: Court 1 sits late
	date("", "2030-01-12", false, "08:00", "17:00") // Sat: the court sits
	date("", "2030-01-14", true, "", "")            // Mon: closed everywhere

	clock := time.Date(2030, 1, 7, 19, 1, 0, 0, time.UTC)
	s := NewScheduler(pool, Config{TickInterval: time.Minute, GraceWindow: 30 * time.Minute, WarmupSeconds: 60, DryRun: true}, quiet)
	s.now = func() time.Time { return clock }
	at := func(when string) {
		t.Helper()
		c, err := time.Parse("2006-01-02 15:04", when)
		if err != nil {
			t.Fatal(err)
		}
		clock = c
		s.tick(ctx)
	}
	// Power runs for a room, by when they switch off.
	runs := func(roomID string) []string {
		t.Helper()
		rows, err := su.Query(ctx, `SELECT to_char(scheduled_at AT TIME ZONE 'UTC', 'Dy DD HH24:MI')
		                              FROM nightly_run WHERE room_id = $1 AND kind = 'power' ORDER BY scheduled_at`, roomID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := func(label, roomID string, expect ...string) {
		t.Helper()
		got := runs(roomID)
		if len(got) != len(expect) {
			t.Fatalf("%s: runs %v, want %v", label, got, expect)
		}
		for i := range got {
			if got[i] != expect[i] {
				t.Fatalf("%s: runs %v, want %v", label, got, expect)
			}
		}
	}

	// Monday 19:01: Court 1 switches off; Court 2 is closed today, so it
	// doesn't (nothing to switch off, nothing to bring back).
	at("2030-01-07 19:01")
	at("2030-01-07 19:02")
	want("closed Monday", court2)
	want("Monday", court1, "Mon 07 19:00")

	// Wednesday: Court 1 sits until 22:00, so 19:00 passes it by.
	at("2030-01-09 19:01")
	if got := runs(court1); len(got) != 1 {
		t.Fatalf("Court 1 switched off at 19:00 on its late day: %v", got)
	}
	at("2030-01-09 22:01")
	if got := runs(court1); len(got) != 2 || got[1] != "Wed 09 22:00" {
		t.Fatalf("late sitting: %v", got)
	}

	// Friday night's switch-off comes back on for Saturday's sitting at
	// 08:00, and Saturday switches off at its own 17:00.
	at("2030-01-11 19:01")
	at("2030-01-11 19:02")
	at("2030-01-12 08:01")
	if got := q(`SELECT phase FROM nightly_run WHERE room_id = $1 AND scheduled_at = '2030-01-11T19:00:00Z'`, court2); got == "off" {
		t.Fatalf("Friday's run still off at 08:01 on the Saturday sitting")
	}
	for _, m := range []string{"08:02", "08:03", "08:05", "08:10"} {
		at("2030-01-12 " + m) // warm-up runs its course
	}
	if got := q(`SELECT status FROM nightly_run WHERE room_id = $1 AND scheduled_at = '2030-01-11T19:00:00Z'`, court2); got != "succeeded" {
		t.Fatalf("Friday's run on the Saturday morning: %s", got)
	}
	at("2030-01-12 17:01")
	if got := runs(court2); got[len(got)-1] != "Sat 12 17:00" {
		t.Fatalf("Saturday sitting: %v", got)
	}

	// Saturday's switch-off skips the closed Monday: still off at 07:31
	// Monday, back on Tuesday.
	at("2030-01-12 17:02")
	at("2030-01-14 07:31")
	sat := `SELECT phase FROM nightly_run WHERE room_id = $1 AND scheduled_at = '2030-01-12T17:00:00Z'`
	if got := q(sat, court2); got != "off" {
		t.Fatalf("Saturday's run on the closed Monday: %s, want off", got)
	}
	at("2030-01-14 19:01")
	if got := runs(court2); got[len(got)-1] != "Sat 12 17:00" {
		t.Fatalf("a run was created on the closed Monday: %v", got)
	}
	at("2030-01-15 07:31")
	if got := q(sat, court2); got == "off" {
		t.Fatalf("Saturday's run still off on Tuesday morning")
	}

	// The portal's view (app_tenant, row security): an organisation sees
	// its own dates; a user limited to another building sees only the
	// every-room ones.
	as := func(user, pass string) string {
		v, _ := url.Parse(dsn)
		v.User = url.UserPassword(user, pass)
		return v.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Annexe') RETURNING id::text`, cust, loc)
	visible := func(scope db.Scope) string {
		var n string
		if err := st.WithTenantScope(ctx, cust, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*)::text FROM schedule_date`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(db.Scope{}); n != "4" {
		t.Errorf("organisation sees %s dates, want 4", n)
	}
	if n := visible(db.Scope{Buildings: []string{other}}); n != "2" {
		t.Errorf("user limited to another building sees %s dates, want the 2 for every room", n)
	}
	stranger := q(`INSERT INTO customers (name) VALUES ('schedule-dates-other') RETURNING id::text`)
	if err := st.WithTenantScope(ctx, stranger, db.Scope{}, func(tx pgx.Tx) error {
		var n string
		if err := tx.QueryRow(ctx, `SELECT count(*)::text FROM schedule_date`).Scan(&n); err != nil {
			return err
		}
		if n != "0" {
			t.Errorf("another organisation sees %s dates", n)
		}
		_, err := tx.Exec(ctx, `INSERT INTO schedule_date (customer_id, on_date, closed) VALUES ($1, '2030-02-01', true)`, stranger)
		return err
	}); err != nil {
		t.Fatalf("tenant can't add its own date: %v", err)
	}
}
