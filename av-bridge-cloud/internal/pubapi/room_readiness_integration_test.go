package pubapi

// Integration test for the /pub/v1/room-readiness routes against a real
// Postgres. Same TEST_DATABASE_URL harness as devices_integration_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestRoomReadinessEndpoints(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping public API integration test")
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
	room := func(cust, name string) string {
		reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
		loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
		b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	cust := q(`INSERT INTO customers (name) VALUES ('readiness-test') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('readiness-other') RETURNING id::text`)
	boardroom := room(cust, "Boardroom")
	huddle := room(cust, "Huddle")
	unrun := room(cust, "New room")
	theirs := room(other, "Elsewhere")
	std := q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, 'Standard check') RETURNING id::text`, cust)
	vc := q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, 'VC check') RETURNING id::text`, cust)
	q(`INSERT INTO nightly_schedule (customer_id, test_routine_id) VALUES ($1, $2) RETURNING id::text`, cust, std)
	q(`INSERT INTO room_nightly_config (customer_id, room_id, test_routine_id) VALUES ($1, $2, $3) RETURNING id::text`, cust, boardroom, vc)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'readiness-' || $1::uuid::text, 'c', '\x00') RETURNING id::text`, cust)
	codec := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol)
	            VALUES ($1, $2, 'codec-1', 'Codec', 'ping') RETURNING id::text`, cust, col)

	run := func(cust, roomID, routineID, status, daysAgo string) string {
		return q(`INSERT INTO nightly_run (customer_id, room_id, routine_id, phase, status, scheduled_at, started_at, completed_at, failure_reason)
		          VALUES ($1, $2, NULLIF($3,'')::uuid, CASE WHEN $4 = 'failed' THEN 'failed' ELSE 'ready' END, $4,
		                  '2026-10-10T06:00:00Z'::timestamptz - ($5 || ' days')::interval,
		                  '2026-10-10T06:00:00Z'::timestamptz - ($5 || ' days')::interval,
		                  '2026-10-10T06:05:00Z'::timestamptz - ($5 || ' days')::interval,
		                  CASE WHEN $4 = 'failed' THEN 'step 1 failed' END)
		          RETURNING id::text`, cust, roomID, routineID, status, daysAgo)
	}
	old := run(cust, boardroom, vc, "succeeded", "2")
	latest := run(cust, boardroom, vc, "failed", "0")
	huddleRun := run(cust, huddle, std, "succeeded", "1")
	theirRun := run(other, theirs, "", "succeeded", "0")
	q(`INSERT INTO nightly_step_result (customer_id, run_id, step_index, step_name, step_type, passed, started_at, completed_at)
	   VALUES ($1, $2, 0, 'Settle', 'wait', true, now(), now()) RETURNING id::text`, cust, latest)
	q(`INSERT INTO nightly_step_result (customer_id, run_id, device_id, step_index, step_name, step_type, expected, actual, passed, error)
	   VALUES ($1, $2, $3, 1, 'Codec online', 'expect_status', '{"status":"online"}', '{"status":"offline"}', false, 'codec offline')
	   RETURNING id::text`, cust, latest, codec)

	h := New(st, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms", h.ListReadinessRooms)
	mux.HandleFunc("GET /runs", h.ListReadinessRuns)
	mux.HandleFunc("GET /runs/{id}", h.GetReadinessRun)
	get := func(path string, into any) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if into != nil && rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
				t.Fatalf("%s: %v\n%s", path, err, rec.Body.String())
			}
		}
		return rec.Code
	}
	type page struct {
		Data       []publicReadinessRun `json:"data"`
		NextCursor *string              `json:"next_cursor"`
	}

	// Runs: newest first, other tenant's absent, step counts filled in.
	var p page
	if code := get("/runs", &p); code != http.StatusOK || len(p.Data) != 3 {
		t.Fatalf("list runs: %d, %d rows", code, len(p.Data))
	}
	if p.Data[0].ID != latest || p.Data[1].ID != huddleRun || p.Data[2].ID != old {
		t.Errorf("order: got %s %s %s", p.Data[0].ID, p.Data[1].ID, p.Data[2].ID)
	}
	l := p.Data[0]
	if l.StepsTotal != 2 || l.StepsPassed != 1 || l.StepsFailed != 1 || l.Routine == nil || *l.Routine != "VC check" ||
		l.DurationSeconds == nil || *l.DurationSeconds != 300 || l.FailureReason == nil || l.Room != "Boardroom" {
		t.Errorf("latest run summary: %+v", l)
	}

	// Paging walks every run once.
	seen := map[string]bool{}
	path := "/runs?limit=1"
	for i := 0; i < 5; i++ {
		var pg page
		if code := get(path, &pg); code != http.StatusOK {
			t.Fatalf("page %d: %d", i, code)
		}
		for _, r := range pg.Data {
			seen[r.ID] = true
		}
		if pg.NextCursor == nil {
			break
		}
		path = "/runs?limit=1&cursor=" + url.QueryEscape(*pg.NextCursor)
	}
	if len(seen) != 3 {
		t.Errorf("paging saw %d runs, want 3", len(seen))
	}

	// Filters.
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/runs?status=failed", 1},
		{"/runs?status=succeeded,failed", 3},
		{"/runs?room_id=" + boardroom, 2},
		{"/runs?since=2026-10-09T00:00:00Z", 2},
		{"/runs?until=2026-10-09T00:00:00Z", 1},
	} {
		var pg page
		if code := get(tc.path, &pg); code != http.StatusOK || len(pg.Data) != tc.want {
			t.Errorf("%s: %d, %d rows, want %d", tc.path, code, len(pg.Data), tc.want)
		}
	}
	for _, bad := range []string{"/runs?status=done", "/runs?room_id=x", "/runs?since=yesterday", "/runs?cursor=junk"} {
		if code := get(bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}

	// Detail: steps in order with device and expected/actual.
	var d struct {
		publicReadinessRun
		Steps []publicReadinessStep `json:"steps"`
	}
	if code := get("/runs/"+latest, &d); code != http.StatusOK || len(d.Steps) != 2 {
		t.Fatalf("run detail: %d, %d steps", code, len(d.Steps))
	}
	s1 := d.Steps[1]
	if d.Steps[0].Name != "Settle" || s1.Passed || s1.Device == nil || *s1.Device != "Codec" ||
		string(s1.Actual) != `{"status":"offline"}` || s1.Error == nil || string(d.Steps[0].Expected) != "null" {
		t.Errorf("steps: %+v", d.Steps)
	}
	var empty struct {
		Steps []publicReadinessStep `json:"steps"`
	}
	if code := get("/runs/"+huddleRun, &empty); code != http.StatusOK || empty.Steps == nil || len(empty.Steps) != 0 {
		t.Errorf("run without steps should have steps: [], got %d %v", code, empty.Steps)
	}
	for _, id := range []string{theirRun, "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if code := get("/runs/"+id, nil); code != http.StatusNotFound {
			t.Errorf("run %s: %d, want 404", id, code)
		}
	}

	// Rooms: routine per room and the latest run.
	var rooms []publicReadinessRoom
	if code := get("/rooms", &rooms); code != http.StatusOK || len(rooms) != 3 {
		t.Fatalf("rooms: %d, %d rows", code, len(rooms))
	}
	byID := map[string]publicReadinessRoom{}
	for _, r := range rooms {
		byID[r.RoomID] = r
	}
	if r := byID[boardroom]; r.Routine == nil || *r.Routine != "VC check" || r.LatestRun == nil || r.LatestRun.ID != latest || r.LatestRun.StepsFailed != 1 {
		t.Errorf("boardroom: %+v", r)
	}
	if r := byID[huddle]; r.Routine == nil || *r.Routine != "Standard check" || r.LatestRun == nil || r.LatestRun.ID != huddleRun {
		t.Errorf("huddle: %+v", r)
	}
	if r := byID[unrun]; r.LatestRun != nil || r.Routine == nil {
		t.Errorf("room with no runs: %+v", r)
	}

	// The routes need nightly.view.
	gated := RequireScope(portalauth.PermNightlyView)(http.HandlerFunc(h.ListReadinessRuns))
	for _, tc := range []struct {
		scopes map[string]struct{}
		want   int
	}{
		{map[string]struct{}{portalauth.PermViewDashboard: {}}, http.StatusForbidden},
		{map[string]struct{}{portalauth.PermNightlyView: {}}, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/runs", nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust, Scopes: tc.scopes}))
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("scopes %v: %d, want %d", tc.scopes, rec.Code, tc.want)
		}
	}
}
