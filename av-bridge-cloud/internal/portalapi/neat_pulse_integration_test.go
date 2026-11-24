package portalapi

// Integration tests for the Neat Pulse integration API: connect and the
// organisation rules, the device picker, device create/delete, and Neat
// people counts in the Room usage report. Runs only when TEST_DATABASE_URL
// points at a disposable Postgres. A fake Pulse serves the REST API; every
// name, serial and id is fake.

import (
	"bytes"
	"context"
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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/dloomes/av-bridge-cloud/internal/telemetry"
)

const fakeNeatKey = "fake-neat-key-value-123"

// newFakeNeat serves orgs org-a and org-b (any other org is 404). org-a
// has a Zoom Neat Bar with a paired Pad, a Teams Neat Board, a Neat Frame
// whose primary mode Pulse doesn't give, and an unpaired Pad.
func newFakeNeat(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeNeatKey {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"forbidden","status":403}`))
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "v1" || parts[1] != "orgs" || parts[3] != "endpoints" ||
			(parts[2] != "org-a" && parts[2] != "org-b") {
			w.WriteHeader(404)
			return
		}
		if parts[2] == "org-b" {
			_, _ = w.Write([]byte(`{"endpoints":[]}`))
			return
		}
		switch {
		case len(parts) == 4:
			_, _ = w.Write([]byte(`{"endpoints":[
 {"id":"ep-bar","connected":true,"inCallStatus":"NONE","model":"Neat Bar","serial":"FAKE-NB-1","roomId":7,"roomName":"Fake Huddle","locationId":1,"locationName":"Fake Site","regionId":1,"regionName":"Fake Region"},
 {"id":"ep-pad","connected":true,"inCallStatus":"NONE","model":"Neat Pad","serial":"FAKE-NP-1","roomId":7,"roomName":"Fake Huddle"},
 {"id":"ep-board","connected":false,"inCallStatus":"NONE","model":"Neat Board","serial":"FAKE-NBD-1","roomId":8,"roomName":"Fake Board Room"},
 {"id":"ep-frame","connected":true,"inCallStatus":"NONE","model":"Neat Frame","serial":"FAKE-NF-1","roomId":9,"roomName":"Fake Desk"},
 {"id":"ep-sched","connected":true,"inCallStatus":"NONE","model":"Neat Pad","serial":"FAKE-NP-9","roomId":10,"roomName":"Fake Corridor"}]}`))
		case len(parts) == 5 && parts[4] == "sensor":
			fmt.Fprintf(w, `{"data":[{"id":"ep-bar","endpointData":{"shutterClosed":false,"data":[{"people":3,"temp":21.5,"timestamp":"%d"}]}}]}`, time.Now().Unix())
		case len(parts) == 5 && parts[4] == "ep-bar":
			_, _ = w.Write([]byte(`{"id":"ep-bar","model":"Neat Bar","serial":"FAKE-NB-1","primaryMode":"ZOOM_ROOMS","pairingSerial":"FAKE-NP-1","roomName":"Fake Huddle","networks":[{"macAddress":"AABeAFMC"}]}`))
		case len(parts) == 5 && parts[4] == "ep-pad":
			_, _ = w.Write([]byte(`{"id":"ep-pad","model":"Neat Pad","serial":"FAKE-NP-1","primaryMode":"ZOOM_ROOMS","pairingSerial":"FAKE-NB-1","roomName":"Fake Huddle"}`))
		case len(parts) == 5 && parts[4] == "ep-board":
			_, _ = w.Write([]byte(`{"id":"ep-board","model":"Neat Board","serial":"FAKE-NBD-1","primaryMode":"MICROSOFT_TEAMS_ROOMS","roomName":"Fake Board Room"}`))
		case len(parts) == 5 && parts[4] == "ep-frame":
			_, _ = w.Write([]byte(`{"id":"ep-frame","model":"Neat Frame","serial":"FAKE-NF-1","primaryMode":"","roomName":"Fake Desk"}`))
		case len(parts) == 5 && parts[4] == "ep-sched":
			_, _ = w.Write([]byte(`{"id":"ep-sched","model":"Neat Pad","serial":"FAKE-NP-9","primaryMode":"SCHEDULER","roomName":"Fake Corridor"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNeatPulseAPI(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Neat Pulse API test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('neat-api-fake') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1::uuid, 'na-' || $1::text, 'na', '\x00', now()) RETURNING id::text`, cust)
	// A Collector device the picker must recognise by serial (case-insensitive).
	q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, serial_number)
	   VALUES ($1, $2, 'm1', 'Collector Board', 'display', 'ping', 'fake-nbd-1') RETURNING id::text`, cust, col)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	huddle := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Huddle') RETURNING id::text`, cust, bld)

	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	pulse := newFakeNeat(t)
	h := New(st, cipher, nil, nil, nil, quiet).SetNeatBaseURL(pulse.URL)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	principals := map[string]portalauth.Principal{
		"admin":  {UserID: "00000000-0000-0000-0000-0000000000c1", Email: "cust.admin@example.test", CustomerID: cust, Permissions: all},
		"viewer": {UserID: "00000000-0000-0000-0000-0000000000c2", CustomerID: cust, Permissions: map[string]struct{}{"view.dashboard": {}}},
		"vendor": {UserID: "00000000-0000-0000-0000-0000000000c3", Email: "vendor.admin@example.test", CustomerID: cust, IsVendor: true, Role: "admin"},
	}
	mux := http.NewServeMux()
	gate := func(perm string, f http.HandlerFunc) http.Handler { return portalauth.RequirePermission(perm)(f) }
	mux.Handle("GET /api/v1/integrations", gate(portalauth.PermViewDashboard, h.ListIntegrations))
	mux.Handle("POST /api/v1/integrations/neat_pulse/test", gate(portalauth.PermIntegrationManage, h.TestNeatPulse))
	mux.Handle("PUT /api/v1/integrations/neat_pulse", gate(portalauth.PermIntegrationManage, h.PutNeatPulse))
	mux.Handle("DELETE /api/v1/integrations/neat_pulse", gate(portalauth.PermIntegrationManage, h.DeleteNeatPulse))
	mux.Handle("GET /api/v1/integrations/neat_pulse/devices", gate(portalauth.PermDeviceCRUD, h.ListNeatPulseDevices))
	mux.HandleFunc("POST /api/v1/devices", h.CreateDevice)
	mux.HandleFunc("PATCH /api/v1/devices/{id}", h.UpdateDevice)
	mux.HandleFunc("DELETE /api/v1/devices/{id}", h.DeleteDevice)
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
	const base = "/api/v1/integrations/neat_pulse"
	key := map[string]any{"org_id": "org-a", "api_key": fakeNeatKey}

	// Permission gates.
	if code, _ := call("viewer", "PUT", base, key); code != 403 {
		t.Errorf("viewer PUT = %d, want 403", code)
	}
	if code, _ := call("viewer", "POST", base+"/test", key); code != 403 {
		t.Errorf("viewer test = %d, want 403", code)
	}
	// The card: not connected; the coming-soon cards stay.
	_, body := call("viewer", "GET", "/api/v1/integrations", nil)
	var list []struct {
		Provider string `json:"provider"`
		Status   string `json:"status"`
	}
	_ = json.Unmarshal([]byte(body), &list)
	statuses := map[string]string{}
	for _, e := range list {
		statuses[e.Provider] = e.Status
	}
	if statuses["neat_pulse"] != "not_connected" || statuses["logitech_sync"] != "coming_soon" || statuses["microsoft_365"] != "coming_soon" {
		t.Errorf("integrations list: %s", body)
	}
	if code, _ := call("admin", "GET", base+"/devices", nil); code != 409 {
		t.Errorf("picker before connect = %d, want 409", code)
	}
	if code, b := call("admin", "POST", base+"/test", map[string]any{"org_id": "org-a", "api_key": "wrong"}); code != 400 || !strings.Contains(b, "rejected") {
		t.Errorf("bad key test = %d %s", code, b)
	}
	if code, b := call("admin", "POST", base+"/test", map[string]any{"org_id": "org-nope", "api_key": fakeNeatKey}); code != 400 || !strings.Contains(b, "organisation") {
		t.Errorf("unknown org test = %d %s", code, b)
	}
	code, b := call("admin", "POST", base+"/test", key)
	if code != 200 || !strings.Contains(b, `"device_count":5`) {
		t.Fatalf("test = %d %s", code, b)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"org_id": "org-a"}); code != 400 {
		t.Errorf("connect without key = %d, want 400", code)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"api_key": fakeNeatKey}); code != 400 {
		t.Errorf("connect without org = %d, want 400", code)
	}

	// Connect: key stored encrypted, never returned.
	code, b = call("admin", "PUT", base, key)
	if code != 200 || strings.Contains(b, fakeNeatKey) || !strings.Contains(b, `"account_id":"org-a"`) || !strings.Contains(b, `"has_secret":true`) {
		t.Fatalf("connect = %d %s", code, b)
	}
	var enc []byte
	if err := su.QueryRow(ctx, `SELECT secret_enc FROM integrations WHERE customer_id = $1 AND provider = 'neat_pulse'`, cust).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte(fakeNeatKey)) {
		t.Fatal("api key stored in clear")
	}
	if plain, err := cipher.Decrypt(enc); err != nil || string(plain) != fakeNeatKey {
		t.Fatalf("key does not round-trip: %v", err)
	}
	_, listBody := call("admin", "GET", "/api/v1/integrations", nil)
	if strings.Contains(listBody, fakeNeatKey) || !strings.Contains(listBody, `"provider":"neat_pulse","status":"connected"`) {
		t.Errorf("list after connect: %s", listBody)
	}
	var audits string
	_ = su.QueryRow(ctx, `SELECT COALESCE(string_agg(action || COALESCE("after"::text, ''), '|'), '') FROM audit_log WHERE customer_id = $1 AND action LIKE 'integration.%'`, cust).Scan(&audits)
	if !strings.Contains(audits, "integration.connect") || strings.Contains(audits, fakeNeatKey) {
		t.Errorf("audit: %s", audits)
	}

	// Organisation changes: vendor only, and with the key again.
	if code, _ := call("admin", "PUT", base, map[string]any{"org_id": "org-b", "api_key": fakeNeatKey}); code != 403 {
		t.Errorf("customer changing org = %d, want 403", code)
	}
	if code, _ := call("admin", "POST", base+"/test", map[string]any{"org_id": "org-b", "api_key": fakeNeatKey}); code != 403 {
		t.Errorf("customer testing another org = %d, want 403", code)
	}
	if code, _ := call("admin", "POST", base+"/test", map[string]any{}); code != 400 {
		t.Errorf("customer test reusing the stored key = %d, want 400", code)
	}
	if code, b := call("vendor", "POST", base+"/test", map[string]any{}); code != 200 || !strings.Contains(b, `"device_count":5`) {
		t.Errorf("vendor test with the stored key = %d %s", code, b)
	}
	if code, _ := call("vendor", "PUT", base, map[string]any{"org_id": "org-b"}); code != 400 {
		t.Errorf("vendor changing org without key = %d, want 400", code)
	}
	if code, b := call("vendor", "PUT", base, map[string]any{"org_id": "org-b", "api_key": fakeNeatKey}); code != 200 {
		t.Errorf("vendor changing org = %d %s", code, b)
	}
	if code, b := call("vendor", "PUT", base, key); code != 200 {
		t.Fatalf("vendor back to org-a = %d %s", code, b)
	}
	// Same org, no key: a settings-only save is allowed.
	if code, b := call("admin", "PUT", base, map[string]any{"org_id": "org-a"}); code != 200 {
		t.Errorf("same-org save without key = %d %s", code, b)
	}

	// Picker: main units only, primary mode, already monitored.
	code, b = call("admin", "GET", base+"/devices", nil)
	if code != 200 {
		t.Fatalf("picker = %d %s", code, b)
	}
	type pickRow struct {
		ExternalID         string  `json:"external_id"`
		Name               string  `json:"name"`
		RoomName           string  `json:"room_name"`
		LocationName       string  `json:"location_name"`
		PrimaryMode        string  `json:"primary_mode"`
		SuggestedType      string  `json:"suggested_type"`
		Connected          bool    `json:"connected"`
		LinkedDeviceID     *string `json:"linked_device_id"`
		AlreadyMonitoredAs *string `json:"already_monitored_as"`
	}
	var pick struct {
		Devices []pickRow `json:"devices"`
	}
	_ = json.Unmarshal([]byte(b), &pick)
	rows := map[string]pickRow{}
	for _, d := range pick.Devices {
		rows[d.ExternalID] = d
	}
	if _, ok := rows["ep-pad"]; ok || len(rows) != 4 {
		t.Fatalf("picker must list the main units and the unpaired Pad, not the paired Pad: %s", b)
	}
	if r := rows["ep-bar"]; r.PrimaryMode != "ZOOM_ROOMS" || r.SuggestedType != "zoom_room" || r.Name != "Fake Huddle" || r.LocationName != "Fake Site" || !r.Connected {
		t.Errorf("bar row %+v", r)
	}
	if r := rows["ep-board"]; r.SuggestedType != "teams_room" || r.AlreadyMonitoredAs == nil || *r.AlreadyMonitoredAs != "Collector Board" {
		t.Errorf("board row %+v", r)
	}
	if r := rows["ep-frame"]; r.SuggestedType != "" {
		t.Errorf("frame row %+v", r)
	}
	if _, b := call("admin", "GET", base+"/devices?q=board", nil); strings.Count(b, `"external_id"`) != 1 {
		t.Errorf("q filter: %s", b)
	}

	// Create: type from the primary mode, Android, no Collector.
	create := func(body map[string]any) (int, string, string) {
		t.Helper()
		body["source"] = "neat_pulse"
		code, b := call("admin", "POST", "/api/v1/devices", body)
		var out struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(b), &out)
		if code != 201 {
			return code, b, ""
		}
		return code, out.ID, out.Type
	}
	bad := func(what string, body map[string]any) {
		t.Helper()
		if code, b, _ := create(body); code != 400 {
			t.Errorf("%s = %d %s, want 400", what, code, b)
		}
	}
	bad("bad platform", map[string]any{"external_id": "ep-bar", "platform": "windows"})
	bad("bad type", map[string]any{"external_id": "ep-bar", "type": "display"})
	bad("collector set", map[string]any{"external_id": "ep-bar", "collector_id": col})
	bad("credentials set", map[string]any{"external_id": "ep-bar", "password": "x"})
	bad("unknown endpoint", map[string]any{"external_id": "ep-nope"})
	bad("paired pad", map[string]any{"external_id": "ep-pad"})
	bad("type unknown and not chosen", map[string]any{"external_id": "ep-frame"})
	bad("type against the primary mode", map[string]any{"external_id": "ep-board", "type": "zoom_room"})

	code, barID, typ := create(map[string]any{"external_id": "ep-bar", "room_id": huddle})
	if code != 201 || typ != "zoom_room" {
		t.Fatalf("create bar = %d %s %s", code, barID, typ)
	}
	var src, plat, dtyp, name, serial, mac string
	var collNull bool
	if err := su.QueryRow(ctx, `SELECT source, platform, type, name, collector_id IS NULL, COALESCE(serial_number,''), COALESCE(mac_address,'')
	                             FROM devices WHERE id = $1`, barID).Scan(&src, &plat, &dtyp, &name, &collNull, &serial, &mac); err != nil {
		t.Fatal(err)
	}
	if src != "neat_pulse" || plat != "android" || dtyp != "zoom_room" || name != "Fake Huddle" || !collNull || serial != "FAKE-NB-1" || mac != "00:00:5e:00:53:02" {
		t.Errorf("bar row: %s %s %s %q %v %q %q", src, plat, dtyp, name, collNull, serial, mac)
	}
	if code, b, _ := create(map[string]any{"external_id": "ep-bar"}); code != 409 {
		t.Errorf("duplicate link = %d %s, want 409", code, b)
	}
	if code, _, typ := create(map[string]any{"external_id": "ep-frame", "type": "teams_room", "name": "Desk Frame"}); code != 201 || typ != "teams_room" {
		t.Errorf("frame with chosen type = %d %s", code, typ)
	}
	if code, _, typ := create(map[string]any{"external_id": "ep-board"}); code != 201 || typ != "teams_room" {
		t.Errorf("board = %d %s", code, typ)
	}
	// The picker now marks the linked rows.
	_, b = call("admin", "GET", base+"/devices", nil)
	if !strings.Contains(b, `"linked_device_id":"`+barID+`"`) {
		t.Errorf("picker does not mark the linked bar: %s", b)
	}
	// Only name, room, tags and asset can change on a Neat device.
	if code, b := call("admin", "PATCH", "/api/v1/devices/"+barID, map[string]any{"type": "teams_room"}); code != 400 || !strings.Contains(b, "Neat Pulse") {
		t.Errorf("retype neat device = %d %s", code, b)
	}
	if code, b := call("admin", "PATCH", "/api/v1/devices/"+barID, map[string]any{"name": "Huddle Bar"}); code != 204 && code != 200 {
		t.Errorf("rename neat device = %d %s", code, b)
	}
	// A Collector device can't be a Zoom Room.
	if code, b := call("admin", "POST", "/api/v1/devices", map[string]any{"collector_id": col, "reported_id": "z1", "type": "zoom_room", "protocol": "ping"}); code != 400 {
		t.Errorf("collector zoom_room = %d %s", code, b)
	}

	// Room usage counts the Neat Bar's people: poll it, then roll up.
	p := integrations.NewPoller(st.AdminPool(), cipher, nil, nil, 0, quiet).SetNeatBaseURL(pulse.URL).SetOnlyCustomers(cust)
	if err := p.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var people int
	if err := su.QueryRow(ctx, `SELECT (latest_metrics->>'people_count')::int FROM devices WHERE id = $1`, barID).Scan(&people); err != nil || people != 3 {
		t.Fatalf("bar people_count = %d %v", people, err)
	}
	if err := telemetry.Rollup(ctx, su, 0); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/r?days=1", nil)
	req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), principals["admin"]))
	rec := httptest.NewRecorder()
	h.RoomUsageReport(rec, req)
	var usage []struct {
		RoomName       string `json:"room_name"`
		HasUsageSignal bool   `json:"has_usage_signal"`
		HoursOccupied  int    `json:"hours_occupied"`
		PeakPeople     *int   `json:"peak_people"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &usage)
	found := false
	for _, u := range usage {
		if u.RoomName == "Huddle" {
			found = true
			if !u.HasUsageSignal || u.HoursOccupied < 1 || u.PeakPeople == nil || *u.PeakPeople != 3 {
				t.Errorf("Huddle usage %+v; want occupied with peak 3", u)
			}
		}
	}
	if !found {
		t.Errorf("Huddle missing from room usage: %s", rec.Body.String())
	}

	// Delete releases the link; the audit names the provider.
	if code, b := call("admin", "DELETE", "/api/v1/devices/"+barID, nil); code != 204 {
		t.Fatalf("delete = %d %s", code, b)
	}
	var links int
	_ = su.QueryRow(ctx, `SELECT count(*) FROM integration_devices WHERE device_id = $1`, barID).Scan(&links)
	var meta string
	_ = su.QueryRow(ctx, `SELECT COALESCE(metadata::text, '') FROM audit_log WHERE customer_id = $1 AND action = 'device.delete' AND target_id = $2`, cust, barID).Scan(&meta)
	if links != 0 || !strings.Contains(meta, `"neat_pulse"`) {
		t.Errorf("after delete: links=%d meta=%s", links, meta)
	}
	if code, _, _ := create(map[string]any{"external_id": "ep-bar"}); code != 201 {
		t.Errorf("relink after delete = %d", code)
	}

	// Disconnect wipes the key and keeps the links.
	if code, b := call("admin", "DELETE", base, nil); code != 204 {
		t.Fatalf("disconnect = %d %s", code, b)
	}
	var status string
	var secretNull bool
	_ = su.QueryRow(ctx, `SELECT status, secret_enc IS NULL FROM integrations WHERE customer_id = $1 AND provider = 'neat_pulse'`, cust).Scan(&status, &secretNull)
	if status != "disconnected" || !secretNull {
		t.Errorf("after disconnect: %s %v", status, secretNull)
	}
	if code, _ := call("admin", "GET", base+"/devices", nil); code != 409 {
		t.Errorf("picker after disconnect = %d, want 409", code)
	}
	if code, _ := call("admin", "PUT", base, map[string]any{"org_id": "org-a"}); code != 400 {
		t.Errorf("reconnect without key = %d, want 400", code)
	}
}
