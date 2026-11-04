package privacy

// Integration tests for FindSubject and Collect. Run only when
// TEST_DATABASE_URL points at a disposable Postgres 16.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// collectFor runs FindSubject and Collect in one AdminPool transaction, as
// the handlers will.
func collectFor(t *testing.T, ctx context.Context, admin interface {
	Begin(context.Context) (pgx.Tx, error)
}, customerID, vendorID, email string) (Subject, map[string]Table) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	s, err := FindSubject(ctx, tx, customerID, vendorID, email)
	if err != nil {
		t.Fatalf("FindSubject: %v", err)
	}
	tables, err := Collect(ctx, tx, s)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]Table{}
	for _, tb := range tables {
		if _, dup := out[tb.Name]; dup {
			t.Errorf("table %s returned twice", tb.Name)
		}
		if len(tb.Rows) == 0 {
			t.Errorf("table %s returned with no rows", tb.Name)
		}
		for _, r := range tb.Rows {
			if len(r) != len(tb.Columns) {
				t.Errorf("%s: row has %d values, %d columns", tb.Name, len(r), len(tb.Columns))
			}
		}
		out[tb.Name] = tb
	}
	return s, out
}

// col returns the values of one column.
func col(t *testing.T, tb Table, name string) []any {
	t.Helper()
	for i, c := range tb.Columns {
		if c == name {
			var v []any
			for _, r := range tb.Rows {
				v = append(v, r[i])
			}
			return v
		}
	}
	t.Fatalf("%s has no column %s (has %v)", tb.Name, name, tb.Columns)
	return nil
}

