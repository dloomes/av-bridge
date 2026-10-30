package portalapi

// Integration test for readiness checks by room tag (migration 0071): a
// tag's check applies to its rooms, field by field between a room's own
// settings and the default; the tags' priority order picks between a
// room's tags; and the tag endpoints. Runs only when TEST_DATABASE_URL
// points at a disposable Postgres 16 (see device_move_integration_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestReadinessByTag(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping readiness-by-tag test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('readiness-tags') RETURNING id::text`)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Crown Court') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	routine := func(name string) string {
		return q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, $2) RETURNING id::text`, cust, name)
	}
	tag := func(name string) string {
		return q(`INSERT INTO room_tags (customer_id, name) VALUES ($1, $2) RETURNING id::text`, cust, name)
	}
	assign := func(roomID, tagID string) {
		q(`INSERT INTO room_tag_assignments (customer_id, room_id, tag_id) VALUES ($1, $2, $3) RETURNING room_id::text`, cust, roomID, tagID)
	}

	standard, court, pilot := routine("Standard"), routine("Court AM check"), routine("Pilot check")
	q(`INSERT INTO readiness_schedule (customer_id, enabled, test_routine_id, run_time) VALUES ($1, true, $2, '07:45') RETURNING customer_id::text`, cust, standard)
	courtroom, pilotTag := tag("Courtroom"), tag("Pilot")
	court1, court2, court3, meeting := room("Court 1"), room("Court 2"), room("Court 3"), room("Meeting room")
	assign(court1, courtroom)
	assign(court2, courtroom)
	assign(court2, pilotTag)
	assign(court3, courtroom)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000d1", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readiness/rooms", h.ListReadinessRooms)
	mux.HandleFunc("PATCH /readiness/rooms/{id}", h.UpdateReadinessRoom)
	mux.HandleFunc("GET /readiness/tags", h.ListReadinessTags)
	mux.HandleFunc("PATCH /readiness/tags/{id}", h.UpdateReadinessTag)
	mux.HandleFunc("DELETE /readiness/tags/{id}", h.ResetReadinessTag)
	mux.HandleFunc("PUT /readiness/tag-order", h.SetReadinessTagOrder)
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			bs, _ := json.Marshal(body)
			rd = bytes.NewReader(bs)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// check reports each room's routine, time and the tag it came from.
	check := func() map[string]string {
		t.Helper()
		code, body := call("GET", "/readiness/rooms", nil)
		if code != 200 {
			t.Fatalf("rooms: %d %s", code, body)
		}
		var rows []readinessRoomRow
		_ = json.Unmarshal([]byte(body), &rows)
		out := map[string]string{}
		for _, r := range rows {
			tagName := "-"
			if r.TagName != nil {
				tagName = *r.TagName
			}
			out[r.RoomName] = deref(r.EffectiveRoutineName) + "|" + r.EffectiveRunTime + "|" + tagName + "|inherits " + r.InheritedRunTime
		}
		return out
	}
	want := func(label string, expect map[string]string) {
		t.Helper()
		got := check()
		for room, w := range expect {
			if got[room] != w {
				t.Errorf("%s: %s = %q, want %q", label, room, got[room], w)
			}
		}
	}

	// No tag has a check yet: everything is the default.
	want("before", map[string]string{"Court 1": "Standard|07:45|-|inherits 07:45"})

	// Courtroom gets a routine and a time; Pilot a routine only.
	if code, body := call("PATCH", "/readiness/tags/"+courtroom, map[string]any{"test_routine_id": court, "run_time": "07:30"}); code != http.StatusNoContent {
		t.Fatalf("courtroom check: %d %s", code, body)
	}
	if code, _ := call("PATCH", "/readiness/tags/"+pilotTag, map[string]any{"test_routine_id": pilot}); code != http.StatusNoContent {
		t.Fatalf("pilot check: %d", code)
	}
	// Court 3 has its own time; the tag still gives its routine.
	if code, _ := call("PATCH", "/readiness/rooms/"+court3, map[string]any{"run_time": "09:00"}); code != http.StatusNoContent {
		t.Fatalf("room override: %d", code)
	}
	want("by tag", map[string]string{
		"Court 1":      "Court AM check|07:30|Courtroom|inherits 07:30",
		"Court 2":      "Court AM check|07:30|Courtroom|inherits 07:30", // Courtroom before Pilot, by name
		"Court 3":      "Court AM check|09:00|Courtroom|inherits 07:30",
		"Meeting room": "Standard|07:45|-|inherits 07:45",
	})

	// Put Pilot first: Court 2 takes Pilot's routine, and the default
	// time, since Pilot doesn't set one (Courtroom's time isn't borrowed).
	if code, _ := call("PUT", "/readiness/tag-order", map[string]any{"tag_ids": []string{pilotTag, courtroom}}); code != http.StatusNoContent {
		t.Fatalf("order: %d", code)
	}
	want("Pilot first", map[string]string{
		"Court 1": "Court AM check|07:30|Courtroom|inherits 07:30",
		"Court 2": "Pilot check|07:45|Pilot|inherits 07:45",
	})
	_, body := call("GET", "/readiness/tags", nil)
	if strings.Index(body, `"Pilot"`) > strings.Index(body, `"Courtroom"`) || !strings.Contains(body, `"run_time":"07:30"`) {
		t.Errorf("tag list order or settings: %s", body)
	}

	// Removing Pilot's check puts Court 2 back on Courtroom's.
	if code, _ := call("DELETE", "/readiness/tags/"+pilotTag, nil); code != http.StatusNoContent {
		t.Fatalf("reset: %d", code)
	}
	want("Pilot reset", map[string]string{"Court 2": "Court AM check|07:30|Courtroom|inherits 07:30"})

	// Another customer's routine can't be used.
	other := q(`INSERT INTO customers (name) VALUES ('readiness-tags-other') RETURNING id::text`)
	theirs := q(`INSERT INTO nightly_test_routine (customer_id, name) VALUES ($1, 'Theirs') RETURNING id::text`, other)
	if code, _ := call("PATCH", "/readiness/tags/"+courtroom, map[string]any{"test_routine_id": theirs}); code != http.StatusBadRequest {
		t.Errorf("another customer's routine: %d", code)
	}
	if code, _ := call("PUT", "/readiness/tag-order", map[string]any{"tag_ids": []string{"00000000-0000-0000-0000-000000000099"}}); code != http.StatusBadRequest {
		t.Errorf("unknown tag in order: %d", code)
	}
	_ = meeting
}
