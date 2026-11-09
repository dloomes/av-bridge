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
	"fmt"
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
	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
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
	// The roadmap tables are global and packages test concurrently against
	// one database: hold the shared roadmap test lock for the whole test.
	unlock, err := roadmap.TestLock(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
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
	// The tables are global. Roadmap tests hold roadmap.TestLock, and
	// TestRoadmapEditing restores what it changes, but earlier runs or
	// future editors may leave extra rows, so check shape rather than
	// exact seed counts or editable settings text.
	if len(rm.Items) < 17 || len(rm.Periods) != 4 || len(rm.Themes) < 4 || len(rm.Shipped) < 7 {
		t.Fatalf("counts items=%d periods=%d themes=%d shipped=%d, want >=17/4/>=4/>=7",
			len(rm.Items), len(rm.Periods), len(rm.Themes), len(rm.Shipped))
	}
	seeded := false
	for _, it := range rm.Items {
		if it["title"] == "Firmware vulnerability check" {
			seeded = true
		}
	}
	if !seeded {
		t.Fatal("seeded item 'Firmware vulnerability check' missing")
	}
	if s, _ := rm.Settings["title"].(string); s == "" {
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
	var logs bytes.Buffer
	e.h.log = slog.New(slog.NewJSONHandler(&logs, nil))
	// changeLogs returns the "roadmap editor changed" lines logged since the
	// last call, and checks none carries an email.
	changeLogs := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) != nil || m["msg"] != "helpdesk user roadmap editor changed" {
				continue
			}
			if strings.Contains(line, "@") {
				t.Errorf("log line carries an email: %s", line)
			}
			out = append(out, m)
		}
		logs.Reset()
		return out
	}
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
	if l := changeLogs(); len(l) != 1 || l[0]["level"] != "INFO" || l[0]["actor"] != "user:"+asAdmin.UserID ||
		l[0]["target_user_id"] != target || l[0]["roadmap_editor"] != true {
		t.Fatalf("change log = %v", l)
	}
	// Setting it to what it already is changes nothing and logs nothing.
	if code, b := call(asAdmin, "PATCH", "/users/"+target, map[string]any{"roadmap_editor": true}); code != 204 {
		t.Fatalf("admin set again = %d %s", code, b)
	}
	if l := changeLogs(); len(l) != 0 {
		t.Fatalf("no-op set logged %v", l)
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
	if l := changeLogs(); len(l) != 1 || l[0]["roadmap_editor"] != false || l[0]["target_user_id"] != target {
		t.Fatalf("clear log = %v", l)
	}
}

