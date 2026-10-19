package portalapi

// Integration test for display links: only shared dashboards, the token
// signs in as a view-only display limited to its dashboards and its
// creator's scope, and revoking stops it at once. Also Rooms in use.
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16.

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
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestDisplayLinks(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping display link test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('display-links') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'dl-' || $1::text, 'dl', '\x00') RETURNING id::text`, cust)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	courtroom := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Court 1') RETURNING id::text`, cust, bld)
	q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Court 2') RETURNING id::text`, cust, bld)
	// Court 1 is in a call; a stale reading elsewhere doesn't count.
	q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol, latest_status, latest_metrics, last_seen_at)
	   VALUES ($1, $2, $3, 'codec', 'Codec', 'cisco_roomos', 'online', '{"active_calls": 1, "people_count": 4}', now())
	   RETURNING id::text`, cust, col, courtroom)
	q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol, latest_status, latest_metrics, last_seen_at)
	   VALUES ($1, $2, $3, 'old', 'Old', 'cisco_roomos', 'online', '{"active_calls": 1}', now() - interval '1 hour')
	   RETURNING id::text`, cust, col, courtroom)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{
		UserID: "00000000-0000-0000-0000-0000000000f1", Email: "admin@example.test", CustomerID: cust,
		Permissions: all, BuildingScopeIDs: []string{bld},
	}
	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /d", h.CreateDashboard)
	mux.HandleFunc("GET /d", h.ListDashboards)
	mux.HandleFunc("GET /d/{id}", h.GetDashboard)
	mux.HandleFunc("PUT /d/{id}", h.UpdateDashboard)
	mux.HandleFunc("GET /links", h.ListDisplayLinks)
	mux.HandleFunc("POST /links", h.CreateDisplayLink)
	mux.HandleFunc("DELETE /links/{id}", h.RevokeDisplayLink)
	mux.HandleFunc("GET /display", h.CurrentDisplay)
	mux.HandleFunc("GET /in-use", h.RoomsInUse)
	call := func(who portalauth.Principal, method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	id := func(b []byte) string {
		var v struct{ ID string }
		_ = json.Unmarshal(b, &v)
		return v.ID
	}

	_, b := call(admin, "POST", "/d", map[string]any{"name": "Wall 1", "shared": true})
	wall1 := id(b)
	_, b = call(admin, "POST", "/d", map[string]any{"name": "Wall 2", "shared": true})
	wall2 := id(b)
	_, b = call(admin, "POST", "/d", map[string]any{"name": "Other", "shared": true})
	other := id(b)
	_, b = call(admin, "POST", "/d", map[string]any{"name": "Mine"})
	mine := id(b)

	// Only shared dashboards can go on a display.
	if code, _ := call(admin, "POST", "/links", map[string]any{"name": "Bad", "dashboard_ids": []string{wall1, mine}}); code != 400 {
		t.Fatalf("link with a personal dashboard = %d, want 400", code)
	}
	code, b := call(admin, "POST", "/links", map[string]any{"name": "Service desk", "dashboard_ids": []string{wall2, wall1, wall2}, "rotate_seconds": 30})
	if code != 201 {
		t.Fatalf("create link = %d %s", code, b)
	}
	var created struct {
		Token string `json:"token"`
		Link  struct {
			ID           string   `json:"id"`
			DashboardIDs []string `json:"dashboard_ids"`
		} `json:"link"`
	}
	_ = json.Unmarshal(b, &created)
	if !slices.Equal(created.Link.DashboardIDs, []string{wall2, wall1}) || len(created.Token) < 40 {
		t.Fatalf("created = %+v, want [wall2 wall1] once each and a token", created)
	}
	if n := q(`SELECT count(*)::text FROM dashboard_display_links WHERE token_hash = $1`, created.Token); n != "0" {
		t.Error("the raw token was stored")
	}

	// The token signs in as a display: view-only, the creator's scope.
	resolver := portalauth.NewDisplayResolver(st.AdminPool())
	disp, ok := resolver.Resolve(created.Token)
	if !ok || disp.CustomerID != cust || disp.DisplayLinkID != created.Link.ID {
		t.Fatalf("resolve = %v %+v", ok, disp)
	}
	if !slices.Equal(disp.BuildingScopeIDs, []string{bld}) {
		t.Errorf("display scope = %v, want the creator's building", disp.BuildingScopeIDs)
	}
	for _, perm := range []string{portalauth.PermDashboardShare, portalauth.PermViewAudit, portalauth.PermCommandDevice, portalauth.PermDeviceCRUD} {
		if disp.HasPermission(perm) {
			t.Errorf("display has %s", perm)
		}
	}
	if disp.ActorLabel() != "display: Service desk" {
		t.Errorf("actor = %q", disp.ActorLabel())
	}
	if _, ok := resolver.Resolve("dsp_wrong"); ok {
		t.Error("a wrong token resolved")
	}

	// It sees only its dashboards, in its order, and can't change them.
	code, b = call(disp, "GET", "/display", nil)
	var cur struct {
		Name          string   `json:"name"`
		DashboardIDs  []string `json:"dashboard_ids"`
		RotateSeconds int      `json:"rotate_seconds"`
	}
	_ = json.Unmarshal(b, &cur)
	if code != 200 || cur.Name != "Service desk" || !slices.Equal(cur.DashboardIDs, []string{wall2, wall1}) || cur.RotateSeconds != 30 {
		t.Errorf("display = %d %+v", code, cur)
	}
	if code, _ := call(disp, "GET", "/d/"+wall1, nil); code != 200 {
		t.Errorf("display reading its dashboard = %d", code)
	}
	if code, _ := call(disp, "GET", "/d/"+other, nil); code != 404 {
		t.Errorf("display reading another shared dashboard = %d, want 404", code)
	}
	_, b = call(disp, "GET", "/d", nil)
	var list struct {
		Dashboards []struct{ ID string } `json:"dashboards"`
	}
	_ = json.Unmarshal(b, &list)
	if len(list.Dashboards) != 2 {
		t.Errorf("display's list = %s, want its two dashboards", b)
	}
	if code, _ := call(disp, "PUT", "/d/"+wall1, map[string]any{"name": "hacked"}); code != 403 {
		t.Errorf("display changing a dashboard = %d, want 403", code)
	}
	if code, _ := call(admin, "GET", "/display", nil); code != 404 {
		t.Errorf("a person calling /display = %d, want 404", code)
	}

	// Unsharing a dashboard takes it off the display.
	call(admin, "PUT", "/d/"+wall1, map[string]any{"shared": false})
	_, b = call(disp, "GET", "/display", nil)
	_ = json.Unmarshal(b, &cur)
	if !slices.Equal(cur.DashboardIDs, []string{wall2}) {
		t.Errorf("after unsharing = %v, want [wall2]", cur.DashboardIDs)
	}

	// Rooms in use: Court 1 from its fresh reading only.
	code, b = call(disp, "GET", "/in-use", nil)
	var inUse []struct {
		RoomName string `json:"room_name"`
		InCall   bool   `json:"in_call"`
		People   *int   `json:"people"`
	}
	_ = json.Unmarshal(b, &inUse)
	if code != 200 || len(inUse) != 1 || inUse[0].RoomName != "Court 1" || !inUse[0].InCall || inUse[0].People == nil || *inUse[0].People != 4 {
		t.Errorf("in use = %d %s", code, b)
	}

	// Revoking stops it at once.
	if code, _ := call(admin, "DELETE", "/links/"+created.Link.ID, nil); code != 204 {
		t.Fatalf("revoke = %d", code)
	}
	if _, ok := resolver.Resolve(created.Token); ok {
		t.Error("a revoked link still resolves")
	}
	_, b = call(admin, "GET", "/links", nil)
	if bytes.Contains(b, []byte(created.Link.ID)) {
		t.Error("revoked link still listed")
	}
	if n := q(`SELECT count(*)::text FROM audit_log WHERE customer_id = $1 AND action LIKE 'display_link.%'`, cust); n != "2" {
		t.Errorf("display link audit rows = %s, want 2", n)
	}
}
