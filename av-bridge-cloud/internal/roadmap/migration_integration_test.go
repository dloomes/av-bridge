package roadmap

// Integration test for migration 0078 (roadmap tables and seed). Runs only
// when TEST_DATABASE_URL points at a disposable Postgres 16.

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func connectAs(t *testing.T, ctx context.Context, dsn, user, pass string) *pgx.Conn {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	c, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return c
}

func TestRoadmapSeed(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set - skipping roadmap migration test")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin := connectAs(t, ctx, dsn, "app_admin", "app_admin_dev")
	tenant := connectAs(t, ctx, dsn, "app_tenant", "app_tenant_dev")

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	if n := count(`SELECT count(*) FROM roadmap_settings WHERE as_of = '2026-11-02'`); n != 1 {
		t.Errorf("settings rows with as_of 2026-11-02 = %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM roadmap_settings`); n != 1 {
		t.Errorf("settings rows = %d, want 1", n)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO roadmap_settings (id, title, headline, lede, as_of, disclaimer) VALUES (false, 't', 'h', 'l', now(), 'd')`); err == nil {
		t.Error("a second settings row was accepted")
	}

	rows, err := admin.Query(ctx, `SELECT position, name, months, is_current FROM roadmap_periods ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	var periods []string
	for rows.Next() {
		var pos int
		var name, months string
		var cur bool
		if err := rows.Scan(&pos, &name, &months, &cur); err != nil {
			t.Fatal(err)
		}
		periods = append(periods, strings.Join([]string{string(rune('0' + pos)), name, months}, "|"))
		if cur != (pos == 0) {
			t.Errorf("period %d is_current = %v", pos, cur)
		}
	}
	rows.Close()
	if len(periods) != 4 || periods[0] != "0|Now|Nov 2026 – Jan 2027" || !strings.HasPrefix(periods[3], "3|Later|") {
		t.Errorf("periods = %q", periods)
	}

	rows, err = admin.Query(ctx, `SELECT name FROM roadmap_themes ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	var themes []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		themes = append(themes, n)
	}
	rows.Close()
	if got := strings.Join(themes, ","); got != "Faster onboarding,Device coverage,Room operations,Integrations" {
		t.Errorf("themes = %s", got)
	}

	if n := count(`SELECT count(*) FROM roadmap_items`); n != 17 {
		t.Errorf("items = %d, want 17", n)
	}
	if n := count(`SELECT count(*) FROM roadmap_items i JOIN roadmap_themes t ON t.id = i.theme_id
		WHERE i.title = 'Firmware vulnerability check' AND i.status = 'exploring'
		  AND i.period_position = 3 AND t.name = 'Room operations'`); n != 1 {
		t.Errorf("Firmware vulnerability check not seeded as exploring / period 3 / Room operations")
	}
	if n := count(`SELECT count(*) FROM roadmap_items WHERE status = 'committed'`); n != 6 {
		t.Errorf("committed items = %d, want 6", n)
	}
	if n := count(`SELECT count(*) FROM roadmap_items WHERE release_month IS NOT NULL`); n != 6 {
		t.Errorf("items with a release month = %d, want 6", n)
	}
	if n := count(`SELECT count(*) FROM roadmap_shipped`); n != 7 {
		t.Errorf("shipped = %d, want 7", n)
	}

	// Themes in use cannot be deleted.
	if _, err := admin.Exec(ctx, `DELETE FROM roadmap_themes WHERE name = 'Integrations'`); err == nil {
		t.Error("a theme with items was deleted")
	}

	// The editor flag.
	var editor bool
	if err := admin.QueryRow(ctx, `SELECT column_default = 'false' FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'roadmap_editor'`).Scan(&editor); err != nil || !editor {
		t.Errorf("users.roadmap_editor missing or default not false: %v", err)
	}

	// Tenants read the content, never the share links.
	var n int
	if err := tenant.QueryRow(ctx, `SELECT count(*) FROM roadmap_items`).Scan(&n); err != nil || n < 17 {
		t.Errorf("app_tenant select roadmap_items: n=%d err=%v", n, err)
	}
	if _, err := tenant.Exec(ctx, `SELECT 1 FROM roadmap_share_links`); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("app_tenant on roadmap_share_links: %v, want permission denied", err)
	}
	if _, err := tenant.Exec(ctx, `UPDATE roadmap_items SET title = title`); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("app_tenant update roadmap_items: %v, want permission denied", err)
	}
}
