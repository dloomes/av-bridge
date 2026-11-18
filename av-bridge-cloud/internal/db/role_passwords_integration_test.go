package db_test

// Integration test for SetRolePasswords. Runs only with TEST_DATABASE_URL.
// Roles are cluster-wide, so the test sets passwords on throwaway roles via
// the same code path (SetRolePasswordsFor) rather than on app_admin/app_tenant,
// which other packages' tests log in to with the dev passwords.

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestSetRolePasswords(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)

	const ra, rt = "pwtest_admin", "pwtest_tenant"
	for _, r := range []string{ra, rt} {
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+r)
		if _, err := admin.Exec(ctx, `CREATE ROLE `+r+` LOGIN PASSWORD 'old_dev_password'`); err != nil {
			t.Fatalf("create role: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, r := range []string{ra, rt} {
			_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+r)
		}
	})

	newAdmin := "it's-a-quoted\"pw-0123456789abcdef"
	newTenant := "tenantpw-0123456789abcdefghij"
	if err := db.SetRolePasswordsFor(ctx, base, ra, newAdmin, rt, newTenant); err != nil {
		t.Fatalf("set passwords: %v", err)
	}
	// idempotent
	if err := db.SetRolePasswordsFor(ctx, base, ra, newAdmin, rt, newTenant); err != nil {
		t.Fatalf("second set: %v", err)
	}

	login := func(role, pw string) error {
		u, _ := url.Parse(base)
		u.User = url.UserPassword(role, pw)
		c, err := pgx.Connect(ctx, u.String())
		if err != nil {
			return err
		}
		return c.Close(ctx)
	}
	if err := login(ra, newAdmin); err != nil {
		t.Errorf("admin new password rejected: %v", err)
	}
	if err := login(ra, "old_dev_password"); err == nil {
		t.Error("admin old password still accepted")
	}
	if err := login(rt, newTenant); err != nil {
		t.Errorf("tenant new password rejected: %v", err)
	}
	if err := login(rt, "old_dev_password"); err == nil {
		t.Error("tenant old password still accepted")
	}
}

func TestSetRolePasswordsEmptyDSN(t *testing.T) {
	if err := db.SetRolePasswords(context.Background(), "", "a", "b"); err == nil {
		t.Fatal("expected error for empty DSN")
	}
}
