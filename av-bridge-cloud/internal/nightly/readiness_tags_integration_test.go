package nightly

// The scheduler's readiness rooms take a room tag's check (migration
// 0071): room settings first, then the winning tag's, then the default,
// field by field. Same TEST_DATABASE_URL harness as
// readiness_integration_test.go.

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

func TestReadinessRoomsByTag(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping readiness-by-tag scheduler test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('readiness-tag-sched') RETURNING id::text`)
	standard := q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, 'Standard') RETURNING id::text`, cust)
	court := q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, 'Court') RETURNING id::text`, cust)
	q(`INSERT INTO readiness_schedule (customer_id, enabled, test_routine_id, run_time, days_of_week)
	   VALUES ($1, true, $2, '07:45', ARRAY[1,2,3,4,5]) RETURNING customer_id::text`, cust, standard)
	t.Cleanup(func() {
		su.Exec(context.Background(), `UPDATE readiness_schedule SET enabled = false WHERE customer_id = $1`, cust)
	})
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	tagged, own, plain := room("Court 1"), room("Court 2"), room("Office")
	tag := q(`INSERT INTO room_tags (customer_id, name) VALUES ($1, 'Courtroom') RETURNING id::text`, cust)
	q(`INSERT INTO tag_readiness_config (customer_id, tag_id, test_routine_id, run_time, days_of_week)
	   VALUES ($1, $2, $3, '07:30', ARRAY[1,2,3,4,5,6]) RETURNING id::text`, cust, tag, court)
	for _, r := range []string{tagged, own} {
		q(`INSERT INTO room_tag_assignments (customer_id, room_id, tag_id) VALUES ($1, $2, $3) RETURNING room_id::text`, cust, r, tag)
	}
	// Court 2 sets its own days; its routine and time still come from the tag.
	q(`INSERT INTO room_readiness_config (customer_id, room_id, days_of_week) VALUES ($1, $2, ARRAY[7]) RETURNING id::text`, cust, own)

	s := NewScheduler(pool, Config{TickInterval: time.Minute, GraceWindow: 30 * time.Minute, WarmupSeconds: 60}, quiet)
	rooms, err := s.collectReadinessRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]readinessRoom{}
	for _, r := range rooms {
		if r.customerID == cust {
			got[r.roomID] = r
		}
	}
	for _, c := range []struct {
		room, routine string
		at            time.Duration
		days          int
	}{
		{tagged, court, 7*time.Hour + 30*time.Minute, 6},
		{own, court, 7*time.Hour + 30*time.Minute, 1},
		{plain, standard, 7*time.Hour + 45*time.Minute, 5},
	} {
		r, ok := got[c.room]
		if !ok {
			t.Errorf("room %s not scheduled", c.room)
			continue
		}
		if r.routineID != c.routine || r.runTime != c.at || len(r.daysOfWeek) != c.days {
			t.Errorf("%s: routine %s at %v on %v; want %s at %v on %d days", r.roomName, r.routineID, r.runTime, r.daysOfWeek, c.routine, c.at, c.days)
		}
	}
}
