package db_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// vendorEnv gives the test a freshly migrated scratch database with no
// bootstrap rows, plus a superuser connection for inspection.
func vendorEnv(t *testing.T) (context.Context, *db.Store, *pgx.Conn) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping vendor tenant integration test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("vendor_tenant_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+dbName); err != nil {
		admin.Close(ctx)
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
		admin.Close(ctx)
	})
	with := func(user, pass string) string {
		u, _ := url.Parse(base)
		if user != "" {
			u.User = url.UserPassword(user, pass)
		}
		u.Path = "/" + dbName
		return u.String()
	}
	if err := db.Migrate(ctx, with("", ""), quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, with("", ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { su.Close(ctx) })
	st, err := db.New(ctx, with("app_admin", "app_admin_dev"), with("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return ctx, st, su
}

func TestEnsureVendorTenant(t *testing.T) {
	const guid = "11111111-2222-3333-4444-555555555555"

	t.Run("creates row, idempotent, vendor user seeds", func(t *testing.T) {
		ctx, st, su := vendorEnv(t)
		vt := db.PocVendorTenantUUID()
		var n int
		if err := su.QueryRow(ctx, `SELECT count(*) FROM vendor_tenants WHERE id=$1`, vt).Scan(&n); err != nil || n != 0 {
			t.Fatalf("expected no vendor row initially, n=%d err=%v", n, err)
		}
		if err := st.EnsureVendorTenant(ctx, guid); err != nil {
			t.Fatal(err)
		}
		var name, ent string
		if err := su.QueryRow(ctx, `SELECT name, entra_tenant_id FROM vendor_tenants WHERE id=$1`, vt).Scan(&name, &ent); err != nil {
			t.Fatal(err)
		}
		if name != "Involve (Vendor)" || ent != guid {
			t.Fatalf("got %q / %q", name, ent)
		}
		if err := st.EnsureVendorTenant(ctx, "other-tenant"); err != nil {
			t.Fatal(err)
		}
		if err := st.EnsureVendorTenant(ctx, guid); err != nil {
			t.Fatal(err)
		}
		if err := su.QueryRow(ctx, `SELECT entra_tenant_id FROM vendor_tenants WHERE id=$1`, vt).Scan(&ent); err != nil || ent != guid {
			t.Fatalf("row changed: %q %v", ent, err)
		}
		if _, err := st.SeedLocalUser(ctx, db.SeedLocalUserOptions{
			Email: "vendor@example.test", Password: "a-long-enough-password", Role: "admin", VendorTenantID: vt,
		}); err != nil {
			t.Fatalf("seed vendor user: %v", err)
		}
	})

	t.Run("existing default row left unchanged", func(t *testing.T) {
		ctx, st, su := vendorEnv(t)
		vt := db.PocVendorTenantUUID()
		if _, err := su.Exec(ctx, `INSERT INTO vendor_tenants (id, name, entra_tenant_id) VALUES ($1,'Involve (Vendor)',$2)`, vt, db.PocVendorTenantID); err != nil {
			t.Fatal(err)
		}
		if err := st.EnsureVendorTenant(ctx, guid); err != nil {
			t.Fatal(err)
		}
		var ent string
		var n int
		if err := su.QueryRow(ctx, `SELECT count(*), min(entra_tenant_id) FROM vendor_tenants`).Scan(&n, &ent); err != nil {
			t.Fatal(err)
		}
		if n != 1 || ent != db.PocVendorTenantID {
			t.Fatalf("row changed: n=%d ent=%q", n, ent)
		}
	})

	t.Run("empty id uses default", func(t *testing.T) {
		ctx, st, su := vendorEnv(t)
		if err := st.EnsureVendorTenant(ctx, ""); err != nil {
			t.Fatal(err)
		}
		var ent string
		if err := su.QueryRow(ctx, `SELECT entra_tenant_id FROM vendor_tenants`).Scan(&ent); err != nil || ent != db.PocVendorTenantID {
			t.Fatalf("got %q %v", ent, err)
		}
	})
}
