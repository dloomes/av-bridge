package portalapi

// Integration tests for the roadmap read API and the roadmap_editor flag on
// helpdesk users. Run only when TEST_DATABASE_URL points at a disposable
// Postgres 16.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

type roadmapEnv struct {
	t   *testing.T
	ctx context.Context
	su  *pgx.Conn
	st  *db.Store
	h   *Handler
}

func newRoadmapEnv(t *testing.T) *roadmapEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set - skipping roadmap test")
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
	t.Cleanup(func() { su.Close(ctx) })
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return &roadmapEnv{t: t, ctx: ctx, su: su, st: st, h: New(st, nil, nil, nil, nil, quiet)}
}

func (e *roadmapEnv) q(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.su.QueryRow(e.ctx, sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *roadmapEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.su.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *roadmapEnv) vendorTenant() string {
	e.t.Helper()
	vt := db.PocVendorTenantUUID()
	e.exec(`INSERT INTO vendor_tenants (id, name, entra_tenant_id) VALUES ($1, 'Involve', $2)
		ON CONFLICT DO NOTHING`, vt, db.PocVendorTenantID)
	return vt
}

func TestRoadmapRead(t *testing.T) {
	e := newRoadmapEnv(t)
	mux := http.NewServeMux()
	resolver := portalauth.NewLocalResolver(e.st.AdminPool())
	mux.Handle("GET /roadmap", portalauth.Middleware(resolver, http.HandlerFunc(e.h.Roadmap)))
	get := func(bearer string) (int, []byte) {
		req := httptest.NewRequest("GET", "/roadmap", http.NoBody)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	// Session for a user, resolved by the real local resolver.
	tag := rmRandHex(t, 4)
	session := func(userID string) string {
		tok := portalauth.LocalTokenPrefix + rmRandHex(t, 32)
		e.exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		return tok
	}

	cust := e.q(`INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, "roadmap-read-"+tag)
	viewer := e.q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, 'x', 'viewer') RETURNING id::text`,
		cust, "viewer-"+tag+"@roadmap.test")
	vt := e.vendorTenant()
	operator := e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'operator') RETURNING id::text`,
		vt, "op-"+tag+"@roadmap.test")

	// A customer viewer reads the roadmap.
	code, body := get(session(viewer))
	if code != 200 {
		t.Fatalf("customer viewer = %d %s", code, body)
	}
	var rm struct {
		Settings map[string]any   `json:"settings"`
		Periods  []map[string]any `json:"periods"`
		Themes   []map[string]any `json:"themes"`
		Items    []map[string]any `json:"items"`
		Shipped  []map[string]any `json:"shipped"`
	}
	if err := json.Unmarshal(body, &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.Items) != 17 || len(rm.Periods) != 4 || len(rm.Themes) != 4 || len(rm.Shipped) != 7 {
		t.Fatalf("counts items=%d periods=%d themes=%d shipped=%d, want 17/4/4/7",
			len(rm.Items), len(rm.Periods), len(rm.Themes), len(rm.Shipped))
	}
	if rm.Settings["title"] != "M.A.R.C.U.S. Roadmap 2027" || rm.Settings["as_of"] != "2026-11-02" {
		t.Fatalf("settings = %v", rm.Settings)
	}
	for _, it := range rm.Items {
		if _, has := it["release_month"]; has && it["release_month"] == "" {
			t.Fatalf("empty release_month should be omitted: %v", it)
		}
	}
	if rm.Periods[0]["name"] != "Now" || rm.Periods[0]["is_current"] != true {
		t.Fatalf("first period = %v", rm.Periods[0])
	}

	// An Involve operator reads it too.
	if code, body := get(session(operator)); code != 200 {
		t.Fatalf("operator = %d %s", code, body)
	}

	// A display link may not.
	dash := e.q(`INSERT INTO dashboards (customer_id, name, created_by) VALUES ($1, 'Wall', 'test') RETURNING id::text`, cust)
	displayToken := makeDisplayLink(t, e, cust, dash, viewer)
	dres := portalauth.NewDisplayResolver(e.st.AdminPool())
	if p, ok := dres.Resolve(displayToken); !ok || p.DisplayLinkID == "" {
		t.Fatalf("display link did not resolve: %v %v", p, ok)
	}
	dmux := http.NewServeMux()
	dmux.Handle("GET /roadmap", portalauth.Middleware(dres, http.HandlerFunc(e.h.Roadmap)))
	req := httptest.NewRequest("GET", "/roadmap", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+displayToken)
	rec := httptest.NewRecorder()
	dmux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("display link = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// No auth.
	if code, _ := get(""); code != 401 {
		t.Fatalf("no auth = %d, want 401", code)
	}
}

// makeDisplayLink creates a display link through the handler so the token and
// hash agree with the resolver, and returns the token.
func makeDisplayLink(t *testing.T, e *roadmapEnv, cust, dash, creator string) string {
	t.Helper()
	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: creator, Email: "viewer@roadmap.test", CustomerID: cust, Permissions: all}
	b, _ := json.Marshal(map[string]any{"name": "Roadmap wall", "dashboard_ids": []string{dash}})
	req := httptest.NewRequest("POST", "/links", bytes.NewReader(b))
	req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
	rec := httptest.NewRecorder()
	e.h.CreateDisplayLink(rec, req)
	if rec.Code != 201 {
		t.Fatalf("create display link = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Token
}

func TestHelpdeskRoadmapEditorFlag(t *testing.T) {
	e := newRoadmapEnv(t)
	tag := rmRandHex(t, 4)
	vt := e.vendorTenant()
	cust := e.q(`INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, "roadmap-flag-"+tag)
	custUser := e.q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		cust, "cu-"+tag+"@roadmap.test")
	target := e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'operator') RETURNING id::text`,
		vt, "target-"+tag+"@roadmap.test")
	adminEmail := "admin-" + tag + "@roadmap.test"
	e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`, vt, adminEmail)
	opID := e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'operator') RETURNING id::text`,
		vt, "op-"+tag+"@roadmap.test")
	t.Cleanup(func() {
		e.exec(`DELETE FROM users WHERE email LIKE '%-' || $1 || '@roadmap.test'`, tag)
	})

	asAdmin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000a1", Email: adminEmail, IsVendor: true, Role: "admin"}
	asOperator := portalauth.Principal{UserID: opID, Email: "op@roadmap.test", IsVendor: true, Role: "operator"}

	mux := http.NewServeMux()
	mux.Handle("GET /users", portalauth.RequireVendor(http.HandlerFunc(e.h.HelpdeskListUsers)))
	mux.Handle("PATCH /users/{id}", portalauth.RequireVendorAdmin(http.HandlerFunc(e.h.HelpdeskUpdateUser)))
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
	listed := func(id string) bool {
		t.Helper()
		code, b := call(asAdmin, "GET", "/users", nil)
		if code != 200 {
			t.Fatalf("list = %d %s", code, b)
		}
		var rows []struct {
			ID            string `json:"id"`
			RoadmapEditor bool   `json:"roadmap_editor"`
		}
		_ = json.Unmarshal(b, &rows)
		for _, r := range rows {
			if r.ID == id {
				return r.RoadmapEditor
			}
		}
		t.Fatalf("user %s not listed", id)
		return false
	}
	resolve := func(userID string) portalauth.Principal {
		t.Helper()
		tok := portalauth.LocalTokenPrefix + rmRandHex(t, 32)
		e.exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		p, ok := portalauth.NewLocalResolver(e.st.AdminPool()).Resolve(tok)
		if !ok {
			t.Fatal("session did not resolve")
		}
		return p
	}

	if listed(target) || resolve(target).RoadmapEditor {
		t.Fatal("roadmap_editor should default to false")
	}

	// An Involve admin sets it.
	if code, b := call(asAdmin, "PATCH", "/users/"+target, map[string]any{"roadmap_editor": true}); code != 204 {
		t.Fatalf("admin set = %d %s", code, b)
	}
	if !listed(target) {
		t.Fatal("list does not show roadmap_editor")
	}
	if !resolve(target).RoadmapEditor {
		t.Fatal("resolved principal lacks RoadmapEditor")
	}

	// An operator cannot, through the vendor-admin guard.
	if code, _ := call(asOperator, "PATCH", "/users/"+target, map[string]any{"roadmap_editor": false}); code != 403 {
		t.Fatalf("operator set = %d, want 403", code)
	}
	if !listed(target) {
		t.Fatal("operator attempt changed the flag")
	}

	// A customer user cannot be a roadmap editor.
	code, b := call(asAdmin, "PATCH", "/users/"+custUser, map[string]any{"roadmap_editor": true})
	var msg struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &msg)
	if code != 400 || msg.Error != "Only Involve users can be roadmap editors." {
		t.Fatalf("customer user = %d %s", code, b)
	}
	if e.q(`SELECT roadmap_editor::text FROM users WHERE id = $1`, custUser) != "false" {
		t.Fatal("customer user flag was set")
	}

	// Clearing works.
	if code, b := call(asAdmin, "PATCH", "/users/"+target, map[string]any{"roadmap_editor": false}); code != 204 {
		t.Fatalf("clear = %d %s", code, b)
	}
	if listed(target) || resolve(target).RoadmapEditor {
		t.Fatal("flag not cleared")
	}
}

func rmRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
