package portalapi

// Integration test for the power schedule and one-off dates by room tag
// (migration 0072): a tag's power settings apply to its rooms, field by
// field between a room's own and the default; the tag endpoints; and
// dates for a tag's rooms. Runs only when TEST_DATABASE_URL points at a
// disposable Postgres 16 (see device_move_integration_test.go).

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

func TestPowerScheduleByTag(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping power-by-tag test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('power-tags') RETURNING id::text`)
	q(`INSERT INTO nightly_schedule (customer_id, enabled, power_off_time, power_on_time) VALUES ($1, true, '19:00', '07:30') RETURNING customer_id::text`, cust)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	b := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'Crown Court') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, b, name)
	}
	court1, court2, office := room("Court 1"), room("Court 2"), room("Office")
	courtroom := q(`INSERT INTO room_tags (customer_id, name) VALUES ($1, 'Courtroom') RETURNING id::text`, cust)
	for _, r := range []string{court1, court2} {
		q(`INSERT INTO room_tag_assignments (customer_id, room_id, tag_id) VALUES ($1, $2, $3) RETURNING room_id::text`, cust, r, courtroom)
	}

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e1", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nightly/rooms", h.ListNightlyRooms)
	mux.HandleFunc("PATCH /nightly/rooms/{id}", h.UpdateRoomOverride)
	mux.HandleFunc("GET /nightly/tags", h.ListPowerTags)
	mux.HandleFunc("PATCH /nightly/tags/{id}", h.UpdatePowerTag)
	mux.HandleFunc("DELETE /nightly/tags/{id}", h.ResetPowerTag)
	mux.HandleFunc("GET /nightly/dates", h.ListScheduleDates)
	mux.HandleFunc("POST /nightly/dates", h.CreateScheduleDate)
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
	plans := func() map[string]string {
		t.Helper()
		_, body := call("GET", "/nightly/rooms", nil)
		var rows []roomOverrideRow
		_ = json.Unmarshal([]byte(body), &rows)
		out := map[string]string{}
		for _, r := range rows {
			tag := "-"
			if r.TagName != nil {
				tag = *r.TagName
			}
			out[r.RoomName] = r.EffectivePowerOff + "-" + r.EffectivePowerOn + "|" + tag + "|inherits " + r.InheritedPowerOff
		}
		return out
	}
	want := func(label string, expect map[string]string) {
		t.Helper()
		got := plans()
		for room, w := range expect {
			if got[room] != w {
				t.Errorf("%s: %s = %q, want %q", label, room, got[room], w)
			}
		}
	}

	// Courtrooms power off later; Court 2 has its own power-on time.
	if code, body := call("PATCH", "/nightly/tags/"+courtroom, map[string]any{"power_off_time": "21:00"}); code != http.StatusNoContent {
		t.Fatalf("tag power: %d %s", code, body)
	}
	if code, _ := call("PATCH", "/nightly/rooms/"+court2, map[string]any{"power_on_time": "06:45"}); code != http.StatusNoContent {
		t.Fatalf("room override: %d", code)
	}
	want("by tag", map[string]string{
		"Court 1": "21:00-07:30|Courtroom|inherits 21:00",
		"Court 2": "21:00-06:45|Courtroom|inherits 21:00",
		"Office":  "19:00-07:30|-|inherits 19:00",
	})
	if _, body := call("GET", "/nightly/tags", nil); !strings.Contains(body, `"power_off_time":"21:00"`) || !strings.Contains(body, `"room_count":2`) {
		t.Errorf("tag list: %s", body)
	}
	if code, _ := call("PATCH", "/nightly/tags/"+courtroom, map[string]any{"power_on_time": "7:30am"}); code != http.StatusBadRequest {
		t.Errorf("bad time: %d", code)
	}

	// Removing the tag's settings returns its rooms to the default.
	if code, _ := call("DELETE", "/nightly/tags/"+courtroom, nil); code != http.StatusNoContent {
		t.Fatalf("reset: %d", code)
	}
	want("reset", map[string]string{"Court 1": "19:00-07:30|-|inherits 19:00"})

	// Dates for a tag's rooms: one per tag per day, not with a room too.
	date := map[string]any{"date": "2030-03-04", "tag_id": courtroom, "closed": true, "note": "Court closure"}
	if code, body := call("POST", "/nightly/dates", date); code != http.StatusCreated {
		t.Fatalf("tag date: %d %s", code, body)
	}
	if code, body := call("POST", "/nightly/dates", date); code != http.StatusConflict {
		t.Errorf("duplicate tag date: %d %s", code, body)
	}
	if code, _ := call("POST", "/nightly/dates", map[string]any{"date": "2030-03-04", "closed": true}); code != http.StatusCreated {
		t.Errorf("estate date on the same day: %d", code)
	}
	if code, _ := call("POST", "/nightly/dates", map[string]any{"date": "2030-03-05", "tag_id": courtroom, "room_id": office, "closed": true}); code != http.StatusBadRequest {
		t.Errorf("room and tag: %d", code)
	}
	if _, body := call("GET", "/nightly/dates?from=2030-03-01", nil); !strings.Contains(body, `"tag_name":"Courtroom"`) {
		t.Errorf("date list: %s", body)
	}
}
