package publicapi

// Integration test for roadmap share links: the editor link APIs (portalapi,
// mounted the way server.go mounts them) and the public read. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16.

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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalapi"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
)

func rmHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestRoadmapShareLinks(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set - skipping roadmap share link test")
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
	unlock, err := roadmap.TestLock(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)

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

	tag := rmHex(t, 4)
	vt := db.PocVendorTenantUUID()
	exec(`INSERT INTO vendor_tenants (id, name, entra_tenant_id) VALUES ($1, 'Involve', $2) ON CONFLICT DO NOTHING`,
		vt, db.PocVendorTenantID)
	editorEmail := "ed-" + tag + "@roadmap-links.test"
	editor := q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role, roadmap_editor)
		VALUES ($1, $2, 'x', 'admin', true) RETURNING id::text`, vt, editorEmail)
	plain := q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role)
		VALUES ($1, $2, 'x', 'admin') RETURNING id::text`, vt, "pl-"+tag+"@roadmap-links.test")
	t.Cleanup(func() {
		exec(`DELETE FROM roadmap_share_links WHERE name LIKE '%' || $1`, tag)
		exec(`DELETE FROM users WHERE email LIKE '%-' || $1 || '@roadmap-links.test'`, tag)
	})
	session := func(userID string) string {
		tok := portalauth.LocalTokenPrefix + rmHex(t, 32)
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		return tok
	}
	edTok, plTok := session(editor), session(plain)

	portalapi.SetMagicLinkPortalBaseURL("https://app.example.test/")
	t.Cleanup(func() { portalapi.SetMagicLinkPortalBaseURL("") })
	ph := portalapi.New(st, nil, nil, nil, nil, quiet)
	pub := NewHandler(st, quiet, notify.SMTPConfig{}, "", false, nil, "")

	mux := http.NewServeMux()
	resolver := portalauth.NewLocalResolver(st.AdminPool())
	for _, rt := range ph.RoadmapEditorRoutes() {
		mux.Handle(rt.Pattern, portalauth.Middleware(resolver, portalauth.RequireVendor(rt.Handler)))
	}
	mux.HandleFunc("GET /public/roadmap/{token}", pub.RoadmapPublic)

	const base = "/api/v1/helpdesk/roadmap/links"
	call := func(tok, method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, base+path, rdr)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	// The limiter belongs to this Handler; each caller IP gets its own window.
	const ip = "203.0.113.10"
	publicGet := func(token, from string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/public/roadmap/"+token, http.NoBody)
		req.Header.Set("X-Forwarded-For", "198.51.100.7, "+from)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	errMsg := func(b []byte) string {
		var m struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &m)
		return m.Error
	}
	checkHeaders := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
			t.Errorf("X-Robots-Tag = %q", got)
		}
	}
	const gone = "This link isn't valid any more."
	expectGone := func(token, why string) {
		t.Helper()
		rec := publicGet(token, ip)
		if rec.Code != 404 || errMsg(rec.Body.Bytes()) != gone {
			t.Fatalf("%s: GET = %d %s, want 404 %q", why, rec.Code, rec.Body.String(), gone)
		}
		checkHeaders(rec)
	}

	t.Run("non-editor is refused", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{"GET", ""}, {"POST", ""},
			{"POST", "/00000000-0000-0000-0000-000000000000/revoke"},
			{"DELETE", "/00000000-0000-0000-0000-000000000000"},
		} {
			code, b := call(plTok, c.method, c.path, map[string]any{"name": "x " + tag})
			if code != 403 || errMsg(b) != "Only roadmap editors can change the roadmap." {
				t.Errorf("%s %s = %d %s, want 403", c.method, c.path, code, b)
			}
		}
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"400 days":   {"name": "Acme " + tag, "expires_in_days": 400},
			"0 days":     {"name": "Acme " + tag, "expires_in_days": 0},
			"empty name": {"name": "   "},
			"long name":  {"name": strings.Repeat("a", 81)},
		} {
			if code, b := call(edTok, "POST", "", body); code != 400 || errMsg(b) == "" {
				t.Errorf("%s = %d %s, want 400", name, code, b)
			}
		}
	})

	type linkJSON struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		ExpiresAt  time.Time  `json:"expires_at"`
		CreatedBy  string     `json:"created_by"`
		LastUsedAt *time.Time `json:"last_used_at"`
		UseCount   int        `json:"use_count"`
		RevokedAt  *time.Time `json:"revoked_at"`
	}
	var link linkJSON
	var token string
	t.Run("create returns the URL once", func(t *testing.T) {
		code, b := call(edTok, "POST", "", map[string]any{"name": "  Acme Ltd " + tag + " "})
		if code != 200 {
			t.Fatalf("create = %d %s", code, b)
		}
		var out struct {
			Link linkJSON `json:"link"`
			URL  string   `json:"url"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		link = out.Link
		const prefix = "https://app.example.test/roadmap/s/rmp_"
		if !strings.HasPrefix(out.URL, prefix) {
			t.Fatalf("url = %q", out.URL)
		}
		token = strings.TrimPrefix(out.URL, "https://app.example.test/roadmap/s/")
		if len(token) != 47 {
			t.Fatalf("token = %q", token)
		}
		if link.Name != "Acme Ltd "+tag || link.CreatedBy != editorEmail || link.UseCount != 0 || link.RevokedAt != nil {
			t.Fatalf("link = %+v", link)
		}
		if d := time.Until(link.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
			t.Fatalf("expires_at %v is not ~90 days away", link.ExpiresAt)
		}
		if strings.Count(string(b), token) != 1 {
			t.Fatal("token appears outside url")
		}
		// Only the hash is stored.
		if n := q(`SELECT count(*)::text FROM roadmap_share_links
			WHERE id = $1 AND token_hash = $2`, link.ID, roadmap.HashToken(token)); n != "1" {
			t.Fatal("stored token_hash does not match")
		}
		if n := q(`SELECT count(*)::text FROM roadmap_share_links r
			WHERE id = $1 AND row_to_json(r)::text LIKE '%' || $2 || '%'`, link.ID, token); n != "0" {
			t.Fatal("plain token stored in the row")
		}
	})

	t.Run("public read", func(t *testing.T) {
		rec := publicGet(token, ip)
		if rec.Code != 200 {
			t.Fatalf("public GET = %d %s", rec.Code, rec.Body.String())
		}
		checkHeaders(rec)
		var out struct {
			roadmap.Roadmap
			SharedWith string `json:"shared_with"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.SharedWith != "Acme Ltd "+tag || len(out.Items) == 0 || len(out.Periods) != 4 || out.Settings.Title == "" {
			t.Fatalf("public body: shared_with=%q items=%d periods=%d", out.SharedWith, len(out.Items), len(out.Periods))
		}
		if strings.Contains(rec.Body.String(), editorEmail) {
			t.Fatal("public body leaks created_by")
		}
		if q(`SELECT use_count::text FROM roadmap_share_links WHERE id = $1`, link.ID) != "1" {
			t.Fatal("use_count not bumped")
		}
		if q(`SELECT (last_used_at IS NOT NULL)::text FROM roadmap_share_links WHERE id = $1`, link.ID) != "true" {
			t.Fatal("last_used_at not set")
		}
		code, b := call(edTok, "GET", "", nil)
		if code != 200 {
			t.Fatalf("list = %d %s", code, b)
		}
		var list struct {
			Links []linkJSON `json:"links"`
		}
		_ = json.Unmarshal(b, &list)
		found := false
		for _, l := range list.Links {
			if l.ID == link.ID {
				found = l.UseCount == 1 && l.LastUsedAt != nil
			}
		}
		if !found {
			t.Fatalf("list does not show the used link: %s", b)
		}
		if strings.Contains(string(b), token) {
			t.Fatal("list leaks the token")
		}
	})

	t.Run("revoke gives 404", func(t *testing.T) {
		code, b := call(edTok, "POST", "/"+link.ID+"/revoke", nil)
		if code != 200 {
			t.Fatalf("revoke = %d %s", code, b)
		}
		expectGone(token, "revoked")
		if q(`SELECT use_count::text FROM roadmap_share_links WHERE id = $1`, link.ID) != "1" {
			t.Fatal("use_count changed on a revoked link")
		}
		// A second revoke succeeds and keeps the original revoked_at.
		first := q(`SELECT revoked_at::text FROM roadmap_share_links WHERE id = $1`, link.ID)
		exec(`SELECT pg_sleep(0.01)`)
		if code, b := call(edTok, "POST", "/"+link.ID+"/revoke", nil); code != 200 {
			t.Fatalf("second revoke = %d %s", code, b)
		}
		if again := q(`SELECT revoked_at::text FROM roadmap_share_links WHERE id = $1`, link.ID); again != first {
			t.Fatalf("revoked_at changed on second revoke: %s -> %s", first, again)
		}
		if code, b := call(edTok, "POST", "/00000000-0000-0000-0000-000000000000/revoke", nil); code != 404 {
			t.Fatalf("revoke unknown = %d %s", code, b)
		}
	})

	t.Run("expired, unknown and malformed give 404", func(t *testing.T) {
		tok, hash := roadmap.NewToken()
		exec(`INSERT INTO roadmap_share_links (name, token_hash, expires_at, created_by)
			VALUES ($1, $2, now() - interval '1 minute', 'test')`, "Expired "+tag, hash)
		expectGone(tok, "expired")
		if q(`SELECT use_count::text FROM roadmap_share_links WHERE token_hash = $1`, hash) != "0" {
			t.Fatal("use_count bumped on an expired link")
		}
		random, _ := roadmap.NewToken()
		expectGone(random, "unknown")
		expectGone("not-a-token", "malformed")
	})

	t.Run("delete", func(t *testing.T) {
		if code, b := call(edTok, "DELETE", "/"+link.ID, nil); code != 200 {
			t.Fatalf("delete = %d %s", code, b)
		}
		if q(`SELECT count(*)::text FROM roadmap_share_links WHERE id = $1`, link.ID) != "0" {
			t.Fatal("row still present")
		}
		if code, _ := call(edTok, "DELETE", "/"+link.ID, nil); code != 404 {
			t.Fatalf("delete again = %d, want 404", code)
		}
	})

	t.Run("31st request in a minute is limited", func(t *testing.T) {
		const from = "192.0.2.10"
		for i := 1; i <= 30; i++ {
			tok, _ := roadmap.NewToken()
			if rec := publicGet(tok, from); rec.Code != 404 {
				t.Fatalf("request %d = %d, want 404", i, rec.Code)
			}
		}
		tok, _ := roadmap.NewToken()
		rec := publicGet(tok, from)
		if rec.Code != 429 {
			t.Fatalf("31st request = %d, want 429", rec.Code)
		}
		checkHeaders(rec)
		if rec.Header().Get("Retry-After") == "" {
			t.Error("no Retry-After on 429")
		}
		// Another IP is unaffected.
		if rec := publicGet(tok, "192.0.2.200"); rec.Code != 404 {
			t.Fatalf("other IP = %d, want 404", rec.Code)
		}
	})
}
