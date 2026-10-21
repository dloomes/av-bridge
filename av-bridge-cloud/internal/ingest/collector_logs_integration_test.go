package ingest

// Integration test for storing the log lines a Collector sends with each
// push. Runs only when TEST_DATABASE_URL points at a disposable Postgres 16
// (see portalapi/device_move_integration_test.go for the docker line).

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestInsertCollectorLogs(t *testing.T) {
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
	cust := q(`INSERT INTO customers (name) VALUES ('collector-logs') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'cl-' || $1::text, 'cl', '\x00') RETURNING id::text`, cust)

	ts := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	logs := []logDTO{
		{TS: ts, Level: "INFO", Msg: "device connected", Attrs: map[string]any{"device": "dsp-1"}},
		{TS: ts, Level: "WARN+2", Msg: "slow"},
		{Level: "ERROR", Msg: strings.Repeat("é", maxLogMsg)}, // no time: stored as now; long: cut
		{TS: ts, Level: "INFO", Msg: "big", Attrs: map[string]any{"body": strings.Repeat("x", maxLogAttrs)}},
	}
	// As the tenant role, as ingest runs, so grants and RLS are exercised.
	if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
		return insertCollectorLogs(ctx, tx, cust, col, logs)
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got := q(`SELECT string_agg(level || ':' || left(msg, 20) || ':' || COALESCE(attrs->>'device', '-'), ' | ' ORDER BY id)
	            FROM collector_logs WHERE collector_id = $1`, col)
	want := "INFO:device connected:dsp-1 | WARN:slow:- | ERROR:" + strings.Repeat("é", 20) + ":- | INFO:big:-"
	if got != want {
		t.Errorf("rows = %q\nwant   %q", got, want)
	}
	if n := q(`SELECT count(*)::text FROM collector_logs WHERE collector_id = $1 AND msg = 'big' AND attrs IS NULL`, col); n != "1" {
		t.Errorf("oversized attrs should be dropped, not stored")
	}
	if n := q(`SELECT count(*)::text FROM collector_logs WHERE collector_id = $1 AND level = 'ERROR' AND ts > now() - interval '1 minute' AND char_length(msg) <= $2`, col, maxLogMsg+1); n != "1" {
		t.Errorf("a line without a time gets now, and a long message is cut")
	}
}
