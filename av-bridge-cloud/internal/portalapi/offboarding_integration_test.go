package portalapi_test

// Integration test for the offboarding check (db/offboarding.go): once a
// customer's offboarding has started, every way in refuses it, through the
// real route wiring (api.NewServer). Runs only when TEST_DATABASE_URL points
// at a disposable Postgres 16:
//
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55440/postgres?sslmode=disable \
//	  go test ./internal/portalapi/ -run TestOffboardingBlocksEveryPath -v

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/dloomes/av-bridge-cloud/internal/api"
	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
	"github.com/dloomes/av-bridge-cloud/internal/bridgecfg"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/ingest"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalapi"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/pubapi"
	"github.com/dloomes/av-bridge-cloud/internal/publicapi"
	"github.com/dloomes/av-bridge-cloud/internal/remote"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestOffboardingBlocksEveryPath(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping offboarding integration test")
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
	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ef", 32))
	if err != nil {
		t.Fatal(err)
	}

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

	// --- Seed: one customer with every kind of credential, plus a vendor.
	tag := randHex(t, 4)
	slug := "offb-" + tag
	tid := "tid-offb-" + tag
	cust := q(`INSERT INTO customers (name, slug, display_name, entra_tenant_id)
	           VALUES ($1, $2, 'Closed Org', $3) RETURNING id::text`, "offboarding-"+tag, slug, tid)
	adminRole := q(`INSERT INTO roles (customer_id, name) VALUES ($1, 'admin') RETURNING id::text`, cust)
	for p := range portalauth.KnownPermissions {
		exec(`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`, adminRole, p)
	}
	const password = "correct horse battery"
	pwHash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	adminEmail := "admin-" + tag + "@offboarding.test"
	admin := q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, $3, 'admin') RETURNING id::text`,
		cust, adminEmail, string(pwHash))
	exec(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, admin, adminRole)

	session := func(userID string) string {
		tok := portalauth.LocalTokenPrefix + randHex(t, 32)
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		return tok
	}
	adminSession := session(admin)

	magic := randHex(t, 32)
	exec(`INSERT INTO magic_link_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
		admin, portalauth.HashToken(magic))

	apiRaw, apiPrefix, apiHash, err := pubapi.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, scopes)
	      VALUES ($1, 'siem', $2, $3, ARRAY['view.dashboard'])`, cust, apiPrefix, apiHash)

	hmacSecret := "secret-" + tag
	enc, err := cipher.Encrypt([]byte(hmacSecret))
	if err != nil {
		t.Fatal(err)
	}
	bridgeID := "offb-col-" + tag
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1, $2, 'c', $3) RETURNING id::text`, cust, bridgeID, enc)
	enrollRaw := randHex(t, 32)
	exec(`INSERT INTO collector_enrollment_tokens (collector_id, token_hash, expires_at)
	      VALUES ($1, $2, now() + interval '1 hour')`, col, portalauth.HashToken(enrollRaw))

	displayRaw := portalauth.DisplayTokenPrefix + randHex(t, 32)
	exec(`INSERT INTO dashboard_display_links (customer_id, name, token_hash, dashboard_ids, created_by)
	      VALUES ($1, 'Wall', $2, ARRAY[gen_random_uuid()], 'test')`, cust, portalauth.HashToken(displayRaw))

	vt := q(`INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id::text`, "vt-offb-"+tag)
	vendor := q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		vt, "vendor-"+tag+"@involve.test")
	vendorSession := session(vendor)

	// --- The real route wiring.
	resolver := portalauth.NewChainResolver(
		portalauth.NewLocalResolver(st.AdminPool()),
		portalauth.NewDisplayResolver(st.AdminPool()),
	)
	bridgeCmds := commands.NewBridgeHandler(st, cipher, 500*time.Millisecond, quiet)
	bridgeCfg := bridgecfg.NewHandler(st, cipher, quiet)
	public := publicapi.NewHandler(st, quiet, notify.SMTPConfig{}, "", false, cipher, "")
	tunnels := remote.NewTunnels(bridgeauth.New(st, cipher, quiet), quiet)
	srv := api.NewServer(":0",
		ingest.NewHandler(st, cipher, nil, nil, quiet),
		nil,
		&api.PortalRoutes{Resolver: resolver, Portal: portalapi.New(st, cipher, nil, nil, nil, quiet)},
		&api.PubAPIRoutes{Resolver: pubapi.NewResolver(st.AdminPool(), quiet), Handler: pubapi.New(st, quiet)},
		api.BridgeCommandRoutes{Poll: bridgeCmds.Poll, Result: bridgeCmds.PostResult, UpdateStatus: bridgeCmds.PostUpdateStatus},
		api.BridgeConfigRoutes{GetConfig: bridgeCfg.Get, PutConfig: bridgeCfg.Put},
		api.PublicRoutes{Branding: public.GetBranding, MagicLinkConsume: public.ConsumeMagicLink, CollectorEnroll: public.EnrollCollector},
		&api.RemoteAccessRoutes{Tunnel: tunnels.Handle},
		quiet)

	type resp struct {
		code int
		body string
	}
	do := func(req *http.Request) resp {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return resp{rec.Code, rec.Body.String()}
	}
	get := func(path, bearer string, hdr ...string) resp {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		return do(req)
	}
	login := func() resp {
		b, _ := json.Marshal(map[string]string{"email": adminEmail, "password": password})
		return do(httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(b)))
	}
	sign := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte(hmacSecret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	bridge := func(method, path string, extra map[string]any) resp {
		t.Helper()
		m := map[string]any{"collector_id": bridgeID}
		for k, v := range extra {
			m[k] = v
		}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("X-Signature", sign(body))
		return do(req)
	}
	tunnel := func() resp {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(hmacSecret))
		fmt.Fprintf(mac, "tunnel:%s:%s", bridgeID, ts)
		req := httptest.NewRequest(http.MethodGet, "/bridge/tunnel", nil)
		req.Header.Set("X-Collector-ID", bridgeID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return do(req)
	}
	branding := func() string {
		var b struct {
			DisplayName string `json:"display_name"`
		}
		r := get("/public/branding?slug="+slug, "")
		_ = json.Unmarshal([]byte(r.body), &b)
		return b.DisplayName
	}
	mockToken := "mock." + base64.RawURLEncoding.EncodeToString([]byte(
		`{"sub":"mock-`+tag+`","email":"m@x.test","tid":"`+tid+`","groups":["g-`+tag+`"]}`))
	exec(`INSERT INTO role_mappings (customer_id, group_id, role) VALUES ($1, $2, 'viewer')`, cust, "g-"+tag)
	mock := portalauth.NewMockJWTResolver(portalauth.NewDBTenantLookup(st.AdminPool()), true)
	display := portalauth.NewDisplayResolver(st.AdminPool())
	browserReq := httptest.NewRequest(http.MethodGet, "/", nil)

	// Sanity: everything works before offboarding.
	if r := get("/api/v1/users", adminSession); r.code != http.StatusOK {
		t.Fatalf("before offboarding: session got %d %s", r.code, r.body)
	}
	if r := get("/pub/v1/ping", apiRaw); r.code != http.StatusOK {
		t.Fatalf("before offboarding: api token got %d %s", r.code, r.body)
	}
	// A fresh resolver per check: the mock resolver caches for 60s.
	if _, ok := portalauth.NewMockJWTResolver(portalauth.NewDBTenantLookup(st.AdminPool()), true).Resolve(mockToken); !ok {
		t.Fatal("before offboarding: mock JWT did not resolve")
	}

	// --- Offboard.
	exec(`UPDATE customers SET offboarding_started_at = now(), offboarding_started_by = 'test',
	             delete_after = now() + interval '30 days' WHERE id = $1`, cust)

	if off, err := st.CustomerOffboarding(ctx, cust); err != nil || !off {
		t.Errorf("CustomerOffboarding = %v, %v; want true", off, err)
	}
	if r := get("/api/v1/users", adminSession); r.code != http.StatusUnauthorized {
		t.Errorf("session: got %d, want 401", r.code)
	}
	if r := login(); r.code != http.StatusForbidden || !strings.Contains(r.body, db.OffboardingMessage) {
		t.Errorf("password login: got %d %s, want 403 with the closed message", r.code, r.body)
	}
	if r := get("/public/magic-link/consume?token="+magic, ""); r.code != http.StatusForbidden || !strings.Contains(r.body, db.OffboardingMessage) {
		t.Errorf("magic link: got %d %s, want 403 with the closed message", r.code, r.body)
	}
	if r := get("/pub/v1/ping", apiRaw); r.code != http.StatusUnauthorized {
		t.Errorf("api token: got %d, want 401", r.code)
	}

	// Every Collector route.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/bridge/poll"},
		{http.MethodPost, "/ingest"},
		{http.MethodPost, "/bridge/commands/00000000-0000-0000-0000-000000000000/result"},
		{http.MethodPost, "/bridge/update-status"},
		{http.MethodPost, "/bridge/config"},
		{http.MethodPut, "/bridge/config"},
	} {
		if r := bridge(c.method, c.path, nil); r.code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d %s, want 401", c.method, c.path, r.code, r.body)
		}
	}
	if r := tunnel(); r.code != http.StatusUnauthorized {
		t.Errorf("GET /bridge/tunnel: got %d %s, want 401", r.code, r.body)
	}

	if name := branding(); name != "" {
		t.Errorf("branding: display_name %q, want the default (empty)", name)
	}

	// Entra: the customer authorize step and both session mints.
	{
		rec := httptest.NewRecorder()
		portalapi.EntraCustomerAuthorize(st)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/entra/customer/authorize?slug="+slug, nil))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), db.OffboardingMessage) {
			t.Errorf("entra customer authorize: got %d %s, want 403 with the closed message", rec.Code, rec.Body.String())
		}
	}
	if _, err := portalapi.EntraCustomerMintSession(ctx, st, admin, browserReq); !errors.Is(err, db.ErrCustomerOffboarding) {
		t.Errorf("entra customer mint: err %v, want ErrCustomerOffboarding", err)
	}
	if _, err := portalapi.EntraVendorMintSession(ctx, st, admin, browserReq); !errors.Is(err, db.ErrCustomerOffboarding) {
		t.Errorf("entra vendor-path mint for a customer user: err %v, want ErrCustomerOffboarding", err)
	}
	if n := q(`SELECT count(*)::text FROM user_sessions WHERE user_id = $1`, admin); n != "1" {
		t.Errorf("customer user has %s sessions, want only the seeded one", n)
	}

	// Display links and the mock-JWT dev sign-in.
	if _, ok := display.Resolve(displayRaw); ok {
		t.Error("display link resolved for an offboarding customer")
	}
	if _, ok := mock.Resolve(mockToken); ok {
		t.Error("mock JWT resolved for an offboarding customer")
	}

	// Vendor users are never blocked, even acting as the closed customer.
	if r := get("/api/v1/users", vendorSession, "X-Customer-Scope", cust); r.code != http.StatusOK {
		t.Errorf("vendor session: got %d %s, want 200", r.code, r.body)
	}
	if _, err := portalapi.EntraVendorMintSession(ctx, st, vendor, browserReq); err != nil {
		t.Errorf("entra vendor mint for a vendor user: %v", err)
	}

	// Collector enrollment (last: a successful redeem rotates the secret).
	{
		b, _ := json.Marshal(map[string]string{"token": enrollRaw})
		r := do(httptest.NewRequest(http.MethodPost, "/public/collectors/enroll", bytes.NewReader(b)))
		if r.code == http.StatusOK {
			t.Errorf("collector enroll: got 200, want refused")
		}
		if used := q(`SELECT (used_at IS NOT NULL)::text FROM collector_enrollment_tokens WHERE collector_id = $1`, col); used != "false" {
			t.Errorf("enrollment token consumed while offboarding")
		}
	}

	// --- Cancel: clear the column and everything works again.
	exec(`UPDATE customers SET offboarding_started_at = NULL, offboarding_started_by = NULL, delete_after = NULL WHERE id = $1`, cust)

	if r := get("/api/v1/users", adminSession); r.code != http.StatusOK {
		t.Errorf("after cancel: session got %d %s, want 200", r.code, r.body)
	}
	if r := get("/pub/v1/ping", apiRaw); r.code != http.StatusOK {
		t.Errorf("after cancel: api token got %d %s, want 200", r.code, r.body)
	}
	if r := login(); r.code != http.StatusOK {
		t.Errorf("after cancel: login got %d %s, want 200", r.code, r.body)
	}
	if r := bridge(http.MethodPost, "/bridge/poll", nil); r.code != http.StatusOK {
		t.Errorf("after cancel: poll got %d %s, want 200", r.code, r.body)
	}
	if name := branding(); name != "Closed Org" {
		t.Errorf("after cancel: branding display_name %q, want Closed Org", name)
	}
	if _, ok := display.Resolve(displayRaw); !ok {
		t.Error("after cancel: display link did not resolve")
	}
}
