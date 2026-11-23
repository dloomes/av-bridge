package portalapi

// Integration tests for creating and deleting Poly Lens managed Teams Rooms.
// Runs only when TEST_DATABASE_URL points at a disposable Postgres. A fake
// Lens serves the token and GraphQL endpoints and records tag mutations.

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
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

func TestLensDeviceCreateDelete(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Lens device CRUD test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('lens-crud-fake') RETURNING id::text`)

	// Fake Lens: five Teams Rooms in acct-a; records tag mutations.
	var mu sync.Mutex
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "tenants") {
			io.WriteString(w, `{"data":{"tenants":[{"id":"acct-a","name":"Acme Test"}]}}`)
			return
		}
		for _, m := range []string{"addDeviceTag", "removeDeviceTag"} {
			if strings.Contains(req.Query, m) {
				mu.Lock()
				calls = append(calls, m+"|"+req.Variables["deviceId"].(string)+"|"+req.Variables["tag"].(string))
				mu.Unlock()
				if req.Variables["deviceId"] == "ext-failtag" {
					io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
					return
				}
				io.WriteString(w, `{"data":{"`+m+`":true}}`)
				return
			}
		}
		node := func(id, name string) string {
			return `{"node":{"id":"` + id + `","name":"` + name + `","serialNumber":"SN-` + id + `","macAddress":"AA:00:00:00:00:02","hardwareModel":"Test X","connected":true,"activeApplicationName":"Microsoft Teams Rooms"}}`
		}
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[`+
			node("ext-1", "Fake Room One")+","+node("ext-2", "Fake Room Two")+","+node("ext-failtag", "Fake Room Fail")+`]}}}}}`)
	})
	lens := httptest.NewServer(mux)
	defer lens.Close()
	tagCalls := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}

	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, cipher, nil, nil, nil, quiet).SetLensBaseURLs(lens.URL+"/token", lens.URL+"/graphql")
	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000b1", Email: "cust.admin@example.test", CustomerID: cust, Permissions: all}
	router := http.NewServeMux()
	router.HandleFunc("POST /api/v1/devices", h.CreateDevice)
	router.HandleFunc("DELETE /api/v1/devices/{id}", h.DeleteDevice)
	router.HandleFunc("PUT /api/v1/integrations/poly_lens", h.PutPolyLens)
	router.HandleFunc("DELETE /api/v1/integrations/poly_lens", h.DeletePolyLens)
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	create := func(ext string) (int, string, string) {
		code, body := call("POST", "/api/v1/devices", map[string]any{
			"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": ext,
		})
		var out struct {
			ID       string `json:"id"`
			TagState string `json:"tag_state"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		return code, out.ID, out.TagState
	}
	setTagging := func(on bool) {
		t.Helper()
		if code, body := call("PUT", "/api/v1/integrations/poly_lens", map[string]any{
			"client_id": "c", "client_secret": "fake-secret", "account_id": "acct-a", "allow_tagging": on}); code != 200 {
			t.Fatalf("put integration: %d %s", code, body)
		}
	}

	if code, id, _ := create("ext-1"); code != 409 {
		t.Fatalf("create before connect = %d (%s), want 409", code, id)
	}
	setTagging(true)

	// Validation.
	bad := func(what string, body map[string]any) {
		t.Helper()
		if code, resp := call("POST", "/api/v1/devices", body); code != 400 {
			t.Errorf("%s = %d %s, want 400", what, code, resp)
		}
	}
	bad("bad platform", map[string]any{"source": "poly_lens", "type": "teams_room", "platform": "linux", "external_id": "ext-1"})
	bad("no platform", map[string]any{"source": "poly_lens", "type": "teams_room", "external_id": "ext-1"})
	bad("wrong type", map[string]any{"source": "poly_lens", "type": "display", "platform": "windows", "external_id": "ext-1"})
	bad("collector_id set", map[string]any{"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-1", "collector_id": "00000000-0000-0000-0000-000000000001"})
	bad("protocol set", map[string]any{"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-1", "protocol": "ping"})
	bad("credentials set", map[string]any{"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-1", "password": "x"})
	bad("unknown external id", map[string]any{"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-nope"})
	bad("unknown source", map[string]any{"source": "other", "type": "teams_room"})
	bad("teams_room on collector path", map[string]any{"type": "teams_room", "collector_id": "00000000-0000-0000-0000-000000000001", "reported_id": "x"})
	if n := len(tagCalls()); n != 0 {
		t.Fatalf("validation failures made %d tag calls", n)
	}

	// Create with tagging allowed.
	code, d1, state := create("ext-1")
	if code != 201 || state != "tagged" {
		t.Fatalf("create ext-1 = %d state %q", code, state)
	}
	var src, plat, name, ser string
	var collNull bool
	if err := su.QueryRow(ctx, `SELECT source, platform, name, collector_id IS NULL, COALESCE(serial_number,'') FROM devices WHERE id = $1`, d1).Scan(&src, &plat, &name, &collNull, &ser); err != nil {
		t.Fatal(err)
	}
	if src != "poly_lens" || plat != "windows" || name != "Fake Room One" || !collNull || ser != "SN-ext-1" {
		t.Errorf("device row: %s %s %q collnull=%v serial=%q", src, plat, name, collNull, ser)
	}
	var extName, model, tagState string
	if err := su.QueryRow(ctx, `SELECT external_name, model, tag_state FROM integration_devices WHERE device_id = $1`, d1).Scan(&extName, &model, &tagState); err != nil {
		t.Fatal(err)
	}
	if extName != "Fake Room One" || model != "Test X" || tagState != "tagged" {
		t.Errorf("link row: %q %q %q", extName, model, tagState)
	}
	if c := tagCalls(); len(c) != 1 || c[0] != "addDeviceTag|ext-1|marcus:"+d1 {
		t.Errorf("tag calls: %v", c)
	}
	var meta string
	if err := su.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE customer_id = $1 AND action = 'device.create' AND target_id = $2`, cust, d1).Scan(&meta); err != nil || !strings.Contains(meta, "poly_lens") {
		t.Errorf("create audit metadata %q: %v", meta, err)
	}

	// Second link of the same Lens device.
	if code, _, _ := create("ext-1"); code != 409 {
		t.Errorf("duplicate link = %d, want 409", code)
	}

	// Tag failure: device is still created.
	code, dFail, state := create("ext-failtag")
	if code != 201 || state != "failed" {
		t.Fatalf("create ext-failtag = %d state %q", code, state)
	}
	if got := q(`SELECT tag_state FROM integration_devices WHERE device_id = $1`, dFail); got != "failed" {
		t.Errorf("tag_state = %q, want failed", got)
	}

	// Tagging disabled: no tag call.
	setTagging(false)
	before := len(tagCalls())
	code, d2, state := create("ext-2")
	if code != 201 || state != "none" {
		t.Fatalf("create ext-2 = %d state %q", code, state)
	}
	if len(tagCalls()) != before {
		t.Errorf("tag call made while tagging disabled: %v", tagCalls())
	}
	if got := q(`SELECT tag_state FROM integration_devices WHERE device_id = $1`, d2); got != "none" {
		t.Errorf("tag_state = %q, want none", got)
	}
	setTagging(true)

	// Delete while connected removes the tag and frees the Lens device.
	if code, body := call("DELETE", "/api/v1/devices/"+d1, nil); code != 204 {
		t.Fatalf("delete d1 = %d %s", code, body)
	}
	if c := tagCalls(); c[len(c)-1] != "removeDeviceTag|ext-1|marcus:"+d1 {
		t.Errorf("last tag call: %v", c)
	}
	if err := su.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE customer_id = $1 AND action = 'device.delete' AND target_id = $2`, cust, d1).Scan(&meta); err != nil || !strings.Contains(meta, `"tag_state": "none"`) && !strings.Contains(meta, `"tag_state":"none"`) {
		t.Errorf("delete audit metadata %q: %v", meta, err)
	}
	if got := q(`SELECT count(*)::text FROM integration_devices WHERE device_id = $1`, d1); got != "0" {
		t.Errorf("link row kept after delete: %s", got)
	}
	code, d3, state := create("ext-1")
	if code != 201 || state != "tagged" {
		t.Fatalf("relink ext-1 = %d state %q", code, state)
	}

	// Delete after disconnect leaves the tag behind and records it.
	if code, body := call("DELETE", "/api/v1/integrations/poly_lens", nil); code != 204 {
		t.Fatalf("disconnect: %d %s", code, body)
	}
	n := len(tagCalls())
	if code, body := call("DELETE", "/api/v1/devices/"+d3, nil); code != 204 {
		t.Fatalf("delete d3 = %d %s", code, body)
	}
	if len(tagCalls()) != n {
		t.Errorf("tag removal attempted while disconnected: %v", tagCalls())
	}
	if err := su.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE customer_id = $1 AND action = 'device.delete' AND target_id = $2`, cust, d3).Scan(&meta); err != nil || !strings.Contains(meta, "left_behind") {
		t.Errorf("left_behind not recorded: %q %v", meta, err)
	}
	if code, _, _ := create("ext-2"); code != 409 {
		t.Errorf("create after disconnect = %d, want 409", code)
	}
	if code, _ := call("DELETE", "/api/v1/devices/"+d3, nil); code != 404 {
		t.Errorf("second delete = %d, want 404", code)
	}
}
