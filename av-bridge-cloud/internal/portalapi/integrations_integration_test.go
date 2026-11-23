package portalapi

// Integration tests for the integrations API (Poly Lens connect, account
// rules, device picker). Runs only when TEST_DATABASE_URL points at a
// disposable Postgres. A fake Lens serves the token and GraphQL endpoints.

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
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

const fakeLensSecret = "fake-secret-value-123"

func newFakeLens(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Secret string `json:"client_secret"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b.Secret == "bad" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "tenants") {
			io.WriteString(w, `{"data":{"tenants":[{"id":"acct-a","name":"Acme Test"},{"id":"acct-b","name":"Other Test Co"}]}}`)
			return
		}
		if req.Variables["tenantId"] != "acct-a" {
			io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[]}}}}}`)
			return
		}
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[
{"node":{"id":"ext-linked","name":"Linked Room","serialNumber":"SN-LINKED","macAddress":"AA:00:00:00:00:01","hardwareModel":"Test X","connected":true,"room":{"name":"Room L"},"site":{"name":"Site A"},"activeApplicationName":"Microsoft Teams Rooms"}},
{"node":{"id":"ext-mon","name":"Monitored Room","serialNumber":"sn-mon","macAddress":"","hardwareModel":"Test X","connected":true,"activeApplicationName":"Microsoft Teams Rooms"}},
{"node":{"id":"ext-mac","name":"Mac Room","serialNumber":"","macAddress":"aa-00-00-00-00-09","hardwareModel":"Test X","connected":false,"activeApplicationName":"Microsoft Teams Rooms"}},
{"node":{"id":"ext-free","name":"Free Room","serialNumber":"SN-FREE","macAddress":"","hardwareModel":"Test Y","connected":true,"activeApplicationName":"Microsoft Teams Rooms"}}]}}}}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestIntegrationsAPI(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integrations API test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('integ-api-fake') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('integ-api-other-fake') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1::uuid, 'ia-' || $1::text, 'ia', '\x00', now()) RETURNING id::text`, cust)
	// Collector devices the picker must recognise by serial (case-insensitive) and MAC (any separator).
	q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, serial_number, latest_status)
	   VALUES ($1, $2, 'm1', 'Collector Codec', 'conferencing', 'ping', 'SN-MON', 'online') RETURNING id::text`, cust, col)
	q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, mac_address, latest_status)
	   VALUES ($1, $2, 'm2', 'Collector Panel', 'conferencing', 'ping', 'AA:00:00:00:00:09', 'online') RETURNING id::text`, cust, col)

	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	lens := newFakeLens(t)
	h := New(st, cipher, nil, nil, nil, quiet).SetLensBaseURLs(lens.URL+"/token", lens.URL+"/graphql")

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	viewer := map[string]struct{}{"view.dashboard": {}}
	principals := map[string]portalauth.Principal{
		"admin":  {UserID: "00000000-0000-0000-0000-0000000000a1", Email: "cust.admin@example.test", CustomerID: cust, Permissions: all},
		"viewer": {UserID: "00000000-0000-0000-0000-0000000000a2", CustomerID: cust, Permissions: viewer},
		"vendor": {UserID: "00000000-0000-0000-0000-0000000000a3", Email: "vendor.admin@example.test", CustomerID: cust, IsVendor: true, Role: "admin"},
		"other":  {UserID: "00000000-0000-0000-0000-0000000000a4", CustomerID: other, Permissions: all},
	}
	mux := http.NewServeMux()
	gate := func(perm string, f http.HandlerFunc) http.Handler {
		return portalauth.RequirePermission(perm)(f)
	}
	mux.Handle("GET /api/v1/integrations", gate(portalauth.PermViewDashboard, h.ListIntegrations))
	mux.Handle("POST /api/v1/integrations/poly_lens/test", gate(portalauth.PermIntegrationManage, h.TestPolyLens))
	mux.Handle("PUT /api/v1/integrations/poly_lens", gate(portalauth.PermIntegrationManage, h.PutPolyLens))
	mux.Handle("DELETE /api/v1/integrations/poly_lens", gate(portalauth.PermIntegrationManage, h.DeletePolyLens))
	mux.Handle("GET /api/v1/integrations/poly_lens/devices", gate(portalauth.PermDeviceCRUD, h.ListPolyLensDevices))
	call := func(who, method, path string, body any) (int, string) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), principals[who]))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	const base = "/api/v1/integrations/poly_lens"

	// Permission gates: a viewer cannot connect, test or disconnect.
	if code, _ := call("viewer", "PUT", base, map[string]any{"client_id": "c", "client_secret": "s", "account_id": "acct-a"}); code != 403 {
		t.Errorf("viewer PUT = %d, want 403", code)
	}
	if code, _ := call("viewer", "POST", base+"/test", map[string]any{"client_id": "c", "client_secret": "s"}); code != 403 {
		t.Errorf("viewer test = %d, want 403", code)
	}
	if code, _ := call("viewer", "DELETE", base, nil); code != 403 {
		t.Errorf("viewer DELETE = %d, want 403", code)
	}

	// Before connecting: Lens is not connected, picker refuses, bad creds are a plain 400.
	_, body := call("viewer", "GET", "/api/v1/integrations", nil)
	if !strings.Contains(body, `"coming_soon"`) || !strings.Contains(body, `"logitech_sync"`) || !strings.Contains(body, `"microsoft_365"`) || !strings.Contains(body, `"servicenow"`) {
		t.Errorf("list missing static providers: %s", body)
	}
	if code, _ := call("admin", "GET", base+"/devices", nil); code != 409 {
		t.Errorf("picker before connect = %d, want 409", code)
	}
	code, body := call("admin", "POST", base+"/test", map[string]any{"client_id": "c", "client_secret": "bad"})
	if code != 400 || !strings.Contains(body, "Poly Lens rejected these credentials") {
		t.Errorf("bad credentials: %d %s", code, body)
	}
	// First connect: a secret is required, and the customer sees every account.
	if code, _ := call("admin", "POST", base+"/test", map[string]any{"client_id": "c"}); code != 400 {
		t.Errorf("test without secret on first connect = %d, want 400", code)
	}
	code, body = call("admin", "POST", base+"/test", map[string]any{"client_id": "c", "client_secret": fakeLensSecret})
	if code != 200 || !strings.Contains(body, "acct-a") || !strings.Contains(body, "acct-b") {
		t.Errorf("first connect test: %d %s", code, body)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"client_id": "c", "account_id": "acct-a"}); code != 400 {
		t.Errorf("first connect without secret = %d, want 400", code)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"client_id": "c", "client_secret": fakeLensSecret}); code != 400 {
		t.Errorf("first connect without account = %d, want 400", code)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-zzz"}); code != 400 {
		t.Errorf("connect to an account the credential cannot reach = %d, want 400", code)
	}

	// Connect: secret stored encrypted, never returned.
	code, body = call("admin", "PUT", base, map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-a", "allow_tagging": true})
	if code != 200 {
		t.Fatalf("connect: %d %s", code, body)
	}
	var enc []byte
	var acctName string
	if err := su.QueryRow(ctx, `SELECT secret_enc, account_name FROM integrations WHERE customer_id = $1 AND provider = 'poly_lens'`, cust).Scan(&enc, &acctName); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(fakeLensSecret)) {
		t.Errorf("secret_enc not encrypted: %q", enc)
	}
	if plain, err := cipher.Decrypt(enc); err != nil || string(plain) != fakeLensSecret {
		t.Errorf("secret does not round-trip: %v", err)
	}
	if acctName != "Acme Test" {
		t.Errorf("account_name = %q", acctName)
	}
	code, listBody := call("viewer", "GET", "/api/v1/integrations", nil)
	if code != 200 || strings.Contains(body+listBody, fakeLensSecret) || !strings.Contains(listBody, `"has_secret":true`) {
		t.Errorf("secret leaked or has_secret missing: %d %s %s", code, body, listBody)
	}
	var audits string
	if err := su.QueryRow(ctx, `SELECT string_agg(action || ':' || COALESCE(before::text, '') || COALESCE("after"::text, '') || COALESCE(metadata::text, ''), '|') FROM audit_log WHERE customer_id = $1 AND action LIKE 'integration.%'`, cust).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audits, "integration.connect") || strings.Contains(audits, fakeLensSecret) || strings.Contains(audits, "secret_enc") {
		t.Errorf("audit: %s", audits)
	}

	t.Run("AccountChangeVendorOnly", func(t *testing.T) {
		chg := map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-b", "allow_tagging": true}
		if code, _ := call("admin", "PUT", base, chg); code != 403 {
			t.Errorf("customer admin changing account = %d, want 403", code)
		}
		if code, body := call("vendor", "PUT", base, map[string]any{"client_id": "c", "account_id": "acct-b"}); code != 400 {
			t.Errorf("vendor changing account without secret = %d %s, want 400", code, body)
		}
		if code, body := call("vendor", "PUT", base, chg); code != 200 {
			t.Errorf("vendor changing account with secret = %d %s, want 200", code, body)
		}
		// Customer test after connection never lists other accounts.
		code, body := call("admin", "POST", base+"/test", map[string]any{"client_id": "c", "client_secret": fakeLensSecret})
		if code != 200 || strings.Contains(body, "acct-a") || !strings.Contains(body, "acct-b") {
			t.Errorf("customer test after connect: %d %s", code, body)
		}
		// Vendor test reuses the stored secret and sees every account.
		code, body = call("vendor", "POST", base+"/test", map[string]any{"client_id": "c"})
		if code != 200 || !strings.Contains(body, "acct-a") || !strings.Contains(body, "acct-b") {
			t.Errorf("vendor test with stored secret: %d %s", code, body)
		}
		// A customer cannot reuse the stored secret.
		if code, _ := call("admin", "POST", base+"/test", map[string]any{"client_id": "c"}); code != 400 {
			t.Errorf("customer test without secret = %d, want 400", code)
		}
		// Back to account A (vendor) for the picker checks.
		back := map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-a", "allow_tagging": true}
		if code, body := call("vendor", "PUT", base, back); code != 200 {
			t.Fatalf("vendor back to A: %d %s", code, body)
		}
	})

	// A customer may change settings on the same account without the secret.
	if code, body := call("admin", "PUT", base, map[string]any{"client_id": "c", "account_id": "acct-a", "allow_tagging": false}); code != 200 {
		t.Errorf("update tagging only: %d %s", code, body)
	}
	var tagging bool
	_ = su.QueryRow(ctx, `SELECT allow_tagging FROM integrations WHERE customer_id = $1`, cust).Scan(&tagging)
	if tagging {
		t.Error("allow_tagging not updated")
	}
	var updates int
	_ = su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id = $1 AND action = 'integration.update'`, cust).Scan(&updates)
	if updates == 0 {
		t.Error("no integration.update audit row")
	}

	// Picker: linked and already-monitored rows are marked.
	lensDev := q(`INSERT INTO devices (customer_id, source, platform, name, type, protocol, latest_status)
	              VALUES ($1, 'poly_lens', 'windows', 'Linked Room', 'conferencing', 'poly_lens', 'online') RETURNING id::text`, cust)
	integID := q(`SELECT id::text FROM integrations WHERE customer_id = $1`, cust)
	q(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id) VALUES ($1, $2, $3, 'ext-linked') RETURNING device_id::text`, lensDev, cust, integID)
	code, body = call("admin", "GET", base+"/devices", nil)
	if code != 200 {
		t.Fatalf("picker: %d %s", code, body)
	}
	var pick struct {
		Degraded bool `json:"degraded"`
		Devices  []struct {
			ExternalID         string  `json:"external_id"`
			LinkedDeviceID     *string `json:"linked_device_id"`
			AlreadyMonitoredAs *string `json:"already_monitored_as"`
			RoomName           string  `json:"room_name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(body), &pick); err != nil || len(pick.Devices) != 4 {
		t.Fatalf("picker body: %v %s", err, body)
	}
	byID := map[string]int{}
	for i, d := range pick.Devices {
		byID[d.ExternalID] = i
	}
	if d := pick.Devices[byID["ext-linked"]]; d.LinkedDeviceID == nil || *d.LinkedDeviceID != lensDev || d.RoomName != "Room L" {
		t.Errorf("linked row: %+v", d)
	}
	if d := pick.Devices[byID["ext-mon"]]; d.AlreadyMonitoredAs == nil || *d.AlreadyMonitoredAs != "Collector Codec" || d.LinkedDeviceID != nil {
		t.Errorf("serial-matched row: %+v", d)
	}
	if d := pick.Devices[byID["ext-mac"]]; d.AlreadyMonitoredAs == nil || *d.AlreadyMonitoredAs != "Collector Panel" {
		t.Errorf("mac-matched row: %+v", d)
	}
	if d := pick.Devices[byID["ext-free"]]; d.AlreadyMonitoredAs != nil || d.LinkedDeviceID != nil {
		t.Errorf("free row: %+v", d)
	}
	if _, body := call("admin", "GET", base+"/devices?q=free", nil); strings.Count(body, `"external_id"`) != 1 {
		t.Errorf("q filter: %s", body)
	}
	// Another customer sees nothing of this one's integration.
	if code, _ := call("other", "GET", base+"/devices", nil); code != 409 {
		t.Errorf("other customer picker = %d, want 409", code)
	}

	// Disconnect wipes the secret and keeps the links.
	if code, body := call("admin", "DELETE", base, nil); code != 204 && code != 200 {
		t.Fatalf("disconnect: %d %s", code, body)
	}
	var status string
	var secretNull bool
	var links int
	_ = su.QueryRow(ctx, `SELECT status, secret_enc IS NULL FROM integrations WHERE id = $1`, integID).Scan(&status, &secretNull)
	_ = su.QueryRow(ctx, `SELECT count(*) FROM integration_devices WHERE integration_id = $1`, integID).Scan(&links)
	if status != "disconnected" || !secretNull || links != 1 {
		t.Errorf("after disconnect: status=%s secretNull=%v links=%d", status, secretNull, links)
	}
	if code, _ := call("admin", "GET", base+"/devices", nil); code != 409 {
		t.Errorf("picker after disconnect = %d, want 409", code)
	}
	// A customer reconnecting is still bound to the stored account.
	if code, _ := call("admin", "PUT", base, map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-b"}); code != 403 {
		t.Errorf("reconnect to a different account as customer = %d, want 403", code)
	}
	if code, body := call("admin", "PUT", base, map[string]any{"client_id": "c", "client_secret": fakeLensSecret, "account_id": "acct-a"}); code != 200 {
		t.Errorf("reconnect same account: %d %s", code, body)
	}

	// ServiceNow status comes from notification channels.
	q(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, 'sn-fake', 'servicenow', 'https://sn.example.test') RETURNING id::text`, cust)
	_, listBody = call("viewer", "GET", "/api/v1/integrations", nil)
	var list []struct {
		Provider string `json:"provider"`
		Status   string `json:"status"`
	}
	_ = json.Unmarshal([]byte(listBody), &list)
	for _, e := range list {
		if e.Provider == "servicenow" && e.Status != "connected" {
			t.Errorf("servicenow status = %q, want connected", e.Status)
		}
		if e.Provider == "poly_lens" && e.Status != "connected" {
			t.Errorf("poly_lens status = %q", e.Status)
		}
	}
}
