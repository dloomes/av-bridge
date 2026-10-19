package portalapi

// Integration test for dashboards: personal ones are private to their
// owner, shared ones need dashboard.share to publish or change, and the
// layout is validated. Runs only when TEST_DATABASE_URL points at a
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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestDashboards(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping dashboards test")
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
	var cust string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('dashboards') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	viewOnly := map[string]struct{}{portalauth.PermViewDashboard: {}}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e1", Email: "admin@example.test", CustomerID: cust, Permissions: all}
	alice := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e2", Email: "alice@example.test", CustomerID: cust, Permissions: viewOnly}
	bob := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e3", Email: "bob@example.test", CustomerID: cust, Permissions: viewOnly}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /d", h.ListDashboards)
	mux.HandleFunc("POST /d", h.CreateDashboard)
	mux.HandleFunc("GET /d/{id}", h.GetDashboard)
	mux.HandleFunc("PUT /d/{id}", h.UpdateDashboard)
	mux.HandleFunc("DELETE /d/{id}", h.DeleteDashboard)
	call := func(who portalauth.Principal, method, path string, body any) (int, map[string]any) {
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
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	layout := map[string]any{"widgets": []map[string]any{
		{"id": "w1", "type": "status", "size": "m"},
		{"id": "w2", "type": "alerts", "size": "l", "config": map[string]any{"severity": "critical"}},
		{"id": "w3", "type": "notice", "size": "xl", "config": map[string]any{"text": "Court 3 out of use", "tone": "warning"}},
		{"id": "w4", "type": "bignumber", "size": "s", "config": map[string]any{"metric": "offline", "threshold": 3}},
	}}

	// Alice builds her own; Bob can't see or change it.
	code, mine := call(alice, "POST", "/d", map[string]any{"name": "My rooms", "layout": layout})
	if code != 201 || mine["shared"] != false || mine["widget_count"] != float64(4) {
		t.Fatalf("create personal = %d %v", code, mine)
	}
	mineID := mine["id"].(string)
	if code, _ := call(bob, "GET", "/d/"+mineID, nil); code != 404 {
		t.Errorf("bob reading alice's dashboard = %d, want 404", code)
	}
	if code, _ := call(bob, "PUT", "/d/"+mineID, map[string]any{"name": "x"}); code != 404 {
		t.Errorf("bob changing alice's dashboard = %d, want 404", code)
	}
	if code, out := call(alice, "PUT", "/d/"+mineID, map[string]any{"name": "My rooms (2)",
		"layout": map[string]any{"widgets": []map[string]any{{"id": "w1", "type": "usage", "size": "xl"}}}}); code != 200 || out["name"] != "My rooms (2)" || out["widget_count"] != float64(1) {
		t.Errorf("alice updating hers = %d %v", code, out)
	}

	// Sharing needs dashboard.share.
	if code, _ := call(alice, "POST", "/d", map[string]any{"name": "Wall", "shared": true}); code != 403 {
		t.Errorf("alice publishing = %d, want 403", code)
	}
	if code, _ := call(alice, "PUT", "/d/"+mineID, map[string]any{"shared": true}); code != 403 {
		t.Errorf("alice sharing hers = %d, want 403", code)
	}
	code, wall := call(admin, "POST", "/d", map[string]any{"name": "Service desk", "shared": true, "layout": layout})
	if code != 201 || wall["shared"] != true {
		t.Fatalf("admin publishing = %d %v", code, wall)
	}
	wallID := wall["id"].(string)

	// Everyone sees the shared one, but only the admin can change it.
	code, list := call(bob, "GET", "/d", nil)
	ds, _ := list["dashboards"].([]any)
	if code != 200 || len(ds) != 1 || ds[0].(map[string]any)["id"] != wallID || ds[0].(map[string]any)["can_edit"] != false || list["can_share"] != false {
		t.Errorf("bob's list = %d %v, want only the shared one, read-only", code, list)
	}
	code, list = call(alice, "GET", "/d", nil)
	if ds, _ := list["dashboards"].([]any); code != 200 || len(ds) != 2 || ds[0].(map[string]any)["shared"] != true {
		t.Errorf("alice's list = %v, want shared first then hers", list)
	}
	if code, _ := call(bob, "PUT", "/d/"+wallID, map[string]any{"name": "mine now"}); code != 403 {
		t.Errorf("bob changing the shared one = %d, want 403", code)
	}
	if code, _ := call(bob, "DELETE", "/d/"+wallID, nil); code != 403 {
		t.Errorf("bob deleting the shared one = %d, want 403", code)
	}

	// Unsharing makes it the admin's own.
	if code, out := call(admin, "PUT", "/d/"+wallID, map[string]any{"shared": false}); code != 200 || out["shared"] != false {
		t.Errorf("unshare = %d %v", code, out)
	}
	if code, _ := call(bob, "GET", "/d/"+wallID, nil); code != 404 {
		t.Errorf("bob reading the unshared one = %d, want 404", code)
	}

	// Layout validation.
	for name, bad := range map[string]any{
		"unknown type": map[string]any{"widgets": []map[string]any{{"id": "a", "type": "nope", "size": "m"}}},
		"bad size":     map[string]any{"widgets": []map[string]any{{"id": "a", "type": "status", "size": "huge"}}},
		"dup ids":      map[string]any{"widgets": []map[string]any{{"id": "a", "type": "status", "size": "m"}, {"id": "a", "type": "alerts", "size": "m"}}},
		"bad id":       map[string]any{"widgets": []map[string]any{{"id": "a b", "type": "status", "size": "m"}}},
		"not object":   []any{1, 2},
	} {
		if code, _ := call(alice, "PUT", "/d/"+mineID, map[string]any{"layout": bad}); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := call(alice, "POST", "/d", map[string]any{"name": "   "}); code != 400 {
		t.Errorf("blank name = %d, want 400", code)
	}

	if code, _ := call(alice, "DELETE", "/d/"+mineID, nil); code != 204 {
		t.Errorf("alice deleting hers = %d", code)
	}
	var n int
	_ = su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id = $1 AND action LIKE 'dashboard.%'`, cust).Scan(&n)
	if n < 5 {
		t.Errorf("audit rows = %d, want the creates, updates and delete", n)
	}
}
