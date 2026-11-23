package pubapi

// Integration test: devices with no Collector (Poly Lens managed) appear in
// the Public API device endpoints with source and platform. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres.

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
)

func TestIntegrationDeviceInPublicAPI(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping public API integration device test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('pub-integ-fake') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc, last_seen_at) VALUES ($1, 'col', '\x00'::bytea, now()) RETURNING id::text`, cust)
	collDev := q(`INSERT INTO devices (customer_id, collector_id, name, reported_id, latest_status, last_seen_at)
	              VALUES ($1, $2, 'collector device', 'cd', 'online', now()) RETURNING id::text`, cust, col)
	lens := func(name, age string) string {
		return q(`INSERT INTO devices (customer_id, source, platform, name, latest_status, last_seen_at)
		          VALUES ($1, 'poly_lens', 'android', $2, 'online', now() - $3::interval) RETURNING id::text`, cust, name, age)
	}
	fresh := lens("lens fresh", "0 minutes")
	stale := lens("lens stale", "16 minutes")

	h := New(st, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pub/v1/devices", h.ListDevices)
	mux.HandleFunc("GET /pub/v1/devices/{id}", h.GetDevice)
	mux.HandleFunc("GET /pub/v1/devices/{id}/telemetry", h.GetDeviceTelemetry)
	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	list := func(path string) map[string]map[string]any {
		body := get(path)
		items, _ := body["data"].([]any)
		if items == nil {
			items, _ = body["items"].([]any)
		}
		out := map[string]map[string]any{}
		for _, it := range items {
			m, _ := it.(map[string]any)
			out[m["id"].(string)] = m
		}
		return out
	}
	devs := list("/pub/v1/devices")
	if len(devs) != 3 {
		t.Fatalf("list = %v, want 3 devices", devs)
	}
	if d := devs[collDev]; d["source"] != "collector" || d["platform"] != nil || d["status"] != "online" {
		t.Errorf("collector device = %v", d)
	}
	if d := devs[fresh]; d["source"] != "poly_lens" || d["platform"] != "android" || d["status"] != "online" {
		t.Errorf("fresh lens device = %v", d)
	}
	if d := devs[stale]; d["source"] != "poly_lens" || d["status"] != "offline" {
		t.Errorf("stale lens device = %v", d)
	}

	// Status filter uses the same effective status.
	if got := list("/pub/v1/devices?status=offline"); len(got) != 1 || got[stale] == nil {
		t.Errorf("status=offline = %v, want only the stale lens device", got)
	}
	if got := list("/pub/v1/devices?status=online"); len(got) != 2 || got[fresh] == nil || got[collDev] == nil {
		t.Errorf("status=online = %v, want collector + fresh lens device", got)
	}

	if d := get("/pub/v1/devices/" + fresh); d["source"] != "poly_lens" || d["platform"] != "android" || d["status"] != "online" {
		t.Errorf("GET fresh = %v", d)
	}
	if d := get("/pub/v1/devices/" + stale); d["status"] != "offline" {
		t.Errorf("GET stale = %v", d)
	}
	if d := get("/pub/v1/devices/" + stale + "/telemetry"); d["status"] != "offline" {
		t.Errorf("telemetry stale = %v", d)
	}
}
