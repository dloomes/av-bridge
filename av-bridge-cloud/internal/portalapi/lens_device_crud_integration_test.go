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
	lensTags := map[string]string{}
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
		switch {
		case strings.Contains(req.Query, "lensTags(filter"):
			mu.Lock()
			name, _ := req.Variables["name"].(string)
			var found string
			for id, n := range lensTags {
				if n == name {
					found = `{"id":"` + id + `","name":"` + n + `"}`
				}
			}
			mu.Unlock()
			io.WriteString(w, `{"data":{"tenant":{"lensTags":[`+found+`]}}}`)
			return
		case strings.Contains(req.Query, "createTag"):
			mu.Lock()
			name, _ := req.Variables["name"].(string)
			id := "tag-" + name
			lensTags[id] = name
			calls = append(calls, "create|"+name)
			mu.Unlock()
			io.WriteString(w, `{"data":{"createTag":{"id":"`+id+`"}}}`)
			return
		case strings.Contains(req.Query, "addLensTagToDevice"):
			ids, _ := req.Variables["tagIds"].([]any)
			devs, _ := req.Variables["deviceIds"].([]any)
			mu.Lock()
			calls = append(calls, "add|"+devs[0].(string)+"|"+lensTags[ids[0].(string)])
			mu.Unlock()
			if devs[0] == "ext-failtag" {
				w.WriteHeader(403)
				io.WriteString(w, `{"errors":[{"message":"no","extensions":{"code":"AUTHORIZATION_ERROR"}}]}`)
				return
			}
			io.WriteString(w, `{"data":{"addLensTagToDevice":[{"deviceId":"`+devs[0].(string)+`","success":true}]}}`)
			return
		case strings.Contains(req.Query, "removeLensTagFromDevice"):
			devs, _ := req.Variables["deviceIds"].([]any)
			mu.Lock()
			calls = append(calls, "remove|"+devs[0].(string)+"|"+lensTags[req.Variables["tagId"].(string)])
			mu.Unlock()
			io.WriteString(w, `{"data":{"removeLensTagFromDevice":[{"deviceId":"`+devs[0].(string)+`","success":true}]}}`)
			return
		case strings.Contains(req.Query, "deleteTag"):
			mu.Lock()
			id, _ := req.Variables["id"].(string)
			calls = append(calls, "deleteTag|"+lensTags[id])
			delete(lensTags, id)
			mu.Unlock()
			io.WriteString(w, `{"data":{"deleteTag":{"success":true}}}`)
			return
		}
		node := func(id, name string) string {
			return `{"node":{"id":"` + id + `","name":"` + name + `","serialNumber":"SN-` + id + `","macAddress":"AA:00:00:00:00:02","hardwareModel":"Test X","connected":true,"activeApplicationName":"Microsoft Teams Rooms"}}`
		}
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[`+
			node("ext-1", "Fake Room One")+","+node("ext-2", "Fake Room Two")+","+node("ext-failtag", "Fake Room Fail")+","+node("ext-3", "Fake Room Three")+","+node("ext-4", "Fake Room Four")+`]}}}}}`)
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
	router.HandleFunc("PATCH /api/v1/devices/{id}", h.UpdateDevice)
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
	if c := tagCalls(); len(c) != 2 || c[0] != "create|marcus:"+d1 || c[1] != "add|ext-1|marcus:"+d1 {
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
	if got := q(`SELECT COALESCE(tag_error,'') FROM integration_devices WHERE device_id = $1`, dFail); got != "Lens role can't tag devices" {
		t.Errorf("tag_error = %q", got)
	}
	// The orphan tag created for the failed assignment was cleaned up.
	if c := tagCalls(); c[len(c)-1] != "deleteTag|marcus:"+dFail {
		t.Errorf("orphan tag not deleted: %v", c)
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
	if c := tagCalls(); c[len(c)-2] != "remove|ext-1|marcus:"+d1 || c[len(c)-1] != "deleteTag|marcus:"+d1 {
		t.Errorf("last tag calls: %v", c)
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

	// Update guards: a Lens device only takes name, room, tags and asset.
	patch := func(id string, body map[string]any) int {
		code, _ := call("PATCH", "/api/v1/devices/"+id, body)
		return code
	}
	for _, f := range []map[string]any{
		{"type": "display"}, {"protocol": "ping"}, {"address": "10.0.0.1"}, {"poll_rate_seconds": 5},
		{"username": "x"}, {"password": "x"}, {"commands": map[string]string{"a": "b"}},
	} {
		if code := patch(d3, f); code != 400 {
			t.Errorf("PATCH lens device %v = %d, want 400", f, code)
		}
	}
	if code := patch(d3, map[string]any{"name": "Renamed Fake Room", "tags": map[string]string{"k": "v"}}); code != 204 && code != 200 {
		t.Errorf("PATCH lens name/tags = %d", code)
	}
	if code := patch(d3, map[string]any{"asset": map[string]any{"asset_tag": "LENS-AT-1", "manufacturer": "Fake"}}); code != 204 && code != 200 {
		t.Errorf("PATCH lens inline asset = %d", code)
	}
	if got := q(`SELECT (asset_id IS NOT NULL)::text FROM devices WHERE id = $1`, d3); got != "true" {
		t.Errorf("asset not linked on update: %s", got)
	}
	coll := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	           VALUES ($1::uuid, 'lens-crud-' || $1::text, 'fake collector', '\x00') RETURNING id::text`, cust)
	cdev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol)
	           VALUES ($1, $2, 'fake-cdev', 'Fake Collector Device', 'ping') RETURNING id::text`, cust, coll)
	if code := patch(cdev, map[string]any{"type": "teams_room"}); code != 400 {
		t.Errorf("PATCH collector device to teams_room = %d, want 400", code)
	}

	// Create-time assets are accepted for a Lens device.
	assetID := q(`INSERT INTO assets (customer_id, name, category) VALUES ($1, 'Fake existing asset', 'other') RETURNING id::text`, cust)
	c, body := call("POST", "/api/v1/devices", map[string]any{
		"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-3", "asset_id": assetID})
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if c != 201 || q(`SELECT asset_id::text FROM devices WHERE id = $1`, created.ID) != assetID {
		t.Errorf("create with asset_id = %d %s", c, body)
	}
	if c, body := call("POST", "/api/v1/devices", map[string]any{
		"source": "poly_lens", "type": "teams_room", "platform": "windows", "external_id": "ext-4",
		"asset_id": "00000000-0000-0000-0000-000000000009"}); c != 400 {
		t.Errorf("create with unknown asset_id = %d %s, want 400", c, body)
	}
	if code, _ := call("DELETE", "/api/v1/devices/"+created.ID, nil); code != 204 {
		t.Errorf("delete asset device = %d", code)
	}

	// Deleting a device drops its alert state rows.
	q(`INSERT INTO integration_alert_state (device_id, customer_id, condition, since) VALUES ($1, $2, 'device_offline', now()) RETURNING condition`, d3, cust)

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
	if got := q(`SELECT count(*)::text FROM integration_alert_state WHERE device_id = $1`, d3); got != "0" {
		t.Errorf("alert state kept after delete: %s", got)
	}
	if code, _, _ := create("ext-2"); code != 409 {
		t.Errorf("create after disconnect = %d, want 409", code)
	}
	if code, _ := call("DELETE", "/api/v1/devices/"+d3, nil); code != 404 {
		t.Errorf("second delete = %d, want 404", code)
	}
}
