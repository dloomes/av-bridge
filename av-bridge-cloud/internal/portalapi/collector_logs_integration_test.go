package portalapi

// Integration test for reading Collector logs. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16 (see
// device_move_integration_test.go for the docker line).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestListCollectorLogs(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping collector logs test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('collector-logs-read') RETURNING id::text`)
	other := q(`INSERT INTO customers (name) VALUES ('collector-logs-other') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'clr-' || $1::text, 'Leeds Collector', '\x00') RETURNING id::text`, cust)
	otherCol := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	               VALUES ($1::uuid, 'clr-' || $1::text, 'other', '\x00') RETURNING id::text`, other)
	line := func(c, cu, level, msg, attrs string, minsAgo int) {
		q(`INSERT INTO collector_logs (customer_id, collector_id, ts, level, msg, attrs)
		   VALUES ($1, $2, now() - make_interval(mins => $6), $3, $4, $5::jsonb) RETURNING id::text`, cu, c, level, msg, attrs, minsAgo)
	}
	line(col, cust, "INFO", "collector started", `{"version": "v1.0.55"}`, 30)
	line(col, cust, "WARN", "device unreachable", `{"device": "dsp-1"}`, 20)
	line(col, cust, "INFO", "device connected", `{"device": "dsp-1"}`, 10)
	line(col, cust, "ERROR", "update failed", `{"error": "100%_disk"}`, 5)
	line(otherCol, other, "ERROR", "someone else's", `null`, 1)

	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000d4", CustomerID: cust,
		Permissions: map[string]struct{}{portalauth.PermViewCollectorLogs: {}}}
	h := New(st, nil, nil, nil, nil, quiet)
	get := func(id, query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/collectors/"+id+"/logs?"+query, nil)
		req.SetPathValue("id", id)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		h.ListCollectorLogs(rec, req)
		return rec
	}
	type page struct {
		Logs []struct {
			ID    int64  `json:"id"`
			Level string `json:"level"`
			Msg   string `json:"msg"`
		} `json:"logs"`
		NextBefore *int64 `json:"next_before"`
	}
	list := func(query string) page {
		t.Helper()
		rec := get(col, query)
		if rec.Code != 200 {
			t.Fatalf("%s = %d %s", query, rec.Code, rec.Body)
		}
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	msgs := func(p page) string {
		var s []string
		for _, l := range p.Logs {
			s = append(s, l.Msg)
		}
		return strings.Join(s, ", ")
	}

	if got := msgs(list("")); got != "update failed, device connected, device unreachable, collector started" {
		t.Errorf("all = %s", got)
	}
	if got := msgs(list("level=warn")); got != "update failed, device unreachable" {
		t.Errorf("warn and above = %s", got)
	}
	if got := msgs(list("q=dsp-1")); got != "device connected, device unreachable" {
		t.Errorf("search in details = %s", got)
	}
	if got := msgs(list("q=100%25_")); got != "update failed" {
		t.Errorf("search with %% and _ taken literally = %s", got)
	}
	p1 := list("limit=3")
	if len(p1.Logs) != 3 || p1.NextBefore == nil {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := list("limit=3&before=" + jsonNum(*p1.NextBefore))
	if msgs(p2) != "collector started" || p2.NextBefore != nil {
		t.Errorf("page 2 = %+v", p2)
	}

	rec := get(col, "format=text&level=warn")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="Leeds-Collector-logs-`) {
		t.Fatalf("download = %d %v", rec.Code, rec.Header())
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "WARN  device unreachable device=dsp-1") ||
		!strings.Contains(lines[1], "ERROR update failed error=100%_disk") {
		t.Errorf("download, oldest first:\n%s", rec.Body)
	}

	if rec := get(otherCol, ""); rec.Code != 404 {
		t.Errorf("another organisation's Collector = %d, want 404", rec.Code)
	}
	if rec := get(col, "level=loud"); rec.Code != 400 {
		t.Errorf("bad level = %d, want 400", rec.Code)
	}
}

func jsonNum(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
