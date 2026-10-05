package portalapi

// Integration test for the user-management guards (user_guards.go), the
// role-source-preserving user update, and sso_available on GET /branding.
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name guardtest -e POSTGRES_PASSWORD=pgtest -p 55435:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55435/postgres?sslmode=disable \
//	  go test ./internal/portalapi/ -run TestUserGuards -v

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
	"sort"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestUserGuards(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping user guard integration test")
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
		var id string
		if err := su.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	cust := q(`INSERT INTO customers (name, entra_tenant_id) VALUES ('guard-test', '00000000-1111-2222-3333-444444444444') RETURNING id::text`)
	r1 := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	r2 := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'South') RETURNING id::text`, cust)
	l1 := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Site N') RETURNING id::text`, cust, r1)
	b1 := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B1') RETURNING id::text`, cust, l1)

	// Roles: admin has every permission; useradmin can manage users and
	// view, but not much else; viewer only views.
	all := make([]string, 0, len(portalauth.KnownPermissions))
	for p := range portalauth.KnownPermissions {
		all = append(all, p)
	}
	role := func(name string, perms []string) string {
		id := q(`INSERT INTO roles (customer_id, name) VALUES ($1, $2) RETURNING id::text`, cust, name)
		for _, p := range perms {
			exec(`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`, id, p)
		}
		return id
	}
	adminRole := role("admin", all)
	userAdminPerms := []string{"view.users", "view.dashboard", "user.create", "user.update", "user.delete", "user.reset_password"}
	userAdminRole := role("useradmin", userAdminPerms)
	viewerRole := role("viewer", []string{"view.users", "view.dashboard"})
	extraRole := role("reports", []string{"view.reports"})

	user := func(email string, scopeCol string, scopeIDs []string, roles ...string) string {
		id := q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`, cust, email)
		if scopeCol != "" {
			exec(`UPDATE users SET `+scopeCol+` = $2::uuid[] WHERE id = $1`, id, scopeIDs)
		}
		for _, r := range roles {
			exec(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, id, r)
		}
		return id
	}
	wholeAdmin := user("whole.admin@example.test", "", nil, adminRole)
	northAdmin := user("north.admin@example.test", "region_scope_ids", []string{r1}, adminRole)
	northUser := user("north.user@example.test", "building_scope_ids", []string{b1}, viewerRole)
	northAdmin2 := user("north.admin2@example.test", "region_scope_ids", []string{r1}, adminRole)
	southUser := user("south.user@example.test", "region_scope_ids", []string{r2}, viewerRole)
	wholeViewer := user("whole.viewer@example.test", "", nil, viewerRole)
	userAdmin := user("user.admin@example.test", "", nil, userAdminRole)

	permSet := func(perms []string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		return m
	}
	principal := map[string]portalauth.Principal{
		"wholeAdmin": {UserID: wholeAdmin, CustomerID: cust, Permissions: permSet(all)},
		"northAdmin": {UserID: northAdmin, CustomerID: cust, Permissions: permSet(all), RegionScopeIDs: []string{r1}},
		"userAdmin":  {UserID: userAdmin, CustomerID: cust, Permissions: permSet(userAdminPerms)},
	}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users", h.ListUsers)
	mux.HandleFunc("POST /users", h.CreateUser)
	mux.HandleFunc("PATCH /users/{id}", h.UpdateUser)
	mux.HandleFunc("POST /users/{id}/reset-password", h.ResetUserPassword)
	mux.HandleFunc("DELETE /users/{id}", h.DeleteUser)
	mux.HandleFunc("GET /branding", h.GetBranding)
	call := func(who, method, path string, body any) (int, string) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), principal[who]))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	pw := map[string]string{"new_password": "a-long-enough-password"}

	// 1. A restricted admin lists only users inside their access, plus themselves.
	code, body := call("northAdmin", "GET", "/users", nil)
	var listed []userRow
	_ = json.Unmarshal([]byte(body), &listed)
	var emails []string
	for _, u := range listed {
		emails = append(emails, u.Email)
	}
	sort.Strings(emails)
	if want := "north.admin2@example.test,north.admin@example.test,north.user@example.test"; code != 200 || strings.Join(emails, ",") != want {
		t.Errorf("north admin's list: %d %v, want %s", code, emails, want)
	}
	if _, body := call("wholeAdmin", "GET", "/users", nil); strings.Count(body, `"email"`) != 7 {
		t.Errorf("whole-tenant admin should see all 7 users: %s", body)
	}

	// 2. ... and can't manage anyone outside it.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/users/" + southUser, map[string]any{"full_name": "x"}},
		{"POST", "/users/" + wholeViewer + "/reset-password", pw},
		{"DELETE", "/users/" + wholeAdmin, nil},
	} {
		if code, body := call("northAdmin", tc.method, tc.path, tc.body); code != http.StatusNotFound {
			t.Errorf("north admin %s %s: %d %s, want 404", tc.method, tc.path, code, body)
		}
	}
	if code, body := call("northAdmin", "PATCH", "/users/"+northUser, map[string]any{"full_name": "North User"}); code != 200 {
		t.Errorf("north admin editing a north user: %d %s", code, body)
	}

	// 3. Nobody can act on a user with more permissions, or give roles beyond their own.
	if code, body := call("userAdmin", "POST", "/users/"+northAdmin2+"/reset-password", pw); code != http.StatusForbidden {
		t.Errorf("user admin resetting an admin's password: %d %s, want 403", code, body)
	}
	if code, body := call("userAdmin", "DELETE", "/users/"+northAdmin2, nil); code != http.StatusForbidden {
		t.Errorf("user admin deleting an admin: %d %s, want 403", code, body)
	}
	if code, body := call("userAdmin", "PATCH", "/users/"+wholeViewer, map[string]any{"role_ids": []string{viewerRole, adminRole}}); code != http.StatusForbidden || !strings.Contains(body, "permissions you don't have") {
		t.Errorf("user admin granting admin: %d %s, want 403", code, body)
	}
	if code, body := call("userAdmin", "PATCH", "/users/"+wholeViewer, map[string]any{"full_name": "Viewer"}); code != 200 {
		t.Errorf("user admin renaming a viewer: %d %s", code, body)
	}
	if code, body := call("userAdmin", "POST", "/users", map[string]any{
		"email": "new.admin@example.test", "password": "a-long-enough-password", "role_ids": []string{adminRole},
	}); code != http.StatusForbidden {
		t.Errorf("user admin creating an admin: %d %s, want 403", code, body)
	}
	if code, body := call("userAdmin", "POST", "/users", map[string]any{
		"email": "new.viewer@example.test", "password": "a-long-enough-password", "role_ids": []string{viewerRole},
	}); code != 200 {
		t.Errorf("user admin creating a viewer: %d %s", code, body)
	}

	// 4. Editing roles keeps the source of the roles that stay.
	exec(`UPDATE user_roles SET granted_by = 'entra' WHERE user_id = $1 AND role_id = $2`, southUser, viewerRole)
	exec(`INSERT INTO user_roles (user_id, role_id, granted_by) VALUES ($1, $2, 'manual')`, southUser, userAdminRole)
	if code, body := call("wholeAdmin", "PATCH", "/users/"+southUser, map[string]any{"role_ids": []string{viewerRole, extraRole}}); code != 200 {
		t.Fatalf("whole admin editing roles: %d %s", code, body)
	}
	sources := map[string]string{}
	rows, err := su.Query(ctx, `SELECT role_id::text, granted_by FROM user_roles WHERE user_id = $1`, southUser)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var rid, by string
		_ = rows.Scan(&rid, &by)
		sources[rid] = by
	}
	rows.Close()
	if len(sources) != 2 || sources[viewerRole] != "entra" || sources[extraRole] != "manual" {
		t.Errorf("role sources after edit = %v, want viewer=entra, reports=manual, useradmin removed", sources)
	}
	_, body = call("wholeAdmin", "GET", "/users", nil)
	_ = json.Unmarshal([]byte(body), &listed)
	for _, u := range listed {
		if u.ID == southUser && (len(u.EntraRoleIDs) != 1 || u.EntraRoleIDs[0] != viewerRole) {
			t.Errorf("entra_role_ids = %v, want [%s]", u.EntraRoleIDs, viewerRole)
		}
	}

	// 5. sso_available on GET /branding needs both the cloud's customer
	// Entra app and the customer's Entra tenant.
	brandingSSO := func() bool {
		_, body := call("wholeAdmin", "GET", "/branding", nil)
		var b struct {
			SSOAvailable bool `json:"sso_available"`
		}
		_ = json.Unmarshal([]byte(body), &b)
		return b.SSOAvailable
	}
	if brandingSSO() {
		t.Error("sso_available should be false when customer SSO isn't configured on the cloud")
	}
	h.SetCustomerSSO(true)
	if !brandingSSO() {
		t.Error("sso_available should be true with customer SSO configured and a tenant set")
	}
	exec(`UPDATE customers SET entra_tenant_id = NULL WHERE id = $1`, cust)
	if brandingSSO() {
		t.Error("sso_available should be false without an Entra tenant")
	}
}
