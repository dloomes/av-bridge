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
	// The power schedule is on (19:00 → 07:30), so readiness rooms report
	// their power times.
	exec(`INSERT INTO nightly_schedule (customer_id, enabled, power_off_time, power_on_time) VALUES ($1, true, '19:00', '07:30')`, cust)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	principal := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000b1", CustomerID: cust, Permissions: all}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readiness/schedule", h.GetReadinessSchedule)
	mux.HandleFunc("PATCH /readiness/schedule", h.UpdateReadinessSchedule)
	mux.HandleFunc("GET /readiness/rooms", h.ListReadinessRooms)
	mux.HandleFunc("PATCH /readiness/rooms/{id}", h.UpdateReadinessRoom)
	mux.HandleFunc("DELETE /readiness/rooms/{id}", h.ResetReadinessRoom)
	mux.HandleFunc("GET /runs/export.csv", h.ExportNightlyRuns)
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
	rooms := func() map[string]readinessRoomRow {
		t.Helper()
		code, body := call("GET", "/readiness/rooms", nil)
		if code != http.StatusOK {
			t.Fatalf("list rooms: %d %s", code, body)
		}
		var rows []readinessRoomRow
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]readinessRoomRow{}
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
	patch := func(path string, body any, want int) {
		t.Helper()
		if code, resp := call("PATCH", path, body); code != want {
			t.Errorf("PATCH %s %v: %d %s, want %d", path, body, code, resp, want)
		}
	}

	// ── The default readiness check ──────────────────────────────────────
	// Can't switch on with no routine anywhere; another customer's routine
	// is refused.
	patch("/readiness/schedule", map[string]any{"enabled": true}, http.StatusBadRequest)
	patch("/readiness/schedule", map[string]any{"test_routine_id": otherRoutine}, http.StatusBadRequest)
	patch("/readiness/schedule", map[string]any{"test_routine_id": defRoutine, "run_time": "08:15", "enabled": true}, http.StatusNoContent)
	patch("/readiness/schedule", map[string]any{"run_time": "8am"}, http.StatusBadRequest)
	var sched readinessScheduleOut
	if code, body := call("GET", "/readiness/schedule", nil); code != http.StatusOK || json.Unmarshal([]byte(body), &sched) != nil {
		t.Fatalf("get schedule: %d %s", code, body)
	}
	if !sched.Enabled || sched.TestRoutineName != "Standard check" || sched.RunTime != "08:15" || sched.Timezone == "" {
		t.Errorf("schedule: %+v", sched)
	}

	// Every room inherits it, and shows its power times.
	rows := rooms()
	if _, ok := rows[otherRoom]; ok {
		t.Error("another customer's room is listed")
	}
	if a := rows[roomA]; str(a.EffectiveRoutineID) != defRoutine || a.EffectiveRunTime != "08:15" || a.HasOverride ||
		!a.PowerEnabled || a.PowerOffTime != "19:00" || a.PowerOnTime != "07:30" {
		t.Errorf("room A inheriting: %+v", a)
	}

	// ── A room's own check ───────────────────────────────────────────────
	patch("/readiness/rooms/"+roomA, map[string]any{"test_routine_id": vcRoutine, "run_time": "09:00", "days_of_week": []int{1, 3}}, http.StatusNoContent)
	rows = rooms()
	if a := rows[roomA]; !a.HasOverride || str(a.EffectiveRoutineName) != "VC room check" || a.EffectiveRunTime != "09:00" ||
		len(a.EffectiveDays) != 2 || str(a.OverrideRoutineID) != vcRoutine {
		t.Errorf("room A with its own check: %+v", a)
	}
	if b := rows[roomB]; str(b.EffectiveRoutineID) != defRoutine || b.HasOverride {
		t.Errorf("room B should still inherit: %+v", b)
	}
	for _, bad := range []map[string]any{
		{"test_routine_id": otherRoutine},
		{"test_routine_id": "not-a-uuid"},
		{"run_time": "25:00"},
		{"days_of_week": []int{0}},
	} {
		patch("/readiness/rooms/"+roomA, bad, http.StatusBadRequest)
	}
	if a := rooms()[roomA]; str(a.EffectiveRoutineID) != vcRoutine {
		t.Error("a refused change altered the room's routine")
	}
	// null inherits one field; the rest stay the room's own.
	patch("/readiness/rooms/"+roomA, map[string]any{"run_time": nil}, http.StatusNoContent)
	if a := rooms()[roomA]; a.EffectiveRunTime != "08:15" || str(a.EffectiveRoutineID) != vcRoutine {
		t.Errorf("after clearing the time: %+v", a)
	}
	// Reset: back to the default entirely.
	if code, body := call("DELETE", "/readiness/rooms/"+roomA, nil); code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", code, body)
	}
	if a := rooms()[roomA]; a.HasOverride || str(a.EffectiveRoutineID) != defRoutine {
		t.Errorf("after reset: %+v", a)
	}
	if code, _ := call("PATCH", "/readiness/rooms/"+otherRoom, map[string]any{"run_time": "09:00"}); code != http.StatusNotFound {
		t.Errorf("another customer's room: %d, want 404", code)
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
			sawSteps = r[col["routine"]] == "VC room check" && r[col["step_passed"]] == "true" && r[col["kind"]] == "power" &&
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
	// The kind filter: these are all power runs.
	if code, body := call("GET", "/runs/export.csv?kind=readiness", nil); code != http.StatusOK || strings.Count(body, "\n") != 1 {
		t.Errorf("kind=readiness export: %d\n%s", code, body)
	}
	if code, _ := call("GET", "/runs/export.csv?kind=both", nil); code != http.StatusBadRequest {
		t.Errorf("bad kind: %d, want 400", code)
	}
	if code, _ := call("GET", "/runs/export.csv?from=yesterday", nil); code != http.StatusBadRequest {
		t.Errorf("bad from: %d, want 400", code)
	}
}
