package portalapi

// Integration test: write and command paths refuse cleanly for devices with
// no Collector (Poly Lens managed). Runs only with TEST_DATABASE_URL.

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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/remote"
)

func TestNoCollectorRefusals(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping no-collector refusal test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('no-collector-fake') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1::uuid, 'nc-' || $1::text, 'nc', '\x00', now()) RETURNING id::text`, cust)
	col2 := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	           VALUES ($1::uuid, 'nc2-' || $1::text, 'nc2', '\x00', now()) RETURNING id::text`, cust)
	collDev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, address)
	              VALUES ($1, $2, 'cd', 'collector device', 'display', 'ping', '10.0.0.9') RETURNING id::text`, cust, col)
	lensDev := q(`INSERT INTO devices (customer_id, source, platform, name, type, protocol, latest_status, last_seen_at)
	              VALUES ($1, 'poly_lens', 'windows', 'lens device', 'conferencing', 'poly_lens', 'online', now())
	              RETURNING id::text`, cust)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	pr := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000e2", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet).SetRemoteAccess(remote.NewService(st.AdminPool(), nil, "access.test", quiet))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /devices/{id}/command", h.SubmitCommand)
	mux.HandleFunc("POST /devices/{id}/reconnect", h.SubmitReconnect)
	mux.HandleFunc("POST /commands/bulk", h.SubmitBulkCommand)
	mux.HandleFunc("POST /devices/move", h.MoveDevices)
	mux.HandleFunc("POST /devices/{id}/remote-sessions", h.StartRemoteSession)
	call := func(path string, body any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), pr))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	const want = `{"error":"not available for devices managed by Poly Lens"}`
	refused := func(where string, code int, body []byte) {
		t.Helper()
		if code != http.StatusConflict || string(bytes.TrimSpace(body)) != want {
			t.Errorf("%s = %d %s, want 409 %s", where, code, body, want)
		}
	}
	cmdCount := func() string {
		return q(`SELECT count(*)::text FROM commands WHERE device_id = $1`, lensDev)
	}

	code, body := call("/devices/"+lensDev+"/command", map[string]any{"name": "power_on"})
	refused("command", code, body)
	code, body = call("/devices/"+lensDev+"/reconnect", map[string]any{})
	refused("reconnect", code, body)
	if n := cmdCount(); n != "0" {
		t.Errorf("commands rows for the Lens device = %s, want 0", n)
	}

	// Bulk: the Collector device is queued, the Lens device skipped.
	code, body = call("/commands/bulk", map[string]any{"device_ids": []string{collDev, lensDev}, "name": "power_on"})
	if code != 200 {
		t.Fatalf("bulk = %d %s", code, body)
	}
	var bulk struct {
		Results []struct {
			DeviceID  string `json:"device_id"`
			CommandID string `json:"command_id"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &bulk); err != nil || len(bulk.Results) != 2 {
		t.Fatalf("bulk results: %v %s", err, body)
	}
	for _, r := range bulk.Results {
		switch r.DeviceID {
		case collDev:
			if r.CommandID == "" || r.Error != "" {
				t.Errorf("bulk collector device = %+v", r)
			}
		case lensDev:
			if r.CommandID != "" || r.Error != "not available for devices managed by Poly Lens" {
				t.Errorf("bulk lens device = %+v", r)
			}
		}
	}
	if n := cmdCount(); n != "0" {
		t.Errorf("commands rows for the Lens device after bulk = %s, want 0", n)
	}

	code, body = call("/devices/move", map[string]any{"device_ids": []string{lensDev}, "target_collector_id": col2})
	refused("move", code, body)
	if got := q(`SELECT COALESCE(collector_id::text, 'none') FROM devices WHERE id = $1`, lensDev); got != "none" {
		t.Errorf("Lens device collector after refused move = %s", got)
	}

	code, body = call("/devices/"+lensDev+"/remote-sessions", map[string]any{})
	refused("remote access", code, body)
}
