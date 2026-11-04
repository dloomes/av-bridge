package privacy

// Integration test for migration 0073. Runs only when TEST_DATABASE_URL
// points at a disposable Postgres 16.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestPrivacyMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
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

	mk := func(name string) string {
		res, err := st.CreateCustomer(ctx, db.CreateCustomerOptions{Name: name})
		if err != nil {
			t.Fatalf("create customer: %v", err)
		}
		return res.CustomerID
	}
	c1, c2 := mk("privacy-mig-1"), mk("privacy-mig-2")

	var has bool
	if err := su.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE r.customer_id = $1 AND r.name = 'admin' AND rp.permission = 'privacy.manage')`, c1).Scan(&has); err != nil || !has {
		t.Fatalf("admin role lacks privacy.manage (err=%v)", err)
	}

	ins := func(cust, actor, action string, after string) {
		t.Helper()
		if _, err := su.Exec(ctx, `INSERT INTO audit_log (customer_id, actor, action, target_kind, "after") VALUES ($1,$2,$3,'user',$4::jsonb)`,
			cust, actor, action, after); err != nil {
			t.Fatal(err)
		}
	}
	const aliceAfter = `{"email":"alice@x.io","full_name":"Alice A","role":"admin"}`
	ins(c1, "Alice@x.io", "user.update", aliceAfter)
	ins(c1, "bob@x.io", "user.update", `{"role":"viewer"}`)
	ins(c2, "Alice@x.io", "user.update", aliceAfter)

	var n int64
	if err := st.AdminPool().QueryRow(ctx, `SELECT privacy_anonymise_audit($1::uuid, 'alice@x.io', $2, 'erased-user-abcd1234')`,
		c1, "00000000-0000-0000-0000-000000000001").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("changed = %d, want 1", n)
	}

	read := func(cust, action string) (actor string, after map[string]any) {
		t.Helper()
		var raw []byte
		rows, err := su.Query(ctx, `SELECT actor, "after" FROM audit_log WHERE customer_id = $1 AND action = $2 ORDER BY id`, cust, action)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := rows.Scan(&actor, &raw); err != nil {
				t.Fatal(err)
			}
			if actor != "bob@x.io" {
				break
			}
		}
		after = map[string]any{}
		if err := json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		return
	}
	actor, after := read(c1, "user.update")
	if actor != "erased-user-abcd1234" {
		t.Errorf("actor = %q", actor)
	}
	if _, ok := after["email"]; ok {
		t.Errorf("email key kept: %v", after)
	}
	if _, ok := after["full_name"]; ok {
		t.Errorf("full_name key kept: %v", after)
	}
	if after["role"] != "admin" {
		t.Errorf("role lost: %v", after)
	}
	var bob int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id=$1 AND actor='bob@x.io' AND "after"->>'role'='viewer'`, c1).Scan(&bob); err != nil || bob != 1 {
		t.Errorf("unrelated row changed (n=%d err=%v)", bob, err)
	}
	var other, otherAfter string
	if err := su.QueryRow(ctx, `SELECT actor, "after"::text FROM audit_log WHERE customer_id=$1`, c2).Scan(&other, &otherAfter); err != nil {
		t.Fatal(err)
	}
	if other != "Alice@x.io" || !strings.Contains(otherAfter, "alice@x.io") {
		t.Errorf("other customer's row changed: %s %s", other, otherAfter)
	}

	// Still append-only for the app role.
	var denied error
	func() {
		tx, err := st.AdminPool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		_, denied = tx.Exec(ctx, `UPDATE audit_log SET actor = 'x' WHERE customer_id = $1`, c2)
	}()
	if denied == nil || !strings.Contains(denied.Error(), "permission denied") {
		t.Errorf("direct UPDATE should be denied, got %v", denied)
	}
}