func TestCollect(t *testing.T) {
	ctx, su, st, mk := setup(t)
	a, b := mk("privacy-collect-a"), mk("privacy-collect-b")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	user := func(cust, email string) string {
		t.Helper()
		var id string
		if err := su.QueryRow(ctx, `INSERT INTO users (customer_id, email, full_name, password_hash)
			VALUES ($1, $2, 'Alice A', 'secret-hash') RETURNING id`, cust, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice := user(a, "Alice@Example.com")
	aliceB := user(b, "alice@example.com")
	bob := user(a, "bob@example.com")

	sess := func(uid, ip string) {
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at, ip_address, user_agent)
			VALUES ($1, $2, now() + interval '1 day', $3, 'Firefox')`, uid, fmt.Sprintf("h-%s-%s", uid, ip), ip)
	}
	sess(alice, "10.0.0.1")
	sess(alice, "10.0.0.2")
	sess(aliceB, "10.9.9.9")
	sess(bob, "10.0.0.3")

	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, created_by, last_used_ip)
		VALUES ($1, 'ci', 'pfx', $2, $3, '10.0.0.4')`, a, "tok-"+alice, alice)
	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, created_by)
		VALUES ($1, 'bob', 'pfx', $2, $3)`, a, "tok-"+bob, bob)

	audit := func(cust, actor, targetID string) {
		exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, "after")
			VALUES ($1, $2, 't', 'user', nullif($3, ''), '{"note":"password reset by admin"}')`, cust, actor, targetID)
	}
	audit(a, "Alice@Example.com", "")
	audit(a, "alice@example.com", "")
	audit(a, "ALICE@example.com", "")
	audit(a, "bob@example.com", strings.ToUpper(alice)) // targets her (id in upper case); no email in the payload
	audit(a, "bob@example.com", bob)   // unrelated
	audit(b, "alice@example.com", "")  // the other organisation
	audit(b, "bob@example.com", alice) // her id, but in the other organisation

	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'Mine', 'bob@example.com')`, a, alice)
	exec(`INSERT INTO dashboards (customer_id, name, created_by) VALUES ($1, 'Shared', 'bob@example.com')`, a)
	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'B', 'alice@example.com')`, b, aliceB)

	dev := func(cust string) string {
		var coll, d string
		if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c', '\x00') RETURNING id`, cust).Scan(&coll); err != nil {
			t.Fatal(err)
		}
		if err := su.QueryRow(ctx, `INSERT INTO devices (customer_id, collector_id) VALUES ($1, $2) RETURNING id`, cust, coll).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	devA, devB := dev(a), dev(b)
	remote := func(cust, dev, uid, actor, reason string) {
		exec(`INSERT INTO remote_session (customer_id, device_id, user_id, actor, host_label, scheme, port, launch_code_hash, expires_at, end_reason)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, 'https', 443, '\x01', now() + interval '1 hour', nullif($6, ''))`,
			cust, dev, uid, actor, "rs-"+dev, reason)
	}
	remote(a, devA, alice, "Alice@Example.com", "")
	remote(b, devB, aliceB, "alice@example.com", "x")

	s, got := collectFor(t, ctx, st.AdminPool(), a, "", "alice@example.com")
	if s.UserID != alice || s.CustomerID != a || s.Email != "alice@example.com" {
		t.Fatalf("subject = %+v", s)
	}
	for name, want := range map[string]int{
		"users": 1, "user_sessions": 2, "audit_log": 4, "dashboards": 1, "remote_session": 1, "api_tokens": 1,
	} {
		if n := len(got[name].Rows); n != want {
			t.Errorf("%s: %d rows, want %d", name, n, want)
		}
	}
	for name, tb := range got {
		for _, c := range tb.Columns {
			if strings.HasSuffix(c, "_hash") || c == "logo" {
				t.Errorf("%s exports secret/binary column %s", name, c)
			}
		}
		// No row from customer B, in any table that names its customer.
		for i, c := range tb.Columns {
			if c != "customer_id" {
				continue
			}
			for _, r := range tb.Rows {
				if r[i] != a {
					t.Errorf("%s: row from customer %v", name, r[i])
				}
			}
		}
	}
	for _, ip := range col(t, got["user_sessions"], "ip_address") {
		if ip != "10.0.0.1" && ip != "10.0.0.2" {
			t.Errorf("foreign session ip %v", ip)
		}
	}

	auditIDs := col(t, got["audit_log"], "id")

	// Account hard-deleted, as DeleteUser does it: the row goes and a
	// user.delete entry records the email against the id. The email still
	// finds the audit trail, and the recorded id still finds rows that
	// reference the old account without a foreign key.
	exec(`DELETE FROM users WHERE id = $1`, alice)
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, before)
		VALUES ($1, 'bob@example.com', 'user.delete', 'user', $2, '{"email":"alice@example.com","role":"admin"}')`, a, alice)
	s, got = collectFor(t, ctx, st.AdminPool(), a, "", "ALICE@example.com")
	if s.UserID != "" {
		t.Fatalf("UserID = %q after delete", s.UserID)
	}
	after := map[any]bool{}
	for _, id := range col(t, got["audit_log"], "id") {
		after[id] = true
	}
	for _, id := range auditIDs {
		if !after[id] {
			t.Errorf("audit row %v lost after delete", id)
		}
	}
	if n := len(got["audit_log"].Rows); n != 5 {
		t.Errorf("audit_log after delete: %d rows, want the 4 plus the user.delete entry", n)
	}
	if n := len(got["dashboards"].Rows); n != 1 {
		t.Errorf("dashboards after delete: %d rows, want 1 (owner_user_id has no FK)", n)
	}
	if _, ok := got["users"]; ok {
		t.Errorf("users returned after delete")
	}
}

// KeyEmailInText, KeyEmailJSON and audit payload matches use the same
// whole-address pattern as privacy_anonymise_audit: decoys stay out.
func TestCollectEmailInText(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-collect-text")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var coll, dev string
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c', '\x00') RETURNING id`, c).Scan(&coll); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO devices (customer_id, collector_id) VALUES ($1, $2) RETURNING id`, c, coll).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	for i, reason := range []string{
		"ended by A.B+c@X.io.",   // match, sentence full stop
		"ended by ca.b+c@x.io",   // superstring decoy
		"ended by axb+c@x.io",    // wildcard decoy
		"ended by a.b+c@x.io.uk", // longer domain decoy
	} {
		exec(`INSERT INTO remote_session (customer_id, device_id, actor, host_label, scheme, port, launch_code_hash, expires_at, end_reason)
			VALUES ($1, $2, 'op@x.io', $3, 'https', 443, '\x01', now() + interval '1 hour', $4)`, c, dev, fmt.Sprintf("txt-%s-%d", c[:8], i), reason)
	}
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, metadata) VALUES
		($1, 'op@x.io', 't', 'user', '{"note":"about A.B+c@x.io, thanks"}'),
		($1, 'op@x.io', 't', 'user', '{"note":"about axb+c@x.io"}')`, c)
	exec(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, 'mail', 'email', 'A.B+c@x.io')`, c)

	var region, loc, bld, room string
	if err := su.QueryRow(ctx, `INSERT INTO regions (customer_id, name) VALUES ($1, 'r') RETURNING id`, c).Scan(&region); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'l') RETURNING id`, c, region).Scan(&loc); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'b') RETURNING id`, c, loc).Scan(&bld); err != nil {
		t.Fatal(err)
	}
	for i, recipients := range []string{
		`[{"channel":"email","target":"other@x.io"},{"channel":"email","target":"a.b+C@x.io"}]`, // match
		`[{"channel":"email","target":"axb+c@x.io"}]`,                                           // decoy
	} {
		if err := su.QueryRow(ctx, `INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id`, c, bld, fmt.Sprint("room", i)).Scan(&room); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO room_nightly_config (customer_id, room_id, notification_recipients) VALUES ($1, $2, $3::jsonb)`, c, room, recipients)
	}

	s, got := collectFor(t, ctx, st.AdminPool(), c, "", "a.b+c@x.io")
	if s.UserID != "" {
		t.Errorf("UserID = %q, want empty (no account)", s.UserID)
	}
	if v := col(t, got["remote_session"], "end_reason"); len(v) != 1 || v[0] != "ended by A.B+c@X.io." {
		t.Errorf("remote_session end_reason = %v", v)
	}
	if n := len(got["audit_log"].Rows); n != 1 {
		t.Errorf("audit_log: %d rows, want 1", n)
	}
	if n := len(got["notification_channels"].Rows); n != 1 {
		t.Errorf("notification_channels: %d rows, want 1", n)
	}
	if n := len(got["room_nightly_config"].Rows); n != 1 {
		t.Errorf("room_nightly_config: %d rows, want 1", n)
	}
}

