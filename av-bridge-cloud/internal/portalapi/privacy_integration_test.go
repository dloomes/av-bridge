package portalapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
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

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestAccessExportHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
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

	cust := q(`INSERT INTO customers (name, entra_tenant_id) VALUES ('export-test', gen_random_uuid()) RETURNING id::text`)
	adminID := q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, 'export.admin@example.test', 'x') RETURNING id::text`, cust)
	subject := "Subject.Person@Example.test"
	q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`, cust, subject)

	adminP := portalauth.Principal{UserID: adminID, Email: "export.admin@example.test", CustomerID: cust,
		Permissions: map[string]struct{}{portalauth.PermPrivacyManage: {}}}
	viewerP := portalauth.Principal{UserID: adminID, CustomerID: cust,
		Permissions: map[string]struct{}{"view.users": {}}}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.Handle("POST /export", portalauth.RequirePermission(portalauth.PermPrivacyManage)(http.HandlerFunc(h.AccessExport)))
	call := func(p portalauth.Principal, email string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest("POST", "/export", bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := call(adminP, subject)
	if rec.Code != 200 {
		t.Fatalf("admin export: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="access-request-`) || !strings.HasSuffix(cd, `.zip"`) {
		t.Errorf("content-disposition %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control %q", rec.Header().Get("Cache-Control"))
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("zip: %v (%d files)", err, len(zr.File))
	}

	if rec := call(viewerP, subject); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", rec.Code)
	}
	if rec := call(adminP, "nobody.here@example.test"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown email: %d %s, want 404", rec.Code, rec.Body.String())
	}

	sum := sha256.Sum256([]byte(strings.ToLower(subject)))
	var n int
	var row string
	var meta string
	err = su.QueryRow(ctx, `SELECT count(*) OVER (), a::text, metadata->>'email_sha256' FROM audit_log a
		WHERE customer_id = $1 AND action = 'privacy.access_export'`, cust).Scan(&n, &row, &meta)
	if err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if meta != hex.EncodeToString(sum[:]) {
		t.Errorf("email_sha256 = %q", meta)
	}
	if strings.Contains(row, "@") {
		t.Errorf("audit row contains an email: %s", row)
	}
}

// privacyEnv is the shared setup for the HTTP tests below: migrated database,
// a superuser connection for seeding and a Store as app_admin/app_tenant.
type privacyEnv struct {
	ctx  context.Context
	su   *pgx.Conn
	st   *db.Store
	t    *testing.T
	logs *bytes.Buffer
	h    *Handler
}

func newPrivacyEnv(t *testing.T) *privacyEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
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
	logs := &bytes.Buffer{}
	h := New(st, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(logs, nil)))
	return &privacyEnv{ctx: ctx, su: su, st: st, t: t, logs: logs, h: h}
}

