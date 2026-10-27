package pubapi

// Integration test for firmware in the public API: the device fields,
// the firmware_status filter and GET /pub/v1/firmware. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16 (see
// devices_integration_test.go for the docker line).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestFirmwareEndpoints(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping public API firmware test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('pubapi-firmware') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('pubapi-firmware-other') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc, last_seen_at) VALUES ($1, 'col', '\x00'::bytea, now()) RETURNING id::text`, cust)
	q(`INSERT INTO firmware_targets (customer_id, make, model, target_version) VALUES ($1, 'Sony', 'FW-65BZ40L', 'PKG6.6510') RETURNING id::text`, cust)
	// device seeds one device: make, model, then where its firmware is.
	device := func(name, make, model, column, metrics, tags string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, name, reported_id, make, model, firmware_version, latest_metrics, tags, last_seen_at)
		          VALUES ($1, $2, $3, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7::jsonb, $8::jsonb, now())
		          RETURNING id::text`, cust, col, name, make, model, column, metrics, tags)
	}
	onTarget := device("On target", "Sony", "FW-65BZ40L", "", `{"firmware_version":"PKG6.6510"}`, `{}`)
	behind := device("Behind", "Sony", "FW-65BZ40L", "", `{"fw_version":"PKG6.5603"}`, `{}`) // the VPX / DIDO reading name
	stored := device("Stored", "Sony", "FW-65BZ40L", "PKG6.6510", `{}`, `{}`)                // set by ingest
	override := device("Override", "Sony", "FW-65BZ40L", "", `{"firmware_version":"PKG6.5603"}`, `{"firmware_version":"PKG6.6510"}`)
	silent := device("Silent", "Sony", "FW-65BZ40L", "", `{}`, `{}`)
	noTarget := device("Encoder", "Aurora", "VPX-TX", "", `{"fw_version":"3.2.14"}`, `{}`)
	otherCol := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c2', '\x00'::bytea) RETURNING id::text`, other)
	q(`INSERT INTO devices (customer_id, collector_id, name, reported_id, make, model, latest_metrics)
	   VALUES ($1, $2, 'Theirs', 'x', 'Sony', 'FW-65BZ40L', '{"firmware_version":"PKG1"}') RETURNING id::text`, other, otherCol)

	h := New(st, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pub/v1/devices", h.ListDevices)
	mux.HandleFunc("GET /pub/v1/devices/{id}", h.GetDevice)
	mux.HandleFunc("GET /pub/v1/firmware", h.ListFirmware)
	call := func(path string, into any) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		_ = json.Unmarshal(rec.Body.Bytes(), into)
		return rec.Code
	}

	// Each device's firmware, target and status.
	for _, c := range []struct {
		id, firmware, status string
	}{
		{onTarget, "PKG6.6510", "matches_target"},
		{behind, "PKG6.5603", "differs_from_target"},
		{stored, "PKG6.6510", "matches_target"},
		{override, "PKG6.6510", "matches_target"},
		{silent, "", "not_reported"},
		{noTarget, "3.2.14", "no_target"},
	} {
		var d map[string]any
		if code := call("/pub/v1/devices/"+c.id, &d); code != http.StatusOK {
			t.Fatalf("GET device: %d %v", code, d)
		}
		if fw, _ := d["firmware_version"].(string); fw != c.firmware || d["firmware_status"] != c.status {
			t.Errorf("%v: firmware %q status %v, want %q %s", d["name"], fw, d["firmware_status"], c.firmware, c.status)
		}
		if d["make"] == "Sony" && d["target_firmware_version"] != "PKG6.6510" {
			t.Errorf("%v: target %v", d["name"], d["target_firmware_version"])
		}
	}

	// The filter lists the devices to update.
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if code := call("/pub/v1/devices?firmware_status=differs_from_target", &page); code != http.StatusOK || len(page.Data) != 1 || page.Data[0]["id"] != behind {
		t.Errorf("differs_from_target filter: %d %v", code, page.Data)
	}
	var e map[string]any
	if code := call("/pub/v1/devices?firmware_status=old", &e); code != http.StatusBadRequest {
		t.Errorf("bad firmware_status: %d", code)
	}

	// The summary: a row per make, model and version, only this tenant's.
	var rows []publicFirmwareRow
	if code := call("/pub/v1/firmware", &rows); code != http.StatusOK {
		t.Fatalf("GET firmware: %d", code)
	}
	got := []string{}
	for _, r := range rows {
		got = append(got, strings.Join([]string{r.Make, r.Model, r.FirmwareVersion, r.FirmwareStatus, itoa(r.DeviceCount)}, "|"))
	}
	sort.Strings(got)
	want := []string{
		"Aurora|VPX-TX|3.2.14|no_target|1",
		"Sony|FW-65BZ40L|PKG6.5603|differs_from_target|1",
		"Sony|FW-65BZ40L|PKG6.6510|matches_target|3",
		"Sony|FW-65BZ40L||not_reported|1",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("firmware summary:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	rows = nil
	if code := call("/pub/v1/firmware?firmware_status=differs_from_target", &rows); code != http.StatusOK || len(rows) != 1 || rows[0].DeviceCount != 1 {
		t.Errorf("summary filter: %d %v", code, rows)
	}
}