// Vendor staff: found through vendor_tenant_id, not a customer.
func TestCollectVendorStaff(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-collect-vendor")
	var vt, uid string
	if err := su.QueryRow(ctx, `INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id`, "vt-"+c).Scan(&vt); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO users (vendor_tenant_id, email, password_hash) VALUES ($1, $2, 'h') RETURNING id`,
		vt, "Staff-"+c[:8]+"@involve.example").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	email := "staff-" + c[:8] + "@involve.example"
	if _, err := su.Exec(ctx, `INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, uid, "vs-"+uid); err != nil {
		t.Fatal(err)
	}
	// Staff act inside customers: their audit entries carry that customer's id.
	if _, err := su.Exec(ctx, `INSERT INTO audit_log (customer_id, actor, action, target_kind, actor_is_vendor) VALUES ($1, $2, 't', 'device', true)`, c, email); err != nil {
		t.Fatal(err)
	}

	// A customer account with the same email is a different account: the
	// vendor subject must not resolve to it, export its users row, or reach
	// its id-keyed rows (sessions). By design, email-keyed rows in customer
	// tables (here the audit entry above) are searched across customers.
	var custUID string
	if err := su.QueryRow(ctx, `INSERT INTO users (customer_id, email, password_hash) VALUES ($1, $2, 'h') RETURNING id`,
		c, email).Scan(&custUID); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, custUID, "cs-"+custUID); err != nil {
		t.Fatal(err)
	}

	s, got := collectFor(t, ctx, st.AdminPool(), "", vt, email)
	if s.UserID != uid || s.VendorTenantID != vt || s.CustomerID != "" {
		t.Fatalf("subject = %+v (customer account is %s)", s, custUID)
	}
	for name, want := range map[string]int{"users": 1, "user_sessions": 1, "audit_log": 1} {
		if n := len(got[name].Rows); n != want {
			t.Errorf("%s: %d rows, want %d", name, n, want)
		}
	}
	if ids := col(t, got["users"], "id"); len(ids) != 1 || ids[0] != uid {
		t.Errorf("users ids = %v, want only the staff account %s", ids, uid)
	}
	if ids := col(t, got["user_sessions"], "user_id"); len(ids) != 1 || ids[0] != uid {
		t.Errorf("user_sessions user_ids = %v, want only %s", ids, uid)
	}
}

