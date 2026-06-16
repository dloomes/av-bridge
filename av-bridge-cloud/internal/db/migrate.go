package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// WaitForDSN opens a brief connection against dsn until success or timeout.
// Used by main before calling Migrate, since the migration DSN typically
// points at a different role than the app pools and compose's healthcheck
// can't prove this role authenticates.
func WaitForDSN(ctx context.Context, dsn string, timeout time.Duration, log *slog.Logger) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.Connect(ctx2, dsn)
		cancel()
		if err == nil {
			_ = conn.Close(ctx)
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("migration DB not ready within %s: %w", timeout, lastErr)
		}
		log.Info("waiting for migration DB", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Migration is one applied schema change. Version comes from the filename
// prefix (e.g. "0003_commands.sql" -> 3).
type migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrate applies any pending embedded migrations using the given DSN. The DSN
// must point at a role with privileges to CREATE TABLE, CREATE ROLE, ALTER
// TABLE … ENABLE/FORCE RLS, and CREATE POLICY — i.e. the Postgres bootstrap
// superuser in dev, or the equivalent in production (a one-shot migration role
// scoped down per environment).
//
// Behaviour:
//   - Creates schema_migrations on first run.
//   - Baselines pre-existing dev databases: if `customers` exists but
//     schema_migrations is empty, every known version is inserted as already
//     applied without re-running its SQL. Lets the old init-script era upgrade
//     in place without `docker compose down -v`.
//   - Applies remaining migrations in version order, each in its own tx.
//
// The opened connection is closed before returning so the app's runtime pools
// can take over.
func Migrate(ctx context.Context, dsn string, log *slog.Logger) error {
	if dsn == "" {
		return fmt.Errorf("migration DSN is empty")
	}
	migrations, err := loadEmbedded()
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for migrate: %w", err)
	}
	defer conn.Close(ctx)

	if err := ensureMigrationsTable(ctx, conn); err != nil {
		return err
	}
	if err := maybeBaseline(ctx, conn, migrations, log); err != nil {
		return err
	}
	applied, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return fmt.Errorf("apply %s: %w", m.Name, err)
		}
		log.Info("migration applied", "version", m.Version, "name", m.Name)
	}
	return nil
}

func loadEmbedded() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		b, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{Version: v, Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("duplicate migration version %d (%s, %s)",
				out[i].Version, out[i-1].Name, out[i].Name)
		}
	}
	return out, nil
}

func parseVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: expected NNNN_description.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad version prefix: %w", name, err)
	}
	return v, nil
}

func ensureMigrationsTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    int PRIMARY KEY,
			name       text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	return err
}

// maybeBaseline handles the one-time transition from the init-script era. If
// customers exists (so the schema is already there) and schema_migrations is
// empty, we treat every embedded migration as already applied. New migrations
// added after the transition still run normally on the next startup.
func maybeBaseline(ctx context.Context, conn *pgx.Conn, migrations []migration, log *slog.Logger) error {
	var count int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		return fmt.Errorf("count schema_migrations: %w", err)
	}
	if count > 0 {
		return nil
	}
	var exists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = 'customers'
		)`).Scan(&exists); err != nil {
		return fmt.Errorf("check customers exists: %w", err)
	}
	if !exists {
		return nil // fresh DB; applyOne will install everything from scratch
	}
	log.Info("baselining existing database: marking embedded migrations as already applied",
		"count", len(migrations))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, m := range migrations {
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.Version, m.Name); err != nil {
			return fmt.Errorf("baseline %s: %w", m.Name, err)
		}
	}
	return tx.Commit(ctx)
}

func loadApplied(ctx context.Context, conn *pgx.Conn) (map[int]struct{}, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]struct{}{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func applyOne(ctx context.Context, conn *pgx.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
		m.Version, m.Name, time.Now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
