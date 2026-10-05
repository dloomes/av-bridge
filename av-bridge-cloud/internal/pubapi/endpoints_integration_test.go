package pubapi

// End-to-end check of every /pub/v1 endpoint against a real Postgres:
// each route and filter answers without a 500, paginated routes walk
// every item exactly once at limit=1, and another tenant's rows never
// appear. Same TEST_DATABASE_URL harness as devices_integration_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestAllEndpoints(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// Tenant under test, with a small hierarchy.
	cust := q(`INSERT INTO customers (name) VALUES ('pub-all') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc, last_seen_at) VALUES ($1, 'col-a', '\x00'::bytea, now()) RETURNING id::text`, cust)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Site') RETURNING id::text`, cust, reg)
	b1 := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B1') RETURNING id::text`, cust, loc)
	b2 := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B2') RETURNING id::text`, cust, loc)
	r1 := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'R1') RETURNING id::text`, cust, b1)
	r2 := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'R2') RETURNING id::text`, cust, b2)

	// Four devices: distinct last_seen_at, one that has never reported
	// (NULL), and one tie on the timestamp to test the id tiebreak.
	devs := []string{
		q(`INSERT INTO devices (customer_id, collector_id, room_id, name, reported_id, latest_status, last_seen_at) VALUES ($1,$2,$3,'D1','d1','online', now() - interval '1 minute') RETURNING id::text`, cust, col, r1),
		q(`INSERT INTO devices (customer_id, collector_id, room_id, name, reported_id, latest_status, last_seen_at) VALUES ($1,$2,$3,'D2','d2','offline', now() - interval '2 minutes') RETURNING id::text`, cust, col, r1),
		q(`INSERT INTO devices (customer_id, collector_id, room_id, name, reported_id, latest_status, last_seen_at) VALUES ($1,$2,$3,'D3','d3','online', now() - interval '2 minutes') RETURNING id::text`, cust, col, r2),
		q(`INSERT INTO devices (customer_id, collector_id, name, reported_id) VALUES ($1,$2,'D4','d4') RETURNING id::text`, cust, col),
		q(`INSERT INTO devices (customer_id, collector_id, name, reported_id) VALUES ($1,$2,'D5','d5') RETURNING id::text`, cust, col),
	}
	exec(`UPDATE devices SET last_seen_at = (SELECT last_seen_at FROM devices WHERE id = $1) WHERE id = $2`, devs[1], devs[2])
	for i, d := range devs[:3] {
		for j := 0; j < 2; j++ {
			exec(`INSERT INTO events (customer_id, device_id, event_type, payload, ts) VALUES ($1, $2, 'status_change', '{"n":1}', now() - make_interval(mins => $3))`, cust, d, i*10+j)
		}
	}
	exec(`INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, status, opened_at) VALUES ($1,$2,'device_offline','critical','down','open', now() - interval '5 minutes')`, cust, devs[1])
	exec(`INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, status, opened_at, resolved_at, resolved_by) VALUES ($1,$2,'device_degraded','warning','slow','resolved', now() - interval '9 minutes', now(), 'auto:recovered')`, cust, devs[0])
	exec(`INSERT INTO alerts (customer_id, collector_id, alert_key, severity, message, status, payload) VALUES ($1,$2,'collector_offline','critical','quiet','open', NULL)`, cust, col)
	a1 := q(`INSERT INTO assets (customer_id, room_id, name, category, status) VALUES ($1,$2,'Screen','display','in_service') RETURNING id::text`, cust, r1)
	q(`INSERT INTO assets (customer_id, room_id, name, category, status) VALUES ($1,$2,'Spare remote','accessory','in_storage') RETURNING id::text`, cust, r2)
	q(`INSERT INTO assets (customer_id, name, category, status) VALUES ($1,'Cable','cable','in_service') RETURNING id::text`, cust)

	// Another tenant whose rows must never appear.
	other := q(`INSERT INTO customers (name) VALUES ('pub-other') RETURNING id::text`)
	ocol := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'col-o', '\x00'::bytea) RETURNING id::text`, other)
	odev := q(`INSERT INTO devices (customer_id, collector_id, name, reported_id, last_seen_at) VALUES ($1,$2,'Theirs','o1', now()) RETURNING id::text`, other, ocol)
	exec(`INSERT INTO events (customer_id, device_id, event_type, ts) VALUES ($1,$2,'x', now())`, other, odev)
	exec(`INSERT INTO alerts (customer_id, device_id, alert_key, severity, message) VALUES ($1,$2,'device_offline','critical','x')`, other, odev)
	oasset := q(`INSERT INTO assets (customer_id, name, category) VALUES ($1,'Theirs','display') RETURNING id::text`, other)

	h := New(st, quiet)
	mux := http.NewServeMux()
	for pattern, fn := range map[string]http.HandlerFunc{
		"GET /pub/v1/ping":                   h.Ping,
		"GET /pub/v1/openapi.json":           h.OpenAPISpec,
		"GET /pub/v1/docs":                   h.SwaggerUI,
		"GET /pub/v1/devices":                h.ListDevices,
		"GET /pub/v1/devices/{id}":           h.GetDevice,
		"GET /pub/v1/devices/{id}/telemetry": h.GetDeviceTelemetry,
		"GET /pub/v1/devices/{id}/events":    h.GetDeviceEvents,
		"GET /pub/v1/buildings":              h.ListBuildings,
		"GET /pub/v1/rooms":                  h.ListRooms,
		"GET /pub/v1/assets":                 h.ListAssets,
		"GET /pub/v1/assets/{id}":            h.GetAsset,
		"GET /pub/v1/alerts":                 h.ListAlerts,
		"GET /pub/v1/events":                 h.ListEvents,
		"GET /pub/v1/audit":                  h.ListAudit,
	} {
		mux.HandleFunc(pattern, fn)
	}
	get := func(path string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust, TokenName: "test"}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	// ids returns the "id" of each item in data, for lists or {data: [...]} pages.
	ids := func(body []byte) ([]string, *string) {
		t.Helper()
		var page struct {
			Data       []map[string]any `json:"data"`
			NextCursor *string          `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil || page.Data == nil {
			var flat []map[string]any
			if err2 := json.Unmarshal(body, &flat); err2 != nil {
				t.Fatalf("unparseable body %s", body)
			}
			page.Data = flat
		}
		var out []string
		for _, it := range page.Data {
			out = append(out, fmt.Sprint(it["id"]))
		}
		return out, page.NextCursor
	}

	// 1. Every route and filter answers 200.
	ok200 := []string{
		"/pub/v1/ping", "/pub/v1/openapi.json", "/pub/v1/docs",
		"/pub/v1/devices", "/pub/v1/devices?status=online", "/pub/v1/devices?building_id=" + b1, "/pub/v1/devices?room_id=" + r2,
		"/pub/v1/devices?building_id=not-a-uuid", "/pub/v1/devices?status=nonsense",
		"/pub/v1/devices/" + devs[0], "/pub/v1/devices/" + devs[3], "/pub/v1/devices/" + devs[0] + "/telemetry", "/pub/v1/devices/" + devs[0] + "/events",
		"/pub/v1/devices/" + devs[3] + "/telemetry",
		"/pub/v1/buildings", "/pub/v1/rooms", "/pub/v1/rooms?building_id=" + b1, "/pub/v1/rooms?building_id=junk",
		"/pub/v1/assets", "/pub/v1/assets?category=display", "/pub/v1/assets?status=in_storage", "/pub/v1/assets?monitored=true",
		"/pub/v1/assets?monitored=false", "/pub/v1/assets?room_id=" + r1, "/pub/v1/assets?building_id=" + b2, "/pub/v1/assets?room_id=junk",
		"/pub/v1/assets/" + a1,
		"/pub/v1/alerts", "/pub/v1/alerts?status=open", "/pub/v1/alerts?severity=warning",
		"/pub/v1/events", "/pub/v1/events?device_id=" + devs[1], "/pub/v1/events?device_id=junk",
		"/pub/v1/audit", "/pub/v1/audit?action=device.", "/pub/v1/audit?target_id=" + devs[0],
	}
	for _, p := range ok200 {
		if code, body := get(p); code != http.StatusOK {
			t.Errorf("GET %s: %d %s", p, code, body)
		}
	}

	// 2. 4xx where expected, never 500.
	for p, want := range map[string]int{
		"/pub/v1/devices/not-a-uuid":                           404,
		"/pub/v1/devices/" + odev:                              404,
		"/pub/v1/devices/" + odev + "/telemetry":               404,
		"/pub/v1/assets/not-a-uuid":                            404,
		"/pub/v1/assets/" + oasset:                             404,
		"/pub/v1/devices?cursor=!!!":                           400,
		"/pub/v1/events?cursor=bm90LWpzb24":                    400,
		"/pub/v1/alerts?cursor=garbage":                        400,
		"/pub/v1/assets?cursor=garbage":                        400,
		"/pub/v1/devices/" + devs[0] + "/events?cursor=bad!!!": 400,
	} {
		if code, body := get(p); code != want {
			t.Errorf("GET %s: %d %s, want %d", p, code, body, want)
		}
	}

	// 3. Filters and tenant isolation return exactly the right rows.
	sorted := func(s []string) []string { c := append([]string{}, s...); sort.Strings(c); return c }
	expectIDs := func(path string, want []string) {
		t.Helper()
		_, body := get(path)
		got, _ := ids(body)
		if strings.Join(sorted(got), ",") != strings.Join(sorted(want), ",") {
			t.Errorf("GET %s: ids %v, want %v", path, got, want)
		}
	}
	expectIDs("/pub/v1/devices", devs)
	expectIDs("/pub/v1/devices?room_id="+r1, devs[:2])
	expectIDs("/pub/v1/devices?building_id="+b2, devs[2:3])
	expectIDs("/pub/v1/buildings", []string{b1, b2})
	expectIDs("/pub/v1/rooms?building_id="+b1, []string{r1})
	count := func(path string) int {
		t.Helper()
		_, body := get(path)
		got, _ := ids(body)
		return len(got)
	}
	for p, n := range map[string]int{
		"/pub/v1/events": 6, "/pub/v1/events?device_id=" + devs[1]: 2, "/pub/v1/devices/" + devs[2] + "/events": 2,
		"/pub/v1/alerts": 3, "/pub/v1/alerts?status=open": 2, "/pub/v1/alerts?severity=warning": 1,
		"/pub/v1/assets": 3, "/pub/v1/assets?category=display": 1, "/pub/v1/assets?room_id=" + r1: 1,
		"/pub/v1/devices?status=online": 2,
	} {
		if got := count(p); got != n {
			t.Errorf("GET %s: %d items, want %d", p, got, n)
		}
	}

	// 4. Paginated routes visit every item exactly once at limit=1.
	for path, total := range map[string]int{
		"/pub/v1/devices": 5, "/pub/v1/events": 6, "/pub/v1/alerts": 3, "/pub/v1/assets": 3,
		"/pub/v1/devices/" + devs[0] + "/events": 2,
	} {
		seen := map[string]bool{}
		next := ""
		for page := 0; page < total+3; page++ {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			p := path + sep + "limit=1"
			if next != "" {
				p += "&cursor=" + url.QueryEscape(next)
			}
			code, body := get(p)
			if code != http.StatusOK {
				t.Errorf("GET %s: %d %s", p, code, body)
				break
			}
			got, nc := ids(body)
			for _, id := range got {
				if seen[id] {
					t.Errorf("%s: item %s returned twice", path, id)
				}
				seen[id] = true
			}
			if nc == nil {
				break
			}
			next = *nc
		}
		if len(seen) != total {
			t.Errorf("%s: paged through %d items, want %d", path, len(seen), total)
		}
	}
}
