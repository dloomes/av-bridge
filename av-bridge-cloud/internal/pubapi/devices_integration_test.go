package pubapi

// Integration test for the device endpoints against a real Postgres, so
// SQL-shape bugs (like the misplaced latest_metrics column that made
// GET /pub/v1/devices/{id} return 500) are caught before deploy.
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name pubtest -e POSTGRES_PASSWORD=pgtest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55434/postgres?sslmode=disable \
//	  go test ./internal/pubapi/ -run TestDeviceEndpoints -v

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

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestDeviceEndpoints(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping public API integration test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('pubapi-test') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('pubapi-other') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc, last_seen_at) VALUES ($1, 'col', '\x00'::bytea, now()) RETURNING id::text`, cust)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'r') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'l') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'b') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Court 3') RETURNING id::text`, cust, bld)
	dev := q(`INSERT INTO devices (customer_id, collector_id, room_id, name, reported_id, latest_status, last_seen_at, latest_metrics, tags)
	          VALUES ($1, $2, $3, 'Display', 'disp', 'online', now(), '{"model":"65BDL3052E","power_status":"on"}', '{"serial_number":"SN1"}')
	          RETURNING id::text`, cust, col, room)
	otherCol := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c2', '\x00'::bytea) RETURNING id::text`, other)
	otherDev := q(`INSERT INTO devices (customer_id, collector_id, name, reported_id) VALUES ($1, $2, 'Theirs', 'x') RETURNING id::text`, other, otherCol)

	h := New(st, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pub/v1/devices", h.ListDevices)
	mux.HandleFunc("GET /pub/v1/devices/{id}", h.GetDevice)
	mux.HandleFunc("GET /pub/v1/devices/{id}/telemetry", h.GetDeviceTelemetry)
	mux.HandleFunc("GET /pub/v1/devices/{id}/events", h.GetDeviceEvents)
	call := func(path string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, body := call("/pub/v1/devices/" + dev)
	if code != http.StatusOK {
		t.Fatalf("GET device: %d %v", code, body)
	}
	if body["id"] != dev || body["room"] != "Court 3" || body["model"] != "65BDL3052E" || body["serial_number"] != "SN1" {
		t.Errorf("device body %v", body)
	}
	if m, _ := body["metrics"].(map[string]any); m["power_status"] != "on" {
		t.Errorf("metrics %v", body["metrics"])
	}

	for _, p := range []string{"/pub/v1/devices/" + dev + "/telemetry", "/pub/v1/devices/" + dev + "/events", "/pub/v1/devices"} {
		if code, body := call(p); code != http.StatusOK {
			t.Errorf("GET %s: %d %v", p, code, body)
		}
	}

	for name, p := range map[string]string{
		"unknown uuid":       "/pub/v1/devices/00000000-0000-0000-0000-000000000000",
		"other tenant":       "/pub/v1/devices/" + otherDev,
		"malformed id":       "/pub/v1/devices/not-a-uuid",
		"malformed telem":    "/pub/v1/devices/123/telemetry",
		"malformed events":   "/pub/v1/devices/123/events",
		"other tenant telem": "/pub/v1/devices/" + otherDev + "/telemetry",
	} {
		if code, body := call(p); code != http.StatusNotFound {
			t.Errorf("%s: %d %v, want 404", name, code, body)
		}
	}
}
