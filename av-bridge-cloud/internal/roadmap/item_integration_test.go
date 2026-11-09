package roadmap

// Integration test for UpdateItem's cell locks. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// Moving an item takes the cell lock on both its old and its new cell, so two
// moves in opposite directions serialise instead of deadlocking.
func TestUpdateItemMoveLocksBothCells(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set - skipping roadmap item test")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	unlock, err := TestLock(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	admin := connectAs(t, ctx, dsn, "app_admin", "app_admin_dev")

	var theme, item string
	if err := admin.QueryRow(ctx, `INSERT INTO roadmap_themes (position, name) VALUES (98, 'Lock test') RETURNING id::text`).Scan(&theme); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `INSERT INTO roadmap_items (theme_id, period_position, status, title, description)
		VALUES ($1, 0, 'planned', 'Lock test', 'Lock test') RETURNING id::text`, theme).Scan(&item); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, `DELETE FROM roadmap_items WHERE theme_id = $1`, theme)
		admin.Exec(ctx, `DELETE FROM roadmap_themes WHERE id = $1`, theme)
	})

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	to := 2
	if err := UpdateItem(ctx, tx, item, ItemInput{Period: &to}, "editor@involve.example"); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	for _, period := range []int{0, 2} {
		var held bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted
			  AND objid = hashtext('roadmap_cell:' || $1::text || ':' || $2::int)::oid)`, theme, period).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if !held {
			t.Errorf("cell lock for period %d not held after the move", period)
		}
	}
}
