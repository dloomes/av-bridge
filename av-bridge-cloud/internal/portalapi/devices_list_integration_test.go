package portalapi

// Integration test for the Devices list's reading-derived fields (power and
// call pills). Runs only when TEST_DATABASE_URL points at a disposable
// Postgres 16 (see device_move_integration_test.go for the docker line).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestListDevicesCallAndPowerReadings(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping device list test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('device-list') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'dl-' || $1::text, 'dl', '\x00') RETURNING id::text`, cust)
	device := func(name, typ, metrics string) {
		q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, latest_metrics)
		   VALUES ($1, $2, $3, $3, $4, 'ping', $5::jsonb) RETURNING id::text`, cust, col, name, typ, metrics)
	}
	device("a-codec-in-call", "conferencing", `{"active_calls": 2, "call_state": "connected"}`)
	device("b-codec-idle", "conferencing", `{"active_calls": 0, "call_state": "idle"}`)
	device("c-display", "display", `{"power_status": "standby", "active_calls": "n/a"}`)
	device("d-no-readings", "audio", `null`)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000d2", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	req := httptest.NewRequest("GET", "/api/v1/devices", nil)
	req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
	rec := httptest.NewRecorder()
	h.ListDevices(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ListDevices = %d %s", rec.Code, rec.Body)
	}
	var got []struct {
		Name        string `json:"name"`
		PowerStatus string `json:"power_status"`
		ActiveCalls *int   `json:"active_calls"`
		CallState   string `json:"call_state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("devices = %+v, want 4", got)
	}
	if d := got[0]; d.ActiveCalls == nil || *d.ActiveCalls != 2 || d.CallState != "connected" {
		t.Errorf("codec in call = %+v", d)
	}
	if d := got[1]; d.ActiveCalls == nil || *d.ActiveCalls != 0 || d.CallState != "idle" {
		t.Errorf("idle codec = %+v", d)
	}
	if d := got[2]; d.ActiveCalls != nil || d.CallState != "" || d.PowerStatus != "standby" {
		t.Errorf("display = %+v; a non-numeric active_calls is left out", d)
	}
	if d := got[3]; d.ActiveCalls != nil || d.CallState != "" || d.PowerStatus != "" {
		t.Errorf("device without readings = %+v", d)
	}
}
