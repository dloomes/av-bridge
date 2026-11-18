package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// rolePasswordLockKey serialises concurrent SetRolePasswords calls
// (0x4d415250 = "MARP"); ALTER ROLE on the same role from several copies
// starting at once can otherwise fail with "tuple concurrently updated".
const rolePasswordLockKey int64 = 0x4d415250

// SetRolePasswords sets the app_admin and app_tenant login passwords using the
// migration (master) DSN. The migrations create those roles with dev passwords
// that are public in git; production overrides them from Secrets Manager.
// Idempotent and safe when several copies start at once. Never logs a password.
func SetRolePasswords(ctx context.Context, migrationDSN, adminPassword, tenantPassword string) error {
	return setRolePasswords(ctx, migrationDSN, "app_admin", adminPassword, "app_tenant", tenantPassword)
}

func setRolePasswords(ctx context.Context, dsn, adminRole, adminPw, tenantRole, tenantPw string) error {
	if dsn == "" {
		return fmt.Errorf("migration DSN is empty")
	}
	if adminPw == "" || tenantPw == "" {
		return fmt.Errorf("role passwords must not be empty")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for role passwords: %w", err)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rolePasswordLockKey); err != nil {
		return fmt.Errorf("role password lock: %w", err)
	}
	for _, rp := range [][2]string{{adminRole, adminPw}, {tenantRole, tenantPw}} {
		var stmt string
		// format() quotes the identifier (%I) and the literal (%L) safely.
		if err := tx.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L', $1::text, $2::text)`, rp[0], rp[1]).Scan(&stmt); err != nil {
			return fmt.Errorf("build statement for %s: %w", rp[0], err)
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("set password for %s: %w", rp[0], err)
		}
	}
	return tx.Commit(ctx)
}
