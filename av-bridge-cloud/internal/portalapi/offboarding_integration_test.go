package portalapi_test

// Integration test for the offboarding check (db/offboarding.go): once a
// customer's offboarding has started, every way in refuses it, through the
// real route wiring (api.NewServer). Runs only when TEST_DATABASE_URL points
// at a disposable Postgres 16:
//
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55440/postgres?sslmode=disable \
//	  go test ./internal/portalapi/ -run TestOffboardingBlocksEveryPath -v

import (
	"archive/zip"
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

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
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
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
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

// TestHelpdeskOffboardCancelExport drives the helpdesk offboarding
// endpoints end to end: offboard (with the live connections it must end),
// export, deletion certificates, and cancel.
func TestHelpdeskOffboardCancelExport(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping helpdesk offboarding integration test")
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
	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("cd", 32))
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

	// --- Seed a customer with a user, an API token, a Collector, a device,
	// raw and daily telemetry and an open remote-access session.
	tag := randHex(t, 4)
	name := "Offboard Co " + tag
	cust := q(`INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, name)
	adminRole := q(`INSERT INTO roles (customer_id, name) VALUES ($1, 'admin') RETURNING id::text`, cust)
	for p := range portalauth.KnownPermissions {
		exec(`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`, adminRole, p)
	}
	admin := q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		cust, "admin-"+tag+"@offx.test")
	exec(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, admin, adminRole)
	session := func(userID string) string {
		tok := portalauth.LocalTokenPrefix + randHex(t, 32)
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			userID, portalauth.HashToken(tok))
		return tok
	}
	adminSession := session(admin)
	session(admin) // a second browser

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
	bridgeID := "offx-col-" + tag
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1, $2, 'c', $3) RETURNING id::text`, cust, bridgeID, enc)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol, address)
	          VALUES ($1, $2, 'd-1', $3, 'ping', '10.0.0.9') RETURNING id::text`, cust, col, "Lectern "+tag)
	exec(`INSERT INTO telemetry (customer_id, device_id, ts, status) VALUES ($1, $2, now(), 'online')`, cust, dev)
	exec(`INSERT INTO telemetry_daily (customer_id, device_id, day, samples) VALUES ($1, $2, current_date - 1, 96)`, cust, dev)
	rs := q(`INSERT INTO remote_session (customer_id, device_id, user_id, actor, host_label, scheme, port,
	                                     launch_code_hash, cookie_hash, launched_at, expires_at)
	         VALUES ($1, $2, $3, 'admin', $4, 'http', 80, '\x00', '\x01', now(), now() + interval '8 hours')
	         RETURNING id::text`, cust, dev, admin, "s-"+tag)

	vt := q(`INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id::text`, "vt-offx-"+tag)
	vendorEmail := "helpdesk-" + tag + "@involve.test"
	vendor := q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		vt, vendorEmail)
	vendorSession := session(vendor)
	operator := q(`INSERT INTO users (vendor_tenant_id, email, password_hash, role) VALUES ($1, $2, 'x', 'operator') RETURNING id::text`,
		vt, "operator-"+tag+"@involve.test")
	operatorSession := session(operator)

	// --- The real route wiring, served over a socket for the websockets.
	hub := wsfanout.NewHub(quiet)
	tunnels := remote.NewTunnels(bridgeauth.New(st, cipher, quiet), quiet)
	remoteSvc := remote.NewService(st.AdminPool(), tunnels, "", quiet)
	bridgeCmds := commands.NewBridgeHandler(st, cipher, 500*time.Millisecond, quiet)
	bridgeCfg := bridgecfg.NewHandler(st, cipher, quiet)
	public := publicapi.NewHandler(st, quiet, notify.SMTPConfig{}, "", false, cipher, "")
	srv := api.NewServer(":0",
		ingest.NewHandler(st, cipher, nil, nil, quiet),
		nil,
		&api.PortalRoutes{
			Resolver: portalauth.NewChainResolver(portalauth.NewLocalResolver(st.AdminPool())),
			Portal:   portalapi.New(st, cipher, nil, nil, nil, quiet).SetRemoteAccess(remoteSvc).SetLiveEvents(hub),
			WSHub:    hub,
		},
		&api.PubAPIRoutes{Resolver: pubapi.NewResolver(st.AdminPool(), quiet), Handler: pubapi.New(st, quiet)},
		api.BridgeCommandRoutes{Poll: bridgeCmds.Poll, Result: bridgeCmds.PostResult, UpdateStatus: bridgeCmds.PostUpdateStatus},
		api.BridgeConfigRoutes{GetConfig: bridgeCfg.Get, PutConfig: bridgeCfg.Put},
		api.PublicRoutes{Branding: public.GetBranding, MagicLinkConsume: public.ConsumeMagicLink, CollectorEnroll: public.EnrollCollector},
		&api.RemoteAccessRoutes{Tunnel: tunnels.Handle},
		quiet)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()
	wsBase := "ws://" + strings.TrimPrefix(ts.URL, "http://")

	type resp struct {
		code int
		body []byte
		hdr  http.Header
	}
	call := func(method, path, bearer string, body any) resp {
		t.Helper()
		var rdr io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return resp{rec.Code, rec.Body.Bytes(), rec.Header()}
	}
	base := "/api/v1/helpdesk/customers/" + cust

	// A non-admin vendor is refused everywhere.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, base + "/offboard"},
		{http.MethodPost, base + "/cancel-offboarding"},
		{http.MethodGet, base + "/export.zip"},
		{http.MethodGet, "/api/v1/helpdesk/deletions"},
	} {
		if r := call(c.method, c.path, operatorSession, map[string]string{"confirm": name}); r.code != http.StatusForbidden {
			t.Errorf("operator %s %s: got %d %s, want 403", c.method, c.path, r.code, r.body)
		}
	}

	// Live connections that offboarding must end: a /ws/events subscriber
	// and the Collector's remote-access tunnel.
	wsHdr := http.Header{}
	wsHdr.Set("Authorization", "Bearer "+adminSession)
	events, _, err := websocket.DefaultDialer.Dial(wsBase+"/ws/events", wsHdr)
	if err != nil {
		t.Fatalf("ws/events dial: %v", err)
	}
	defer events.Close()
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(hmacSecret))
	fmt.Fprintf(mac, "tunnel:%s:%s", bridgeID, tsStr)
	tunHdr := http.Header{}
	tunHdr.Set("X-Collector-ID", bridgeID)
	tunHdr.Set("X-Timestamp", tsStr)
	tunHdr.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	tunWS, _, err := websocket.DefaultDialer.Dial(wsBase+"/bridge/tunnel", tunHdr)
	if err != nil {
		t.Fatalf("tunnel dial: %v", err)
	}
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	ysess, err := yamux.Server(remote.NewWSConn(tunWS), ycfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ysess.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !tunnels.Connected(col) {
		if time.Now().After(deadline) {
			t.Fatal("tunnel never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A webhook channel created and edited through the real handlers, so its
	// URLs are recorded in audit_log before/after.
	auditURLs := []string{"AUDITCREATE" + tag, "AUDITUPDATE" + tag}
	{
		r := call(http.MethodPost, "/api/v1/notifications/channels", adminSession, map[string]any{
			"name": "audited hook", "type": "webhook", "target": "https://hooks.example.com/services/" + auditURLs[0],
		})
		if r.code != http.StatusCreated {
			t.Fatalf("create channel: got %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r.body, &created)
		r = call(http.MethodPatch, "/api/v1/notifications/channels/"+created.ID, adminSession, map[string]any{
			"name": "audited hook", "target": "https://hooks.example.com/services/" + auditURLs[1],
		})
		if r.code/100 != 2 {
			t.Fatalf("update channel: got %d %s", r.code, r.body)
		}
		if n := q(`SELECT count(*)::text FROM audit_log WHERE customer_id = $1 AND target_kind = 'notification_channel'
		             AND ("after"::text LIKE '%' || $2 || '%' OR "after"::text LIKE '%' || $3 || '%')`,
			cust, auditURLs[0], auditURLs[1]); n != "2" {
			t.Fatalf("channel audit entries holding the URLs = %s, want 2", n)
		}
	}
	secretInZip := func(what string, body []byte, secrets []string) {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		var all bytes.Buffer
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			all.Write(b)
		}
		if !bytes.Contains(all.Bytes(), []byte("hooks.example.com/[redacted]")) {
			t.Errorf("%s: no masked channel URL found", what)
		}
		for _, s := range secrets {
			if bytes.Contains(body, []byte(s)) || bytes.Contains(all.Bytes(), []byte(s)) {
				t.Errorf("%s contains %q", what, s)
			}
		}
	}

	// --- Offboard: wrong name, then right name, then again.
	if r := call(http.MethodPost, base+"/offboard", vendorSession, map[string]string{"confirm": "not the name"}); r.code != http.StatusBadRequest ||
		!strings.Contains(string(r.body), "confirm must match the customer name") {
		t.Fatalf("offboard with wrong name: got %d %s, want 400", r.code, r.body)
	}
	if n := q(`SELECT count(*)::text FROM customers WHERE id = $1 AND offboarding_started_at IS NOT NULL`, cust); n != "0" {
		t.Fatal("wrong-name offboard changed the customer")
	}
	r := call(http.MethodPost, base+"/offboard", vendorSession, map[string]string{"confirm": name})
	if r.code != http.StatusOK {
		t.Fatalf("offboard: got %d %s, want 200", r.code, r.body)
	}
	var off struct {
		OffboardingStartedAt time.Time `json:"offboarding_started_at"`
		DeleteAfter          time.Time `json:"delete_after"`
	}
	if err := json.Unmarshal(r.body, &off); err != nil {
		t.Fatalf("offboard body %s: %v", r.body, err)
	}
	if d := time.Until(off.DeleteAfter) - 30*24*time.Hour; d < -time.Minute || d > time.Minute {
		t.Errorf("delete_after = %v, want about now + 30 days", off.DeleteAfter)
	}
	if v := q(`SELECT (abs(extract(epoch FROM delete_after - (now() + interval '30 days'))) < 60)::text
	             FROM customers WHERE id = $1`, cust); v != "true" {
		t.Errorf("stored delete_after not now + 30 days")
	}
	if n := q(`SELECT count(*)::text FROM user_sessions WHERE user_id = $1 AND revoked_at IS NULL`, admin); n != "0" {
		t.Errorf("%s of the customer's sessions still live, want 0", n)
	}
	if n := q(`SELECT count(*)::text FROM user_sessions WHERE user_id = $1 AND revoked_at IS NULL`, vendor); n == "0" {
		t.Error("offboarding revoked the vendor's own session")
	}
	if got := q(`SELECT customer_name || '|' || started_by || '|' || (cancelled_at IS NULL)::text || '|' || jsonb_array_length(downloads)::text
	               FROM customer_deletions WHERE customer_id = $1`, cust); got != name+"|"+vendorEmail+"|true|0" {
		t.Errorf("deletion row = %q", got)
	}
	if got := q(`SELECT actor FROM audit_log WHERE customer_id = $1 AND action = 'customer.offboard'`, cust); got != "user:"+vendor {
		t.Errorf("customer.offboard audit actor = %q, want user:%s", got, vendor)
	}

	// The live connections end.
	if got := q(`SELECT COALESCE(end_reason, '') FROM remote_session WHERE id = $1 AND ended_at IS NOT NULL
	             UNION ALL SELECT '' LIMIT 1`, rs); got == "" {
		t.Error("remote-access session not ended")
	}
	_ = events.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := events.ReadMessage(); err == nil {
		t.Error("ws/events still delivering after offboarding")
	} else if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		t.Error("ws/events not closed by offboarding (read timed out)")
	}
	select {
	case <-ysess.CloseChan():
	case <-time.After(5 * time.Second):
		t.Error("Collector tunnel not dropped by offboarding")
	}

	if r := call(http.MethodPost, base+"/offboard", vendorSession, map[string]string{"confirm": name}); r.code != http.StatusConflict {
		t.Errorf("second offboard: got %d %s, want 409", r.code, r.body)
	}

	// The helpdesk list shows the countdown.
	{
		r := call(http.MethodGet, "/api/v1/helpdesk/customers", vendorSession, nil)
		var list []struct {
			ID                   string     `json:"id"`
			OffboardingStartedAt *time.Time `json:"offboarding_started_at"`
			DeleteAfter          *time.Time `json:"delete_after"`
		}
		if err := json.Unmarshal(r.body, &list); err != nil {
			t.Fatalf("customer list %d %s: %v", r.code, r.body, err)
		}
		found := false
		for _, c := range list {
			if c.ID == cust {
				found = true
				if c.OffboardingStartedAt == nil || c.DeleteAfter == nil {
					t.Errorf("customer list item missing offboarding fields: %+v", c)
				}
			}
		}
		if !found {
			t.Error("customer missing from helpdesk list")
		}
	}

	// Only one open deletion row per customer (migration 0076).
	if _, err := su.Exec(ctx, `INSERT INTO customer_deletions (customer_id, customer_name, started_at, started_by)
	                           VALUES ($1, 'dup', now(), 'x')`, cust); err == nil {
		t.Error("a second open customer_deletions row was accepted")
	}
	// Malformed ids are 404 before any lookup.
	for _, path := range []string{
		"/api/v1/helpdesk/customers/not-a-uuid/offboard",
		"/api/v1/helpdesk/customers/not-a-uuid/cancel-offboarding",
		"/api/v1/helpdesk/customers/not-a-uuid/export.zip",
		"/api/v1/helpdesk/deletions/not-a-uuid.csv",
	} {
		method := http.MethodPost
		if strings.HasSuffix(path, ".zip") || strings.HasSuffix(path, ".csv") {
			method = http.MethodGet
		}
		if r := call(method, path, vendorSession, map[string]string{"confirm": name}); r.code != http.StatusNotFound {
			t.Errorf("%s %s: got %d %s, want 404", method, path, r.code, r.body)
		}
	}

	// Secrets the export must not carry: a ServiceNow password, webhook
	// headers and URL, a Teams URL.
	secretStrs := []string{"SNPW" + tag, "WHTOKEN" + tag, "WHURL" + tag, "TEAMS" + tag, "NESTED" + tag}
	exec(`INSERT INTO notification_channels (customer_id, name, type, target, config) VALUES
		($1, 'sn', 'servicenow', 'https://acme.service-now.com', $2::jsonb),
		($1, 'hook', 'webhook', $3, $4::jsonb),
		($1, 'teams', 'teams', $5, NULL),
		($1, 'mail', 'email', 'ops@offx.test', NULL)`, cust,
		`{"username":"marcus","password_enc":"`+secretStrs[0]+`","extra":[{"Api_Key":"`+secretStrs[4]+`"}]}`,
		"https://hooks.example.com/services/"+secretStrs[2],
		`{"headers":{"Authorization":"Bearer `+secretStrs[1]+`"}}`,
		"https://acme.webhook.office.com/webhookb2/"+secretStrs[3])

	// A second customer whose rows must not appear in this export.
	other := q(`INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, "Other Co "+tag)
	otherUser := q(`INSERT INTO users (customer_id, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id::text`,
		other, "other-"+tag+"@other.test")
	otherCol := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	               VALUES ($1, $2, 'oc', '\x00') RETURNING id::text`, other, "other-col-"+tag)
	otherDev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol)
	               VALUES ($1, $2, 'od', 'Other device', 'ping') RETURNING id::text`, other, otherCol)
	exec(`INSERT INTO telemetry_daily (customer_id, device_id, day, samples) VALUES ($1, $2, current_date - 1, 5)`, other, otherDev)
	otherChan := q(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, 'm', 'email', 'x@other.test') RETURNING id::text`, other)
	otherAudit := q(`INSERT INTO audit_log (customer_id, actor, action, target_kind) VALUES ($1, 'test', 'other.thing', 'customer') RETURNING id::text`, other)
	otherIDs := []string{other, otherUser, otherCol, otherDev, otherChan, otherAudit}

	// --- Export.
	{
		r := call(http.MethodGet, base+"/export.zip", vendorSession, nil)
		if r.code != http.StatusOK || r.hdr.Get("Content-Type") != "application/zip" {
			t.Fatalf("export: got %d %s %s", r.code, r.hdr.Get("Content-Type"), r.body)
		}
		if cl := r.hdr.Get("Content-Length"); cl != strconv.Itoa(len(r.body)) {
			t.Errorf("Content-Length %s, body %d bytes", cl, len(r.body))
		}
		zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		var all strings.Builder
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			files[f.Name] = string(b)
			all.Write(b)
		}
		for _, s := range secretStrs {
			if bytes.Contains(r.body, []byte(s)) || strings.Contains(all.String(), s) {
				t.Errorf("export contains the secret %q", s)
			}
		}
		secretInZip("customer export", r.body, auditURLs)
		if !strings.Contains(files["audit_log.csv"], "notification_channel.update") {
			t.Error("audit_log.csv lacks the channel audit entries")
		}

		// The admin's access export holds those audit entries too (they
		// are the actor).
		ar := call(http.MethodPost, "/api/v1/helpdesk/privacy/access-export", vendorSession, map[string]any{
			"customer_id": cust, "email": "admin-" + tag + "@offx.test",
		})
		if ar.code != http.StatusOK {
			t.Fatalf("access export: got %d %s", ar.code, ar.body)
		}
		secretInZip("access export", ar.body, auditURLs)
		for _, want := range []string{"https://hooks.example.com/[redacted]", "https://acme.webhook.office.com/[redacted]",
			"https://acme.service-now.com", "ops@offx.test", "marcus"} {
			if !strings.Contains(files["notification_channels.csv"], want) {
				t.Errorf("notification_channels.csv lacks %q:\n%s", want, files["notification_channels.csv"])
			}
		}
		if !strings.Contains(files["README.txt"], "notification_channels.target for Teams and webhook channels") {
			t.Error("README does not describe the redaction")
		}
		for _, id := range otherIDs {
			if strings.Contains(all.String(), id) {
				t.Errorf("export contains the other customer's id %s", id)
			}
		}
		for _, want := range []string{"devices.csv", "devices.json", "telemetry_daily.csv", "customers.csv", "README.txt"} {
			if _, ok := files[want]; !ok {
				t.Errorf("export missing %s", want)
			}
		}
		for _, bad := range []string{"telemetry.csv", "telemetry.json", "customer_deletions.csv"} {
			if _, ok := files[bad]; ok {
				t.Errorf("export holds %s", bad)
			}
		}
		if !strings.Contains(files["devices.csv"], "Lectern "+tag) {
			t.Errorf("devices.csv lacks the seeded device:\n%s", files["devices.csv"])
		}
		if strings.Contains(files["collectors.csv"], "hmac_secret_enc") {
			t.Error("export holds the Collector secret column")
		}
		if !strings.Contains(files["README.txt"], name) {
			t.Error("README does not name the customer")
		}
		if got := q(`SELECT jsonb_array_length(downloads)::text || '|' || (downloads->0->>'by')
		               FROM customer_deletions WHERE customer_id = $1`, cust); got != "1|user:"+vendor {
			t.Errorf("downloads = %q, want one entry by user:%s", got, vendor)
		}
	}

	// Overlapping exports both succeed and are both recorded.
	{
		codes := make(chan int, 3)
		for i := 0; i < 3; i++ {
			go func() {
				req, _ := http.NewRequest(http.MethodGet, ts.URL+base+"/export.zip", nil)
				req.Header.Set("Authorization", "Bearer "+vendorSession)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					codes <- 0
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				codes <- res.StatusCode
			}()
		}
		for i := 0; i < 3; i++ {
			if c := <-codes; c != http.StatusOK {
				t.Errorf("concurrent export: got %d, want 200", c)
			}
		}
		if n := q(`SELECT jsonb_array_length(downloads)::text FROM customer_deletions WHERE customer_id = $1`, cust); n != "4" {
			t.Errorf("downloads after 3 concurrent exports = %s, want 4", n)
		}
	}

	// --- Deletion certificates.
	delID := q(`SELECT id::text FROM customer_deletions WHERE customer_id = $1`, cust)
	{
		r := call(http.MethodGet, "/api/v1/helpdesk/deletions", vendorSession, nil)
		if r.code != http.StatusOK || !strings.Contains(string(r.body), delID) {
			t.Errorf("deletions list: got %d, want 200 including %s", r.code, delID)
		}
		r = call(http.MethodGet, "/api/v1/helpdesk/deletions/"+delID+".csv", vendorSession, nil)
		if r.code != http.StatusOK || !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/csv") ||
			!strings.Contains(string(r.body), "table,rows") || !strings.Contains(string(r.body), name) {
			t.Errorf("deletion certificate: got %d %s\n%s", r.code, r.hdr.Get("Content-Type"), r.body)
		}
		if r := call(http.MethodGet, "/api/v1/helpdesk/deletions/00000000-0000-0000-0000-000000000000.csv", vendorSession, nil); r.code != http.StatusNotFound {
			t.Errorf("unknown certificate: got %d, want 404", r.code)
		}
	}

	// --- Cancel.
	if r := call(http.MethodGet, "/pub/v1/ping", apiRaw, nil); r.code != http.StatusUnauthorized {
		t.Errorf("api token while offboarding: got %d, want 401", r.code)
	}
	if r := call(http.MethodPost, base+"/cancel-offboarding", vendorSession, nil); r.code != http.StatusOK {
		t.Fatalf("cancel: got %d %s, want 200", r.code, r.body)
	}
	if r := call(http.MethodPost, base+"/cancel-offboarding", vendorSession, nil); r.code != http.StatusConflict {
		t.Errorf("second cancel: got %d %s, want 409", r.code, r.body)
	}
	if r := call(http.MethodGet, "/pub/v1/ping", apiRaw, nil); r.code != http.StatusOK {
		t.Errorf("api token after cancel: got %d %s, want 200", r.code, r.body)
	}
	if got := q(`SELECT (offboarding_started_at IS NULL AND offboarding_started_by IS NULL AND delete_after IS NULL)::text
	               FROM customers WHERE id = $1`, cust); got != "true" {
		t.Error("cancel left the offboarding columns set")
	}
	if got := q(`SELECT cancelled_by FROM customer_deletions WHERE customer_id = $1`, cust); got != vendorEmail {
		t.Errorf("cancelled_by = %q, want %s", got, vendorEmail)
	}
	if got := q(`SELECT actor FROM audit_log WHERE customer_id = $1 AND action = 'customer.offboard_cancel'`, cust); got != "user:"+vendor {
		t.Errorf("customer.offboard_cancel audit actor = %q", got)
	}
}
