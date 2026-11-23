package db_test

// Integration test for migration 0081 (devices without a Collector,
// integrations tables). Runs only when TEST_DATABASE_URL points at a
// disposable Postgres.

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

func TestIntegrationsMigration(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integrations migration test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	dbName := fmt.Sprintf("integrations_mig_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`) })
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
	defer su.Close(ctx)

	var custA, custB, coll string
	for _, c := range []struct {
		name string
		dst  *string
	}{{"integ-test-a", &custA}, {"integ-test-b", &custB}} {
		if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ($1) RETURNING id::text`, c.name).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
		VALUES ($1, 'integ-coll', 'integ-coll', '\x00') RETURNING id::text`, custA).Scan(&coll); err != nil {
		t.Fatal(err)
	}

	// A Lens device with no collector inserts, updates and deletes cleanly.
	var devID string
	if err := su.QueryRow(ctx, `INSERT INTO devices (customer_id, source, platform, name)
		VALUES ($1, 'poly_lens', 'windows', 'fake room') RETURNING id::text`, custA).Scan(&devID); err != nil {
		t.Fatalf("insert poly_lens device without collector: %v", err)
	}
	if _, err := su.Exec(ctx, `UPDATE devices SET address = '10.0.0.1', deleted_at = now() WHERE id = $1`, devID); err != nil {
		t.Fatalf("update NULL-collector device: %v", err)
	}
	if _, err := su.Exec(ctx, `DELETE FROM devices WHERE id = $1`, devID); err != nil {
		t.Fatalf("delete NULL-collector device: %v", err)
	}

	// The source/collector pairing is enforced both ways.
	if _, err := su.Exec(ctx, `INSERT INTO devices (customer_id, source, name) VALUES ($1, 'collector', 'x')`, custA); err == nil {
		t.Error("collector device with NULL collector was accepted")
	}
	if _, err := su.Exec(ctx, `INSERT INTO devices (customer_id, collector_id, source, name) VALUES ($1, $2, 'poly_lens', 'x')`, custA, coll); err == nil {
		t.Error("poly_lens device with a collector was accepted")
	}
	if _, err := su.Exec(ctx, `INSERT INTO devices (customer_id, source, platform, name) VALUES ($1, 'poly_lens', 'linux', 'x')`, custA); err == nil {
		t.Error("unknown platform was accepted")
	}

	// A normal Collector device still bumps the collector's config version.
	var before, after int64
	_ = su.QueryRow(ctx, `SELECT config_version FROM collectors WHERE id = $1`, coll).Scan(&before)
	if _, err := su.Exec(ctx, `INSERT INTO devices (customer_id, collector_id, name) VALUES ($1, $2, 'real')`, custA, coll); err != nil {
		t.Fatal(err)
	}
	_ = su.QueryRow(ctx, `SELECT config_version FROM collectors WHERE id = $1`, coll).Scan(&after)
	if after != before+1 {
		t.Errorf("collector config_version %d -> %d, want +1", before, after)
	}

	// One integration per customer and provider.
	if _, err := su.Exec(ctx, `INSERT INTO integrations (customer_id, provider) VALUES ($1, 'poly_lens')`, custA); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `INSERT INTO integrations (customer_id, provider) VALUES ($1, 'poly_lens')`, custA); err == nil {
		t.Error("second poly_lens integration for one customer was accepted")
	}
	if _, err := su.Exec(ctx, `INSERT INTO integrations (customer_id, provider) VALUES ($1, 'poly_lens')`, custB); err != nil {
		t.Fatalf("other customer's integration: %v", err)
	}

	// Tenant isolation, as app_tenant.
	st, err := db.New(ctx, with("app_admin", "app_admin_dev"), with("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	count := func(cust string) (n int) {
		err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM integrations`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(custA); n != 1 {
		t.Errorf("tenant A sees %d integrations, want 1", n)
	}
	if n := count(custB); n != 1 {
		t.Errorf("tenant B sees %d integrations, want 1 (its own)", n)
	}
	err = st.WithTenant(ctx, custB, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO integrations (customer_id, provider) VALUES ($1, 'poly_lens')`, custA)
		return e
	})
	if err == nil {
		t.Error("tenant B wrote a row for tenant A")
	}
}