// Collect runs as app_admin, so it must be able to read every register
// table; erasure, also as app_admin, must be able to UPDATE every table with
// an ActReplace entry (0074 granted it on firmware_targets).
func TestAdminCanReadRegister(t *testing.T) {
	ctx, su, _, _ := setup(t)
	check := func(table, priv string) {
		t.Helper()
		var ok bool
		if err := su.QueryRow(ctx, `SELECT has_table_privilege('app_admin', $1, $2)`, table, priv).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("app_admin lacks %s on %s", priv, table)
		}
	}
	seen := map[string]bool{}
	for _, e := range Personal {
		if e.Action == ActReplace && !seen[e.Table+"/u"] {
			seen[e.Table+"/u"] = true
			check(e.Table, "UPDATE")
		}
		if !seen[e.Table] {
			seen[e.Table] = true
			check(e.Table, "SELECT")
		}
	}
}

func TestFindSubjectArgs(t *testing.T) {
	ctx, _, st, _ := setup(t)
	tx, err := st.AdminPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, tc := range []struct{ cust, vendor, email string }{
		{"", "", "a@x.io"},
		{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "a@x.io"},
		{"00000000-0000-0000-0000-000000000001", "", "  "},
	} {
		if _, err := FindSubject(ctx, tx, tc.cust, tc.vendor, tc.email); err == nil {
			t.Errorf("FindSubject(%q, %q, %q) should fail", tc.cust, tc.vendor, tc.email)
		}
	}
}

// The Go pattern must equal the one privacy_anonymise_audit builds. The test
// lifts the two assignments out of migration 0073 into a temporary function
// and compares outputs, so the two cannot drift apart silently.
func TestEmailPatternMatchesMigration(t *testing.T) {
	ctx, su, _, _ := setup(t)
	raw, err := os.ReadFile("../db/migrations/0073_privacy.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i := strings.Index(text, "    esc := ")
	j := strings.Index(text, "    pattern := ")
	if i < 0 || j < i {
		t.Fatal("esc/pattern assignments not found in 0073")
	}
	k := strings.Index(text[j:], ";\n")
	if k < 0 {
		t.Fatal("pattern assignment not terminated")
	}
	body := text[i : j+k+1]
	tx, err := su.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE FUNCTION pg_temp.privacy_pattern(p_email text) RETURNS text LANGUAGE plpgsql AS $f$
		DECLARE esc text; pattern text;
		BEGIN `+body+` RETURN pattern; END $f$`); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{
		"alice@example.com", "A.B+c@X.io", `we(ird)[x]{y}^$|?*\@x.io`, "o'brien%_@x.io",
	} {
		var want string
		if err := tx.QueryRow(ctx, `SELECT pg_temp.privacy_pattern($1)`, email).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if got := EmailPattern(email); got != want {
			t.Errorf("EmailPattern(%q) = %q, migration builds %q", email, got, want)
		}
	}
}
