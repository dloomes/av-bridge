package portalapi

// Integration test: devices with no Collector (Poly Lens managed) appear in
// every read path. Runs only when TEST_DATABASE_URL points at a disposable
// Postgres (see device_move_integration_test.go).

import (
	"context"
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

func TestIntegrationDeviceInReadPaths(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration device read test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('integ-read-fake') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1::uuid, 'ir-' || $1::text, 'ir', '\x00', now()) RETURNING id::text`, cust)
	collDev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, latest_status, last_seen_at)
	          VALUES ($1, $2, 'cd', 'collector device', 'display', 'ping', 'online', now()) RETURNING id::text`, cust, col)
	integ := q(`INSERT INTO integrations (customer_id, provider, account_name) VALUES ($1, 'poly_lens', 'Fake Account') RETURNING id::text`, cust)
	lens := func(name, ext, age string) string {
		id := q(`INSERT INTO devices (customer_id, source, platform, name, type, protocol, latest_status, last_seen_at)
		         VALUES ($1, 'poly_lens', 'windows', $2, 'conferencing', 'poly_lens', 'online', now() - $3::interval)
		         RETURNING id::text`, cust, name, age)
		q(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id, external_name, tag_state)
		   VALUES ($1, $2, $3, $4, $5, 'tagged') RETURNING device_id::text`, id, cust, integ, ext, name+" (Lens)")
		return id
	}
	fresh := lens("lens fresh", "ext-1", "0 minutes")
	stale := lens("lens stale", "ext-2", "16 minutes")
	q(`UPDATE devices SET make='Poly', model='Fake Model X', serial_number='FAKE-SN-1', firmware_version='9.9.9', mac_address='00:00:00:00:00:01'
	   WHERE id = $1 RETURNING id::text`, fresh)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	pr := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e1", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/devices", h.ListDevices)
	mux.HandleFunc("GET /api/v1/devices/{id}", h.GetDevice)
	mux.HandleFunc("GET /api/v1/devices/{id}/telemetry", h.GetTelemetry)
	mux.HandleFunc("GET /api/v1/status", h.Status)
	mux.HandleFunc("GET /api/v1/reports/device-uptime", h.DeviceUptimeReport)
	get := func(path string, dst any) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), pr))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
	}

	type integration struct {
		Provider     string `json:"provider"`
		ExternalName string `json:"external_name"`
		AccountName  string `json:"account_name"`
		TagState     string `json:"tag_state"`
	}
	type dev struct {
		ID              string       `json:"id"`
		CollectorID     *string      `json:"collector_id"`
		Source          string       `json:"source"`
		Platform        *string      `json:"platform"`
		Status          string       `json:"status"`
		Integration     *integration `json:"integration"`
		Make            *string      `json:"make"`
		Model           *string      `json:"model"`
		SerialNumber    *string      `json:"serial_number"`
		FirmwareVersion *string      `json:"firmware_version"`
		MACAddress      *string      `json:"mac_address"`
	}
	check := func(where string, d dev, wantSource, wantStatus string) {
		t.Helper()
		if d.Source != wantSource || d.Status != wantStatus {
			t.Errorf("%s: source/status = %s/%s, want %s/%s", where, d.Source, d.Status, wantSource, wantStatus)
		}
		if wantSource == "collector" {
			if d.CollectorID == nil || d.Integration != nil || d.Platform != nil {
				t.Errorf("%s: collector device = %+v", where, d)
			}
			return
		}
		if d.CollectorID != nil || d.Platform == nil || *d.Platform != "windows" {
			t.Errorf("%s: lens device collector/platform = %+v", where, d)
		}
		if d.Integration == nil || d.Integration.Provider != "poly_lens" || d.Integration.AccountName != "Fake Account" || d.Integration.TagState != "tagged" {
			t.Errorf("%s: integration = %+v", where, d.Integration)
		}
	}

	var list []dev
	get("/api/v1/devices", &list)
	if len(list) != 3 {
		t.Fatalf("list = %+v, want 3 devices", list)
	}
	byID := map[string]dev{}
	for _, d := range list {
		byID[d.ID] = d
	}
	check("list collector", byID[collDev], "collector", "online")
	check("list fresh", byID[fresh], "poly_lens", "online")
	check("list stale", byID[stale], "poly_lens", "offline")

	var one dev
	get("/api/v1/devices/"+fresh, &one)
	check("detail fresh", one, "poly_lens", "online")
	if one.Make == nil || *one.Make != "Poly" || one.Model == nil || *one.Model != "Fake Model X" ||
		one.SerialNumber == nil || *one.SerialNumber != "FAKE-SN-1" ||
		one.FirmwareVersion == nil || *one.FirmwareVersion != "9.9.9" ||
		one.MACAddress == nil || *one.MACAddress != "00:00:00:00:00:01" {
		t.Errorf("detail fresh inventory = %+v", one)
	}
	one = dev{}
	get("/api/v1/devices/"+stale, &one)
	check("detail stale", one, "poly_lens", "offline")
	one = dev{}
	get("/api/v1/devices/"+collDev, &one)
	check("detail collector", one, "collector", "online")

	var tel struct {
		DeviceID string `json:"device_id"`
		Status   string `json:"status"`
	}
	get("/api/v1/devices/"+stale+"/telemetry", &tel)
	if tel.DeviceID != stale || tel.Status != "offline" {
		t.Errorf("telemetry = %+v", tel)
	}

	var status struct{ Total, Online, Offline, Unknown int }
	get("/api/v1/status", &status)
	if status.Total != 3 || status.Online != 2 || status.Offline != 1 || status.Unknown != 0 {
		t.Errorf("status = %+v, want 3 total / 2 online / 1 offline", status)
	}

	var uptime []struct {
		DeviceID      string `json:"device_id"`
		CurrentStatus string `json:"current_status"`
	}
	get("/api/v1/reports/device-uptime?days=7", &uptime)
	if len(uptime) != 3 {
		t.Fatalf("uptime report = %+v, want 3 devices", uptime)
	}
	for _, u := range uptime {
		want := map[string]string{collDev: "online", fresh: "online", stale: "offline"}[u.DeviceID]
		if u.CurrentStatus != want {
			t.Errorf("uptime report %s = %s, want %s", u.DeviceID, u.CurrentStatus, want)
		}
	}

	// Helpdesk overview (vendor, cross-tenant): this customer's row counts the Lens devices.
	req := httptest.NewRequest("GET", "/api/v1/helpdesk/overview", nil)
	rec := httptest.NewRecorder()
	h.HelpdeskOverview(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HelpdeskOverview = %d %s", rec.Code, rec.Body)
	}
	var ov []struct {
		ID             string `json:"id"`
		DevicesTotal   int    `json:"devices_total"`
		DevicesOnline  int    `json:"devices_online"`
		DevicesOffline int    `json:"devices_offline"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range ov {
		if o.ID == cust {
			found = true
			if o.DevicesTotal != 3 || o.DevicesOnline != 2 || o.DevicesOffline != 1 {
				t.Errorf("helpdesk row = %+v, want 3/2/1", o)
			}
		}
	}
	if !found {
		t.Error("customer missing from helpdesk overview")
	}
}
