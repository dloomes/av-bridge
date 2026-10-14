package portalapi

// Integration test for per-room test routines (nightly_rooms.go) and the
// run-history CSV export (nightly_runs_export.go). Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/portalapi/ -run TestNightly -v

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestNightlyRoomRoutineAndExport(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping nightly integration test")
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

	room := func(cust, name string) string {
		reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
		loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
		b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	routine := func(cust, name string) string {
		return q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, $2) RETURNING id::text`, cust, name)
	}

	cust := q(`INSERT INTO customers (name) VALUES ('nightly-test') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('nightly-other') RETURNING id::text`)
	roomA := room(cust, "Boardroom")
	roomB := room(cust, "=Huddle") // formula-looking name, for the CSV check
	otherRoom := room(other, "Elsewhere")
	defRoutine := routine(cust, "Standard check")
	vcRoutine := routine(cust, "VC room check")
	otherRoutine := routine(other, "Not yours")
	exec(`INSERT INTO nightly_schedule (customer_id, test_routine_id) VALUES ($1, $2)`, cust, defRoutine)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	principal := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000b1", CustomerID: cust, Permissions: all}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms", h.ListNightlyRooms)
	mux.HandleFunc("PATCH /rooms/{id}", h.UpdateRoomOverride)
	mux.HandleFunc("GET /runs/export.csv", h.ExportNightlyRuns)
	mux.HandleFunc("PATCH /schedule", h.UpdateNightlySchedule)
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), principal))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	effective := func() map[string]roomOverrideRow {
		t.Helper()
		code, body := call("GET", "/rooms", nil)
		if code != http.StatusOK {
			t.Fatalf("list rooms: %d %s", code, body)
		}
		var rows []roomOverrideRow
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]roomOverrideRow{}
		for _, r := range rows {
			out[r.RoomID] = r
		}
		return out
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	// What the scheduler will actually run for a room (scheduler.go).
	schedulerRoutine := func(roomID string) string {
		return q(`SELECT COALESCE(COALESCE(rnc.test_routine_id, ns.test_routine_id)::text, '')
		            FROM rooms r
		            LEFT JOIN room_nightly_config rnc ON rnc.room_id = r.id
		            LEFT JOIN nightly_schedule ns ON ns.customer_id = r.customer_id
		           WHERE r.id = $1`, roomID)
	}

	// Both rooms start on the customer default.
	rows := effective()
	if _, ok := rows[otherRoom]; ok {
		t.Error("another customer's room is listed")
	}
	if got := str(rows[roomA].EffectiveRoutineID); got != defRoutine || str(rows[roomA].EffectiveRoutineName) != "Standard check" {
		t.Errorf("room A effective routine = %q, want the default", got)
	}

	// Give room A its own routine.
	if code, body := call("PATCH", "/rooms/"+roomA, map[string]any{"test_routine_id": vcRoutine}); code != http.StatusNoContent {
		t.Fatalf("set room routine: %d %s", code, body)
	}
	rows = effective()
	if str(rows[roomA].OverrideRoutineID) != vcRoutine || str(rows[roomA].EffectiveRoutineName) != "VC room check" || !rows[roomA].HasOverride {
		t.Errorf("room A after override: %+v", rows[roomA])
	}
	if str(rows[roomB].EffectiveRoutineID) != defRoutine || rows[roomB].OverrideRoutineID != nil {
		t.Errorf("room B should still inherit the default: %+v", rows[roomB])
	}
	if schedulerRoutine(roomA) != vcRoutine || schedulerRoutine(roomB) != defRoutine {
		t.Error("scheduler would not run the per-room routine")
	}

	// Another customer's routine, or nonsense, is refused.
	for _, bad := range []string{otherRoutine, "not-a-uuid"} {
		if code, body := call("PATCH", "/rooms/"+roomA, map[string]any{"test_routine_id": bad}); code != http.StatusBadRequest {
			t.Errorf("routine %q: %d %s, want 400", bad, code, body)
		}
	}
	if schedulerRoutine(roomA) != vcRoutine {
		t.Error("a refused change altered the room's routine")
	}
	if code, body := call("PATCH", "/schedule", map[string]any{"test_routine_id": otherRoutine}); code != http.StatusBadRequest {
		t.Errorf("default routine from another customer: %d %s, want 400", code, body)
	}
	if schedulerRoutine(roomB) != defRoutine {
		t.Error("a refused default change altered the default routine")
	}

	// Other fields don't disturb the routine, and null reverts it.
	if code, body := call("PATCH", "/rooms/"+roomA, map[string]any{"power_off_time": "20:00"}); code != http.StatusNoContent {
		t.Fatalf("set time: %d %s", code, body)
	}
	if schedulerRoutine(roomA) != vcRoutine {
		t.Error("changing the time cleared the routine")
	}
	if code, body := call("PATCH", "/rooms/"+roomA, map[string]any{"test_routine_id": nil}); code != http.StatusNoContent {
		t.Fatalf("clear routine: %d %s", code, body)
	}
	if schedulerRoutine(roomA) != defRoutine {
		t.Error("null should revert the room to the default routine")
	}

	// Routine set on a room with no override row yet (insert path).
	if code, body := call("PATCH", "/rooms/"+roomB, map[string]any{"test_routine_id": vcRoutine}); code != http.StatusNoContent {
		t.Fatalf("set room B routine: %d %s", code, body)
	}
	if schedulerRoutine(roomB) != vcRoutine {
		t.Error("insert path didn't store the routine")
	}

	// ── CSV export ────────────────────────────────────────────────────────
	run := func(cust, roomID, routineID, status string) string {
		return q(`INSERT INTO nightly_run (customer_id, room_id, routine_id, phase, status, scheduled_at, started_at, completed_at)
		          VALUES ($1, $2, NULLIF($3, '')::uuid, 'ready', $4, now() - interval '1 hour', now() - interval '1 hour', now() - interval '50 minutes')
		          RETURNING id::text`, cust, roomID, routineID, status)
	}
	runA := run(cust, roomA, vcRoutine, "succeeded")
	// An overnight run: started (power-off) 10 hours ago, powered on 5
	// minutes ago, ready 4 minutes ago. Its duration is the morning's
	// minute, not the 10 hours.
	q(`INSERT INTO nightly_run (customer_id, room_id, phase, status, scheduled_at, started_at, powered_on_at, completed_at)
	                VALUES ($1, $2, 'ready', 'succeeded', now() - interval '10 hours', now() - interval '10 hours',
	                        now() - interval '5 minutes', now() - interval '4 minutes') RETURNING id::text`, cust, roomA)
	run(cust, roomB, "", "failed") // no steps: still one row
	run(other, otherRoom, otherRoutine, "succeeded")
	for i, name := range []string{"Wait for codec", "Codec online"} {
		exec(`INSERT INTO nightly_step_result (customer_id, run_id, step_index, step_name, step_type, expected, actual, passed)
		      VALUES ($1, $2, $3, $4, 'expect_status', '{"status":"online"}', '{"status":"online"}', true)`,
			cust, runA, i, name)
	}

	code, body := call("GET", "/runs/export.csv", nil)
	if code != http.StatusOK {
		t.Fatalf("export: %d %s", code, body)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(recs) != 5 { // header + 2 steps + 2 step-less runs
		t.Fatalf("want 5 lines, got %d:\n%s", len(recs), body)
	}
	col := map[string]int{}
	for i, c := range recs[0] {
		col[c] = i
	}
	var sawFormula, sawSteps, sawOvernight bool
	for _, r := range recs[1:] {
		if r[col["room"]] == "Elsewhere" {
			t.Error("another customer's run is in the export")
		}
		if r[col["room"]] == "'=Huddle" {
			sawFormula = true
			if r[col["step_index"]] != "" || r[col["run_status"]] != "failed" {
				t.Errorf("step-less run row: %v", r)
			}
		}
		if r[col["room"]] == "Boardroom" && r[col["step_index"]] == "" {
			// The overnight run: measured from power-on, not power-off.
			sawOvernight = true
			if r[col["run_duration_seconds"]] != "60" || r[col["run_powered_on_at"]] == "" {
				t.Errorf("overnight run: duration %q powered on %q, want 60 and set",
					r[col["run_duration_seconds"]], r[col["run_powered_on_at"]])
			}
		}
		if r[col["step_name"]] == "Codec online" {
			sawSteps = r[col["routine"]] == "VC room check" && r[col["step_passed"]] == "true" &&
				r[col["run_duration_seconds"]] == "600" && strings.Contains(r[col["actual"]], "online")
		}
	}
	if !sawFormula {
		t.Error("formula-looking room name not escaped")
	}
	if !sawSteps {
		t.Error("step row missing or incomplete")
	}
	if !sawOvernight {
		t.Error("overnight run missing from the export")
	}

	// Filters apply, and bad input is a 400 rather than a broken file.
	if code, body := call("GET", "/runs/export.csv?status=succeeded&room_id="+roomA, nil); code != http.StatusOK || strings.Count(body, "\n") != 4 {
		t.Errorf("filtered export: %d\n%s", code, body)
	}
	if code, _ := call("GET", "/runs/export.csv?from=yesterday", nil); code != http.StatusBadRequest {
		t.Errorf("bad from: %d, want 400", code)
	}
}