// TestRoadmapEditing drives the editor APIs through the same route table and
// guards as server.go. The roadmap tables are global and TestRoadmapRead reads
// them in parallel, so item tests use items created here, and every
// replace-the-list call is restored from a snapshot. Subtests run in order.
func TestRoadmapEditing(t *testing.T) {
	e := newRoadmapEnv(t)
	tag := rmRandHex(t, 4)
	vt := e.vendorTenant()
	cust := e.q(`INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, "roadmap-edit-"+tag)
	custAdmin := e.q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		cust, "ca-"+tag+"@roadmap.test")
	editor := e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		vt, "ed-"+tag+"@roadmap.test")
	t.Cleanup(func() {
		e.exec(`DELETE FROM users WHERE email LIKE '%-' || $1 || '@roadmap.test'`, tag)
	})

	mux := http.NewServeMux()
	resolver := portalauth.NewLocalResolver(e.st.AdminPool())
	for _, rt := range e.h.RoadmapEditorRoutes() {
		mux.Handle(rt.Pattern, portalauth.Middleware(resolver, portalauth.RequireVendor(rt.Handler)))
	}
	session := func(userID string) string {
		tok := portalauth.LocalTokenPrefix + rmRandHex(t, 32)
		e.exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		return tok
	}
	edTok := session(editor)
	caTok := session(custAdmin)
	const base = "/api/v1/helpdesk/roadmap"
	call := func(tok, scope, method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, base+path, rdr)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		if scope != "" {
			req.Header.Set("X-Customer-Scope", scope)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	errMsg := func(b []byte) string {
		var m struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &m)
		return m.Error
	}
	decode := func(b []byte) roadmap.Roadmap {
		t.Helper()
		var rm roadmap.Roadmap
		if err := json.Unmarshal(b, &rm); err != nil {
			t.Fatalf("decode roadmap: %v %s", err, b)
		}
		return rm
	}
	load := func() roadmap.Roadmap {
		t.Helper()
		tx, err := e.st.AdminPool().Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(e.ctx)
		rm, err := roadmap.Load(e.ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return rm
	}
	findItem := func(rm roadmap.Roadmap, id string) (roadmap.Item, bool) {
		for _, it := range rm.Items {
			if it.ID == id {
				return it, true
			}
		}
		return roadmap.Item{}, false
	}

	// Items this test creates live in its own temporary theme, never in a
	// seed cell; the theme and anything in it are removed at the end.
	theme := e.q(`INSERT INTO roadmap_themes (position, name, note) VALUES (99, $1, '') RETURNING id::text`,
		"Edit theme "+tag)
	t.Cleanup(func() {
		e.exec(`DELETE FROM roadmap_items WHERE theme_id = $1 OR title LIKE '%' || $2 || '%'`, theme, tag)
		e.exec(`DELETE FROM roadmap_themes WHERE id = $1`, theme)
		// Close the position gap SaveThemes may have left on seed themes.
		e.exec(`UPDATE roadmap_themes t SET position = r.rn - 1
			FROM (SELECT id, row_number() OVER (ORDER BY position, name) AS rn FROM roadmap_themes) r
			WHERE t.id = r.id`)
	})

	t.Run("non-editor Involve admin is refused", func(t *testing.T) {
		code, b := call(edTok, "", "POST", "/items", map[string]any{
			"theme_id": theme, "period": 3, "status": "exploring", "title": "x " + tag, "description": "d",
		})
		if code != 403 || errMsg(b) != "Only roadmap editors can change the roadmap." {
			t.Fatalf("non-editor = %d %s", code, b)
		}
	})

	e.exec(`UPDATE users SET roadmap_editor = true WHERE id = $1`, editor)

	var created string
	t.Run("create round-trips text unchanged", func(t *testing.T) {
		title := "<script>alert(1)</script> Display wall " + tag
		code, b := call(edTok, "", "POST", "/items", map[string]any{
			"theme_id": theme, "period": 3, "status": "exploring", "release_month": "Oct",
			"title": title, "description": "Shown as text.",
		})
		if code != 200 {
			t.Fatalf("create = %d %s", code, b)
		}
		rm := decode(b)
		for _, it := range rm.Items {
			if it.Title == title {
				created = it.ID
				if it.ThemeID != theme || it.Period != 3 || it.Status != "exploring" || it.ReleaseMonth != "Oct" {
					t.Fatalf("created item = %+v", it)
				}
			}
		}
		if created == "" {
			t.Fatalf("created item not in response")
		}
		if got := e.q(`SELECT updated_by FROM roadmap_items WHERE id = $1`, created); got != "ed-"+tag+"@roadmap.test" {
			t.Fatalf("updated_by = %q", got)
		}
	})

	t.Run("item validation", func(t *testing.T) {
		good := func() map[string]any {
			return map[string]any{"theme_id": theme, "period": 3, "status": "planned",
				"title": "valid " + tag, "description": "d"}
		}
		cases := map[string]func(m map[string]any){
			"empty title":     func(m map[string]any) { m["title"] = "   " },
			"long desc":       func(m map[string]any) { m["description"] = strings.Repeat("a", 241) },
			"bad status":      func(m map[string]any) { m["status"] = "maybe" },
			"period 4":        func(m map[string]any) { m["period"] = 4 },
			"long title":      func(m map[string]any) { m["title"] = strings.Repeat("a", 81) },
			"unknown theme":   func(m map[string]any) { m["theme_id"] = "00000000-0000-0000-0000-000000000000" },
			"long rel. month": func(m map[string]any) { m["release_month"] = strings.Repeat("a", 21) },
		}
		for name, mut := range cases {
			m := good()
			mut(m)
			code, b := call(edTok, "", "POST", "/items", m)
			if code != 400 || errMsg(b) == "" {
				t.Errorf("%s = %d %s, want 400", name, code, b)
			}
		}
		// A 240-character description is fine.
		m := good()
		m["description"] = strings.Repeat("a", 240)
		if code, b := call(edTok, "", "POST", "/items", m); code != 200 {
			t.Fatalf("240-char description = %d %s", code, b)
		}
		// Bad PATCH values are refused too.
		if code, b := call(edTok, "", "PATCH", "/items/"+created, map[string]any{"status": "maybe"}); code != 400 {
			t.Fatalf("patch bad status = %d %s", code, b)
		}
	})

	t.Run("patch moves period, delete removes", func(t *testing.T) {
		code, b := call(edTok, "", "PATCH", "/items/"+created, map[string]any{"period": 2, "release_month": ""})
		if code != 200 {
			t.Fatalf("patch = %d %s", code, b)
		}
		it, ok := findItem(decode(b), created)
		if !ok || it.Period != 2 || it.ReleaseMonth != "" || it.Status != "exploring" {
			t.Fatalf("patched = %+v %v", it, ok)
		}
		code, b = call(edTok, "", "DELETE", "/items/"+created, nil)
		if code != 200 {
			t.Fatalf("delete = %d %s", code, b)
		}
		if _, ok := findItem(decode(b), created); ok {
			t.Fatal("item still present after delete")
		}
		if code, _ := call(edTok, "", "DELETE", "/items/"+created, nil); code != 404 {
			t.Fatalf("second delete = %d, want 404", code)
		}
		if code, _ := call(edTok, "", "PATCH", "/items/not-a-uuid", map[string]any{"period": 1}); code != 404 {
			t.Fatalf("patch bad id = %d, want 404", code)
		}
	})

	t.Run("reorder needs the full set", func(t *testing.T) {
		// A fresh theme gives a cell no other test touches.
		themeTag := "Order theme " + tag
		cur := load()
		list := []map[string]any{}
		for _, th := range cur.Themes {
			list = append(list, map[string]any{"id": th.ID, "name": th.Name, "note": th.Note})
		}
		list = append(list, map[string]any{"id": "", "name": themeTag, "note": ""})
		t.Cleanup(func() {
			e.exec(`DELETE FROM roadmap_items WHERE theme_id IN (SELECT id FROM roadmap_themes WHERE name = $1)`, themeTag)
			e.exec(`DELETE FROM roadmap_themes WHERE name = $1`, themeTag)
		})
		code, b := call(edTok, "", "PUT", "/themes", list)
		if code != 200 {
			t.Fatalf("add theme = %d %s", code, b)
		}
		var tid string
		for _, th := range decode(b).Themes {
			if th.Name == themeTag {
				tid = th.ID
			}
		}
		if tid == "" {
			t.Fatal("new theme missing")
		}
		ids := []string{}
		for i := 0; i < 3; i++ {
			code, b := call(edTok, "", "POST", "/items", map[string]any{"theme_id": tid, "period": 1, "status": "planned",
				"title": fmt.Sprintf("order %d %s", i, tag), "description": "d"})
			if code != 200 {
				t.Fatalf("create = %d %s", code, b)
			}
			for _, it := range decode(b).Items {
				if it.Title == fmt.Sprintf("order %d %s", i, tag) {
					ids = append(ids, it.ID)
					if it.Position != i {
						t.Fatalf("new item position = %d, want %d", it.Position, i)
					}
				}
			}
		}
		if code, b := call(edTok, "", "PUT", "/items/order", map[string]any{"theme_id": tid, "period": 1, "ids": ids[:2]}); code != 400 {
			t.Fatalf("partial order = %d %s", code, b)
		}
		if code, b := call(edTok, "", "PUT", "/items/order", map[string]any{"theme_id": tid, "period": 1,
			"ids": []string{ids[0], ids[0], ids[1]}}); code != 400 {
			t.Fatalf("duplicate order = %d %s", code, b)
		}
		rev := []string{ids[2], ids[0], ids[1]}
		code, b = call(edTok, "", "PUT", "/items/order", map[string]any{"theme_id": tid, "period": 1, "ids": rev})
		if code != 200 {
			t.Fatalf("order = %d %s", code, b)
		}
		rm := decode(b)
		for want, id := range rev {
			if it, _ := findItem(rm, id); it.Position != want {
				t.Fatalf("item %s position = %d, want %d", id, it.Position, want)
			}
		}
		// Deleting the middle item closes the gap.
		code, b = call(edTok, "", "DELETE", "/items/"+ids[0], nil)
		if code != 200 {
			t.Fatalf("delete = %d %s", code, b)
		}
		rm = decode(b)
		if a, _ := findItem(rm, ids[2]); a.Position != 0 {
			t.Fatalf("first = %d", a.Position)
		}
		if c, _ := findItem(rm, ids[1]); c.Position != 1 {
			t.Fatalf("second = %d", c.Position)
		}

		// Leaving out a theme that still has items: 409, nothing changes.
		before := load()
		code, b = call(edTok, "", "PUT", "/themes", list[:len(list)-1])
		if code != 409 || errMsg(b) != "Move or delete this theme's items first." {
			t.Fatalf("drop theme with items = %d %s", code, b)
		}
		after := load()
		if len(after.Themes) != len(before.Themes) {
			t.Fatalf("themes changed after 409: %d -> %d", len(before.Themes), len(after.Themes))
		}

		// Once empty, the theme can go.
		e.exec(`DELETE FROM roadmap_items WHERE theme_id = $1`, tid)
		code, b = call(edTok, "", "PUT", "/themes", list[:len(list)-1])
		if code != 200 {
			t.Fatalf("drop empty theme = %d %s", code, b)
		}
		for _, th := range decode(b).Themes {
			if th.ID == tid {
				t.Fatal("empty theme not removed")
			}
		}
	})

	t.Run("periods", func(t *testing.T) {
		cur := load()
		t.Cleanup(func() {
			for _, p := range cur.Periods {
				e.exec(`UPDATE roadmap_periods SET name = $2, months = $3, is_current = $4 WHERE position = $1`,
					p.Position, p.Name, p.Months, p.IsCurrent)
			}
		})
		list := []map[string]any{}
		for _, p := range cur.Periods {
			list = append(list, map[string]any{"name": p.Name, "months": p.Months, "is_current": p.IsCurrent})
		}
		if code, b := call(edTok, "", "PUT", "/periods", list[:3]); code != 400 {
			t.Fatalf("3 periods = %d %s", code, b)
		}
		changed := append([]map[string]any{}, list...)
		changed[3] = map[string]any{"name": cur.Periods[3].Name, "months": "Test " + tag, "is_current": false}
		code, b := call(edTok, "", "PUT", "/periods", changed)
		if code != 200 {
			t.Fatalf("periods = %d %s", code, b)
		}
		if got := decode(b).Periods[3].Months; got != "Test "+tag {
			t.Fatalf("months = %q", got)
		}
		if code, b := call(edTok, "", "PUT", "/periods", list); code != 200 {
			t.Fatalf("restore periods = %d %s", code, b)
		}
	})

	t.Run("shipped", func(t *testing.T) {
		cur := load()
		t.Cleanup(func() {
			e.exec(`DELETE FROM roadmap_shipped WHERE title LIKE '%' || $1 || '%'`, tag)
		})
		list := []map[string]any{}
		for _, s := range cur.Shipped {
			list = append(list, map[string]any{"id": s.ID, "when": s.When, "title": s.Title})
		}
		withNew := append(append([]map[string]any{}, list...), map[string]any{"when": "Dec 2026", "title": "Shipped " + tag})
		code, b := call(edTok, "", "PUT", "/shipped", withNew)
		if code != 200 {
			t.Fatalf("shipped = %d %s", code, b)
		}
		rm := decode(b)
		if len(rm.Shipped) != len(cur.Shipped)+1 || rm.Shipped[len(rm.Shipped)-1].Title != "Shipped "+tag {
			t.Fatalf("shipped = %+v", rm.Shipped)
		}
		if rm.Shipped[0].ID != cur.Shipped[0].ID {
			t.Fatal("existing shipped ids not kept")
		}
		bad := append(append([]map[string]any{}, list...), map[string]any{"when": "", "title": "x"})
		if code, b := call(edTok, "", "PUT", "/shipped", bad); code != 400 {
			t.Fatalf("bad shipped = %d %s", code, b)
		}
		if code, b := call(edTok, "", "PUT", "/shipped", list); code != 200 {
			t.Fatalf("restore shipped = %d %s", code, b)
		}
	})

	t.Run("settings", func(t *testing.T) {
		cur := load()
		by := e.q(`SELECT COALESCE(updated_by, '') FROM roadmap_settings`)
		t.Cleanup(func() {
			e.exec(`UPDATE roadmap_settings SET title = $1, headline = $2, lede = $3, disclaimer = $4, as_of = $5::date,
				updated_at = $6, updated_by = NULLIF($7, '')`,
				cur.Settings.Title, cur.Settings.Headline, cur.Settings.Lede, cur.Settings.Disclaimer, cur.Settings.AsOf,
				cur.Settings.UpdatedAt, by)
		})
		body := map[string]any{"title": cur.Settings.Title, "headline": "Headline " + tag,
			"lede": cur.Settings.Lede, "disclaimer": cur.Settings.Disclaimer, "as_of": cur.Settings.AsOf}
		code, b := call(edTok, "", "PUT", "/settings", body)
		if code != 200 || decode(b).Settings.Headline != "Headline "+tag {
			t.Fatalf("settings = %d %s", code, b)
		}
		body["as_of"] = "2026-13-40"
		if code, b := call(edTok, "", "PUT", "/settings", body); code != 400 {
			t.Fatalf("bad as_of = %d %s", code, b)
		}
		body["as_of"] = cur.Settings.AsOf
		body["headline"] = cur.Settings.Headline
		if code, b := call(edTok, "", "PUT", "/settings", body); code != 200 {
			t.Fatalf("restore settings = %d %s", code, b)
		}
	})

	t.Run("customer admin is refused everywhere", func(t *testing.T) {
		routes := [][2]string{
			{"PUT", "/settings"}, {"POST", "/items"}, {"PATCH", "/items/" + theme}, {"DELETE", "/items/" + theme},
			{"PUT", "/items/order"}, {"PUT", "/periods"}, {"PUT", "/themes"}, {"PUT", "/shipped"},
			{"GET", "/links"}, {"POST", "/links"}, {"POST", "/links/" + theme + "/revoke"}, {"DELETE", "/links/" + theme},
		}
		for _, rt := range routes {
			for _, scope := range []string{"", cust} {
				if code, b := call(caTok, scope, rt[0], rt[1], map[string]any{}); code != 403 {
					t.Errorf("%s %s scope=%q = %d %s, want 403", rt[0], rt[1], scope, code, b)
				}
			}
		}
		if len(e.h.RoadmapEditorRoutes()) != len(routes) {
			t.Fatalf("route table has %d routes, test covers %d", len(e.h.RoadmapEditorRoutes()), len(routes))
		}
	})
}

func rmRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
