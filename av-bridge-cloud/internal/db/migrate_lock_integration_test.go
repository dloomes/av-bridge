package db_test

// Integration test: several copies of the service starting at once must not
// collide while applying migrations. Runs only when TEST_DATABASE_URL points
// at a disposable Postgres (superuser; a scratch database is created/dropped).

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestMigrateConcurrent(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping concurrent migrate test")
	}
	ctx := context.Background()
	const scratch = "migrate_lock_test"

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + scratch
	scratchDSN := u.String()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	drop := func() {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`)
	}
	drop()
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+scratch); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(drop)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	const workers = 4
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = db.Migrate(ctx, scratchDSN, quiet)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrate worker %d: %v", i, err)
		}
	}

	conn, err := pgx.Connect(ctx, scratchDSN)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob migrations: %v (%d files)", err, len(files))
	}
	if want := len(files); n != want {
		t.Errorf("schema_migrations rows = %d, want %d", n, want)
	}
}
