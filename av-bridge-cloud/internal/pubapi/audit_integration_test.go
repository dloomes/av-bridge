package pubapi

// Integration test for GET /pub/v1/audit against a real Postgres. Same
// TEST_DATABASE_URL harness as devices_integration_test.go.

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
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestAuditEndpoint(t *testing.T) {
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

	var cust, other string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('audit-test') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('audit-other') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	ins := func(customer, actor, action, kind, id, relKind, relID, after, ago string) string {
		t.Helper()
		var out string
		err := su.QueryRow(ctx, `
			INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id,
			                       related_target_kind, related_target_id, "after", ts)
			VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,'')::jsonb,
			        '2026-10-01T12:00:00Z'::timestamptz - $9::interval)
			RETURNING id::text`, customer, actor, action, kind, id, relKind, relID, after, ago).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	const dev = "11111111-1111-1111-1111-111111111111"
	upd := ins(cust, "admin@example.test", "device.update", "device", dev, "", "", `{"name":"Display"}`, "1 hour")
	cmd := ins(cust, "operator@example.test", "command.submit", "command", "c-1", "device", dev, `{"command":"power_on"}`, "2 hours")
	usr := ins(cust, "admin@example.test", "user.create", "user", "u-1", "", "", "", "3 hours")
	// Two entries with the same timestamp, to check the id tiebreak.
	dc1 := ins(cust, "admin@example.test", "device.create", "device", "22222222-2222-2222-2222-222222222222", "", "", "", "4 hours")
	dc2 := ins(cust, "admin@example.test", "device.create", "device", "33333333-3333-3333-3333-333333333333", "", "", "", "4 hours")
	ins(other, "them@example.test", "device.update", "device", dev, "", "", "", "1 hour")
	// Newest first; the two same-time entries tie-break on id, highest first.
	all := []string{upd, cmd, usr, dc2, dc1}

	h := New(st, quiet)
	get := func(path string) (int, []string, *string, []map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(withPrincipal(req.Context(), Principal{CustomerID: cust}))
		rec := httptest.NewRecorder()
		h.ListAudit(rec, req)
		var page struct {
			Data       []map[string]any `json:"data"`
			NextCursor *string          `json:"next_cursor"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		var ids []string
		for _, e := range page.Data {
			ids = append(ids, e["id"].(string))
		}
		return rec.Code, ids, page.NextCursor, page.Data
	}
	set := func(ids []string) string {
		c := append([]string{}, ids...)
		sort.Strings(c)
		return strings.Join(c, ",")
	}

	code, ids, _, data := get("/pub/v1/audit")
	if code != http.StatusOK || strings.Join(ids, ",") != strings.Join(all, ",") {
		t.Fatalf("all: %d %v, want %v in order", code, ids, all)
	}
	if data[0]["action"] != "device.update" || data[0]["actor"] != "admin@example.test" ||
		data[0]["timestamp"] == nil || data[0]["after"].(map[string]any)["name"] != "Display" {
		t.Errorf("first entry %v", data[0])
	}
	if data[1]["related_target_kind"] != "device" || data[1]["related_target_id"] != dev {
		t.Errorf("command entry %v", data[1])
	}
	if _, has := data[2]["after"]; has {
		t.Errorf("empty after should be omitted: %v", data[2])
	}

	for p, want := range map[string][]string{
		"/pub/v1/audit?action=device.update":                                  {upd},
		"/pub/v1/audit?action=device.":                                        {upd, dc1, dc2},
		"/pub/v1/audit?target_kind=user":                                      {usr},
		"/pub/v1/audit?target_id=" + dev:                                      {upd, cmd},
		"/pub/v1/audit?target_id=" + dev + "&target_kind=device":              {upd, cmd},
		"/pub/v1/audit?target_id=" + dev + "&target_kind=command":             {},
		"/pub/v1/audit?actor=operator@example.test":                           {cmd},
		"/pub/v1/audit?since=2026-10-01T09:30:00Z":                            {upd, cmd},
		"/pub/v1/audit?until=2026-10-01T09:30:00Z":                            {usr, dc1, dc2},
		"/pub/v1/audit?since=2026-10-01T09:00:00Z&until=2026-10-01T11:00:00Z": {cmd, usr},
		"/pub/v1/audit?action=nothing.matches":                                {},
	} {
		code, ids, _, _ := get(p)
		if code != http.StatusOK || set(ids) != set(want) {
			t.Errorf("GET %s: %d %v, want %v", p, code, ids, want)
		}
	}

	for _, p := range []string{
		"/pub/v1/audit?since=yesterday",
		"/pub/v1/audit?until=2026-10-01",
		"/pub/v1/audit?cursor=garbage",
		"/pub/v1/audit?cursor=" + EncodeCursor(Cursor{TS: &cursorTime, ID: "not-a-number"}),
	} {
		if code, _, _, _ := get(p); code != http.StatusBadRequest {
			t.Errorf("GET %s: %d, want 400", p, code)
		}
	}

	// Paging at limit=1 visits every entry once, in order, across the tie.
	var paged []string
	next := ""
	for i := 0; i < len(all)+2; i++ {
		p := "/pub/v1/audit?limit=1"
		if next != "" {
			p += "&cursor=" + url.QueryEscape(next)
		}
		code, ids, nc, _ := get(p)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d", p, code)
		}
		paged = append(paged, ids...)
		if nc == nil {
			break
		}
		next = *nc
	}
	if strings.Join(paged, ",") != strings.Join(all, ",") {
		t.Errorf("paged %v, want %v", paged, all)
	}
}

// cursorTime is any valid timestamp for building a cursor.
var cursorTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