func (e *privacyEnv) q(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.su.QueryRow(e.ctx, sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *privacyEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.su.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// customer makes a customer with the system-default roles.
func (e *privacyEnv) customer(name string) string {
	e.t.Helper()
	res, err := e.st.CreateCustomer(e.ctx, db.CreateCustomerOptions{Name: name})
	if err != nil {
		e.t.Fatalf("create customer: %v", err)
	}
	return res.CustomerID
}

// vendorTenant makes sure the fixed vendor tenant the helpdesk handlers use exists.
func (e *privacyEnv) vendorTenant() string {
	e.t.Helper()
	vt := db.PocVendorTenantUUID()
	if _, err := e.su.Exec(e.ctx, `INSERT INTO vendor_tenants (id, name, entra_tenant_id) VALUES ($1, 'Involve', $2)
		ON CONFLICT DO NOTHING`, vt, db.PocVendorTenantID); err != nil {
		e.t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM vendor_tenants WHERE id = $1`, vt) != 1 {
		e.t.Fatal("fixed vendor tenant missing")
	}
	return vt
}

// Carried from the Task 5 review: the helpdesk access-export route.
func TestHelpdeskAccessExportHTTP(t *testing.T) {
	e := newPrivacyEnv(t)
	cust := e.customer("helpdesk-export")
	vt := e.vendorTenant()
	e.q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, 'Pat.Person@Example.test', 'x') RETURNING id::text`, cust)
	staff := "hd-export-" + cust[:8] + "@involve.example"
	e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`, vt, staff)

	mux := http.NewServeMux()
	mux.Handle("POST /hd", portalauth.RequireVendorAdmin(http.HandlerFunc(e.h.HelpdeskAccessExport)))
	call := func(p portalauth.Principal, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/hd", bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	vendor := func() portalauth.Principal {
		return portalauth.Principal{UserID: e.q(`SELECT gen_random_uuid()::text`), Role: "admin", IsVendor: true}
	}

	custAdmin := portalauth.Principal{UserID: e.q(`SELECT gen_random_uuid()::text`), CustomerID: cust, Role: "admin",
		Permissions: map[string]struct{}{portalauth.PermPrivacyManage: {}}}
	if rec := call(custAdmin, map[string]any{"customer_id": cust, "email": "pat.person@example.test"}); rec.Code != http.StatusForbidden {
		t.Errorf("non-vendor: %d, want 403", rec.Code)
	}
	v := vendor()
	if rec := call(v, map[string]any{"customer_id": cust, "vendor": true, "email": "pat.person@example.test"}); rec.Code != http.StatusBadRequest {
		t.Errorf("both: %d, want 400", rec.Code)
	}
	if rec := call(v, map[string]any{"email": "pat.person@example.test"}); rec.Code != http.StatusBadRequest {
		t.Errorf("neither: %d, want 400", rec.Code)
	}
	if rec := call(v, map[string]any{"customer_id": "00000000-0000-0000-0000-00000000dead", "email": "pat.person@example.test"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown customer: %d, want 404", rec.Code)
	}

	rec := call(v, map[string]any{"customer_id": strings.ToUpper(cust), "email": "PAT.person@example.test"})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("customer export: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE customer_id = $1 AND action = 'privacy.access_export' AND actor = $2`,
		cust, "user:"+v.UserID); n != 1 {
		t.Errorf("customer export audit rows = %d, want 1", n)
	}

	v2 := vendor()
	rec = call(v2, map[string]any{"vendor": true, "email": strings.ToUpper(staff)})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("vendor export: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE actor = $1`, "user:"+v2.UserID); n != 0 {
		t.Errorf("vendor-staff export wrote %d audit_log rows, want 0", n)
	}
	if !strings.Contains(e.logs.String(), v2.UserID) || strings.Contains(e.logs.String(), "@") {
		t.Errorf("vendor-staff export log line missing or carries an email: %s", e.logs.String())
	}
}

func TestPrivacyEraseHTTP(t *testing.T) {
	e := newPrivacyEnv(t)
	cust := e.customer("erase-http")
	adminRole := e.q(`SELECT id::text FROM roles WHERE customer_id = $1 AND name = 'admin' AND is_system_default`, cust)
	officerRole := e.q(`INSERT INTO roles (customer_id, name) VALUES ($1, 'privacy officer') RETURNING id::text`, cust)
	e.q(`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) RETURNING permission`, officerRole, portalauth.PermPrivacyManage)
	user := func(email, role string) string {
		id := e.q(`INSERT INTO users (customer_id, email, full_name, password_hash) VALUES ($1, $2, 'Some One', 'x') RETURNING id::text`, cust, email)
		e.q(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) RETURNING role_id::text`, id, role)
		return id
	}
	adminID := user("only.admin@example.test", adminRole)
	officerID := user("privacy.officer@example.test", officerRole)
	subjectID := user("Leaver.Person@Example.test", officerRole)
	if _, err := e.su.Exec(e.ctx, `INSERT INTO audit_log (customer_id, actor, action, target_kind) VALUES ($1, 'leaver.person@example.test', 't', 'device')`, cust); err != nil {
		t.Fatal(err)
	}

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	adminP := portalauth.Principal{UserID: adminID, CustomerID: cust, Role: "admin", Permissions: all}
	officerP := portalauth.Principal{UserID: officerID, CustomerID: cust, Role: "privacy officer",
		Permissions: map[string]struct{}{portalauth.PermPrivacyManage: {}}}

	mux := http.NewServeMux()
	mux.Handle("POST /erase", portalauth.RequirePermission(portalauth.PermPrivacyManage)(http.HandlerFunc(e.h.PrivacyErase)))
	mux.Handle("POST /hd/erase", portalauth.RequireVendorAdmin(http.HandlerFunc(e.h.HelpdeskPrivacyErase)))
	call := func(path string, p portalauth.Principal, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	erase := func(p portalauth.Principal, email, confirm string, dry bool) *httptest.ResponseRecorder {
		return call("/erase", p, map[string]any{"email": email, "confirm": confirm, "dry_run": dry})
	}

	if rec := erase(officerP, "privacy.officer@example.test", "privacy.officer@example.test", false); rec.Code != http.StatusBadRequest {
		t.Errorf("self: %d %s, want 400", rec.Code, rec.Body.String())
	}
	// The organisation's only admin holds permissions the officer lacks: the user-management guard refuses.
	if rec := erase(officerP, "only.admin@example.test", "only.admin@example.test", false); rec.Code != http.StatusForbidden {
		t.Errorf("officer erasing the admin: %d %s, want 403", rec.Code, rec.Body.String())
	}
	// Vendor staff pass that guard, so the last-admin guard is what stops them.
	vendorP := portalauth.Principal{UserID: e.q(`SELECT gen_random_uuid()::text`), Role: "admin", IsVendor: true}
	if rec := call("/hd/erase", vendorP, map[string]any{"customer_id": cust, "email": "only.admin@example.test", "confirm": "only.admin@example.test"}); rec.Code != http.StatusConflict {
		t.Errorf("vendor erasing the last admin: %d %s, want 409", rec.Code, rec.Body.String())
	}
	rec := erase(adminP, "leaver.person@example.test", "someone.else@example.test", false)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirm must match email") {
		t.Errorf("mismatched confirm: %d %s", rec.Code, rec.Body.String())
	}
	if rec := erase(adminP, "nobody.here@example.test", "nobody.here@example.test", false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown email: %d %s, want 404", rec.Code, rec.Body.String())
	}

	// Dry run: preview counts, nothing changes, no audit entry.
	rec = erase(adminP, "Leaver.Person@example.test", "leaver.person@example.test", true)
	if rec.Code != 200 {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	var dry struct {
		StandIn string           `json:"stand_in"`
		Counts  map[string]int64 `json:"counts"`
		Actions []string         `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	if dry.Counts["users"] != 1 || dry.Counts["audit_log"] != 1 {
		t.Errorf("dry run counts = %v", dry.Counts)
	}
	if e.count(`SELECT count(*) FROM users WHERE id = $1`, subjectID) != 1 {
		t.Fatal("dry run deleted the user")
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE customer_id = $1 AND action = 'privacy.erase'`, cust); n != 0 {
		t.Errorf("dry run wrote %d privacy.erase rows", n)
	}

	rec = erase(adminP, "Leaver.Person@example.test", "leaver.person@example.test", false)
	if rec.Code != 200 {
		t.Fatalf("erase: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		StandIn string           `json:"stand_in"`
		Counts  map[string]int64 `json:"counts"`
		Actions []string         `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "erased-user-" + strings.ReplaceAll(subjectID, "-", "")[:8]; got.StandIn != want || dry.StandIn != want {
		t.Errorf("stand-in = %q (dry %q), want %q", got.StandIn, dry.StandIn, want)
	}
	if e.count(`SELECT count(*) FROM users WHERE id = $1`, subjectID) != 0 {
		t.Error("user not deleted")
	}
	var n int
	var row, target string
	if err := e.su.QueryRow(e.ctx, `SELECT count(*) OVER (), a::text, a.target_id FROM audit_log a
		WHERE customer_id = $1 AND action = 'privacy.erase'`, cust).Scan(&n, &row, &target); err != nil {
		t.Fatalf("privacy.erase row: %v", err)
	}
	if n != 1 {
		t.Errorf("privacy.erase rows = %d, want 1", n)
	}
	if strings.Contains(row, "@") {
		t.Errorf("privacy.erase row carries an email: %s", row)
	}
	if target != got.StandIn || !strings.Contains(row, `"counts"`) || !strings.Contains(row, "user:"+adminID) {
		t.Errorf("privacy.erase row = %s", row)
	}
}

// The audit entries an administrator's own privacy actions leave carry the
// actor 'user:<uuid>', not an email. When that administrator is later the
// subject, the access export must include those entries and erasure must
// anonymise them.
func TestPrivacyActorIDEntriesExportedAndErased(t *testing.T) {
	e := newPrivacyEnv(t)
	cust := e.customer("erase-actor-ids")
	adminRole := e.q(`SELECT id::text FROM roles WHERE customer_id = $1 AND name = 'admin' AND is_system_default`, cust)
	user := func(email string) string {
		id := e.q(`INSERT INTO users (customer_id, email, full_name, password_hash) VALUES ($1, $2, 'Some One', 'x') RETURNING id::text`, cust, email)
		e.q(`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) RETURNING role_id::text`, id, adminRole)
		return id
	}
	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	tag := cust[:8]
	firstEmail, secondEmail, leaverEmail := "first-"+tag+"@example.test", "second-"+tag+"@example.test", "leaver-"+tag+"@example.test"
	first := portalauth.Principal{UserID: user(firstEmail), CustomerID: cust, Role: "admin", Permissions: all}
	second := portalauth.Principal{UserID: user(secondEmail), CustomerID: cust, Role: "admin", Permissions: all}
	user(leaverEmail)

	mux := http.NewServeMux()
	mux.Handle("POST /export", portalauth.RequirePermission(portalauth.PermPrivacyManage)(http.HandlerFunc(e.h.AccessExport)))
	mux.Handle("POST /erase", portalauth.RequirePermission(portalauth.PermPrivacyManage)(http.HandlerFunc(e.h.PrivacyErase)))
	call := func(path string, p portalauth.Principal, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s as %s: %d %s", path, p.UserID, rec.Code, rec.Body.String())
		}
		return rec
	}

	// The first administrator exports and erases the leaver.
	call("/export", first, map[string]any{"email": leaverEmail})
	call("/erase", first, map[string]any{"email": leaverEmail, "confirm": leaverEmail})
	actor := "user:" + first.UserID
	if n := e.count(`SELECT count(*) FROM audit_log WHERE customer_id = $1 AND actor = $2
		AND action IN ('privacy.access_export', 'privacy.erase')`, cust, actor); n != 2 {
		t.Fatalf("first admin's privacy entries = %d, want 2", n)
	}

	// The second exports the first: those entries are in the archive.
	rec := call("/export", second, map[string]any{"email": firstEmail})
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var auditCSV string
	for _, f := range zr.File {
		if f.Name == "audit_log.csv" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			auditCSV = string(b)
		}
	}
	for _, action := range []string{"privacy.access_export", "privacy.erase"} {
		if !strings.Contains(auditCSV, action) {
			t.Errorf("access export audit_log.csv lacks the %s entry:\n%s", action, auditCSV)
		}
	}

	// Then erases the first: no entry names the account id any more.
	rec = call("/erase", second, map[string]any{"email": firstEmail, "confirm": firstEmail})
	var res struct {
		StandIn string `json:"stand_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log a WHERE customer_id = $1 AND a::text ILIKE '%' || $2 || '%'`, cust, actor); n != 0 {
		t.Errorf("%d audit entries still hold %s", n, actor)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE customer_id = $1 AND actor = $2
		AND action IN ('privacy.access_export', 'privacy.erase')`, cust, res.StandIn); n != 2 {
		t.Errorf("privacy entries with the stand-in %s as actor = %d, want 2", res.StandIn, n)
	}
}

// The spec's "helpdesk user deletion writes a user.delete audit entry".
// Helpdesk users belong to the vendor tenant, not a customer, and
// audit_log.customer_id is NOT NULL, so the entry goes to the structured log
// (as vendor-staff privacy actions do), with ids only.
func TestHelpdeskDeleteUserAudit(t *testing.T) {
	e := newPrivacyEnv(t)
	vt := e.vendorTenant()
	victim := e.q(`INSERT INTO users (vendor_tenant_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`,
		vt, "hd-delete-"+e.q(`SELECT substr(md5(random()::text), 1, 8)`)+"@involve.example")
	actor := portalauth.Principal{UserID: e.q(`SELECT gen_random_uuid()::text`), Role: "admin", IsVendor: true}

	mux := http.NewServeMux()
	mux.Handle("DELETE /hd/users/{id}", portalauth.RequireVendorAdmin(http.HandlerFunc(e.h.HelpdeskDeleteUser)))
	req := httptest.NewRequest("DELETE", "/hd/users/"+victim, nil)
	req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), actor))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(e.logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m["action"] == "user.delete" && m["target_id"] == victim && m["actor"] == "user:"+actor.UserID {
			found = true
			if strings.Contains(line, "@") {
				t.Errorf("user.delete log line carries an email: %s", line)
			}
		}
	}
	if !found {
		t.Errorf("no user.delete entry logged: %s", e.logs.String())
	}
}
