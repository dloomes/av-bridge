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

func setup(t *testing.T) (context.Context, *pgx.Conn, *db.Store, func(name string) string) {
	t.Helper()
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
	t.Cleanup(func() { su.Close(ctx) })
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	mk := func(name string) string {
		res, err := st.CreateCustomer(ctx, db.CreateCustomerOptions{Name: name})
		if err != nil {
			t.Fatalf("create customer: %v", err)
		}
		return res.CustomerID
	}
	return ctx, su, st, mk
}

func TestPrivacyMigration(t *testing.T) {
	ctx, su, st, mk := setup(t)
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

// The 0073 permission seed must work for a table owner that does not bypass
// RLS (the RDS master). The test makes a throwaway non-superuser the owner of
// roles and role_permissions inside a transaction, so FORCE ROW LEVEL SECURITY
// applies to it, runs the seed block from the migration file as that role, and
// rolls everything back. (Needs the test connection to be a superuser, as the
// disposable-Postgres setup is.)
func TestPrivacySeedUnderForceRLS(t *testing.T) {
	ctx, su, _, mk := setup(t)
	cust := mk("privacy-seed")
	if _, err := su.Exec(ctx, `DELETE FROM role_permissions WHERE permission = 'privacy.manage'
		AND role_id IN (SELECT id FROM roles WHERE customer_id = $1)`, cust); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../db/migrations/0073_privacy.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i, j := strings.Index(text, "-- seed:begin"), strings.Index(text, "-- seed:end")
	if i < 0 || j < i {
		t.Fatal("seed markers not found in 0073")
	}
	seed := text[i:j]

	run := func(owner bool) bool {
		tx, err := su.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec := func(sql string) {
			t.Helper()
			if _, err := tx.Exec(ctx, sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		exec(`CREATE ROLE privacy_seed_owner NOSUPERUSER NOBYPASSRLS`)
		exec(`ALTER TABLE roles OWNER TO privacy_seed_owner`)
		exec(`ALTER TABLE role_permissions OWNER TO privacy_seed_owner`)
		exec(`SET LOCAL ROLE privacy_seed_owner`)
		if owner {
			exec(seed)
		} else {
			// Control: the plain INSERT (no NO FORCE) must be a no-op under FORCE RLS,
			// proving the test reproduces the RDS situation.
			exec(`INSERT INTO role_permissions (role_id, permission)
				SELECT r.id, 'privacy.manage' FROM roles r WHERE r.name = 'admin' AND r.is_system_default`)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
			WHERE r.customer_id = $1 AND rp.permission = 'privacy.manage'`, cust).Scan(&n); err != nil {
			// Visibility of the count itself is subject to RLS; with FORCE restored it reads 0.
			t.Fatal(err)
		}
		_ = n
		exec(`RESET ROLE`)
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
			WHERE r.customer_id = $1 AND rp.permission = 'privacy.manage'`, cust).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if run(false) {
		t.Fatal("control: plain INSERT unexpectedly worked under FORCE RLS; test does not reproduce RDS")
	}
	if !run(true) {
		t.Fatal("seed did not restore privacy.manage for a non-bypass table owner")
	}
}

func TestPrivacyAnonymiseMatching(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-match")
	ins := func(actor, meta, after string) {
		t.Helper()
		if _, err := su.Exec(ctx, `INSERT INTO audit_log (customer_id, actor, action, target_kind, "after", metadata)
			VALUES ($1,$2,'t','user',$3::jsonb,$4::jsonb)`, c, actor, after, meta); err != nil {
			t.Fatal(err)
		}
	}
	ins("op@x.io", `{"note":"note about A.B+c@X.io, thanks"}`, `{"role":"viewer"}`)           // changed
	ins("op@x.io", `{"note":"about axb+c@x.io"}`, `{"email":"axb+c@x.io","role":"viewer"}`)   // wildcard decoy
	ins("op@x.io", `{"note":"about ca.b+c@x.io"}`, `{"email":"ca.b+c@x.io","role":"viewer"}`) // superstring decoy
	ins("op@x.io", `{"note":"about a.b+c@x.io.uk"}`, `{"email":"a.b+c@x.io.uk","role":"viewer"}`)

	ins("op@x.io", `{"note":"contact A.B+c@X.io."}`, `{"role":"viewer"}`) // changed, full stop kept

	var n int64
	if err := st.AdminPool().QueryRow(ctx, `SELECT privacy_anonymise_audit($1::uuid, 'a.b+c@x.io', '', 'erased-user-00000001')`, c).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("changed = %d, want 2", n)
	}
	var stop string
	if err := su.QueryRow(ctx, `SELECT metadata->>'note' FROM audit_log WHERE customer_id=$1 AND metadata->>'note' LIKE 'contact%'`, c).Scan(&stop); err != nil || stop != "contact erased-user-00000001." {
		t.Errorf("trailing full stop case: %q err=%v", stop, err)
	}
	var meta string
	if err := su.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE customer_id=$1 AND metadata->>'note' LIKE 'note about%'`, c).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, "note about erased-user-00000001, thanks") {
		t.Errorf("metadata not rewritten: %s", meta)
	}
	var kept int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id=$1 AND "after" ? 'email'
		AND metadata->>'note' LIKE 'about %' AND metadata::text NOT LIKE '%erased-user%'`, c).Scan(&kept); err != nil || kept != 3 {
		t.Errorf("decoys changed (kept=%d err=%v)", kept, err)
	}

	// Guards: empty email is refused; empty user id can't match target_id = ''.
	if err := st.AdminPool().QueryRow(ctx, `SELECT privacy_anonymise_audit($1::uuid, '', NULL, 'erased-user-00000001')`, c).Scan(&n); err == nil {
		t.Error("empty email should raise")
	}
	if _, err := su.Exec(ctx, `INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id) VALUES ($1,'op@x.io','t2','user','')`, c); err != nil {
		t.Fatal(err)
	}
	if err := st.AdminPool().QueryRow(ctx, `SELECT privacy_anonymise_audit($1::uuid, 'nobody@x.io', '', 'erased-user-00000002')`, c).Scan(&n); err != nil || n != 0 {
		t.Errorf("empty user id matched rows (n=%d err=%v)", n, err)
	}
}
