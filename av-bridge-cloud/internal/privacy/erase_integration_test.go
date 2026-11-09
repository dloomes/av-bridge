package privacy

// Integration tests for Erase. Run only when TEST_DATABASE_URL points at a
// disposable Postgres 16.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
)

func TestStandIn(t *testing.T) {
	if got := StandIn(Subject{UserID: "1A2B3C4D-5E6F-7a8b-9c0d-0e1f2a3b4c5d", Email: "x@y.z"}); got != "erased-user-1a2b3c4d" {
		t.Errorf("StandIn with account = %q", got)
	}
	sum := sha256.Sum256([]byte("alice@example.com"))
	want := "erased-user-" + hex.EncodeToString(sum[:])[:8]
	if got := StandIn(Subject{Email: "Alice@Example.com"}); got != want {
		t.Errorf("StandIn without account = %q, want %q", got, want)
	}
	if got := StandIn(Subject{UserID: "00000000-0000-0000-0000-000000000001"}); got != "erased-user-00000000" {
		t.Errorf("StandIn zero id = %q", got)
	}
}

// eraseIn runs FindSubject and Erase in one AdminPool transaction and commits
// unless dryRun, as the handlers do.
func eraseIn(t *testing.T, ctx context.Context, admin interface {
	Begin(context.Context) (pgx.Tx, error)
}, customerID, vendorID, email string, dryRun bool) (Subject, Result) {
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
	res, err := Erase(ctx, tx, s, dryRun)
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return s, res
}

// registerSnapshot renders every register table's rows for one customer
// (Scoped tables by their scope column; the rest through the given user ids)
// plus that customer's audit_log, as text, so two snapshots can be compared.
func registerSnapshot(t *testing.T, ctx context.Context, su *pgx.Conn, customerID string, ids []string) string {
	t.Helper()
	var b strings.Builder
	seen := map[string]bool{}
	for _, e := range Personal {
		if seen[e.Table] {
			continue
		}
		seen[e.Table] = true
		var where string
		var arg any
		switch {
		case e.Scoped:
			sc := e.ScopeColumn
			if sc == "" {
				sc = "customer_id"
			}
			where, arg = "t."+sc+" = $1::uuid", customerID
		default:
			kc := e.KeyColumn
			if kc == "" {
				kc = e.Column
			}
			where, arg = "t."+kc+"::text = ANY($1::text[])", ids
		}
		var s string
		if err := su.QueryRow(ctx, `SELECT coalesce(string_agg(to_jsonb(t)::text, E'\n' ORDER BY to_jsonb(t)::text), '') FROM `+
			e.Table+` t WHERE `+where, arg).Scan(&s); err != nil {
			t.Fatalf("snapshot %s: %v", e.Table, err)
		}
		fmt.Fprintf(&b, "== %s\n%s\n", e.Table, s)
	}
	return b.String()
}

// leftovers counts, for one register entry, the rows of customer A that still
// identify the person: for a user-id key, rows still found through one of the
// ids with the column set; for every Scoped entry, rows whose column still
// holds the email or the name (case-insensitive). audit_log is checked over
// the whole row, since the function rewrites before/after/metadata too.
func leftovers(t *testing.T, ctx context.Context, su *pgx.Conn, e Entry, customerID string, ids []string, email, name string) int {
	t.Helper()
	scope := ""
	if e.Scoped {
		sc := e.ScopeColumn
		if sc == "" {
			sc = "customer_id"
		}
		scope = " AND t." + sc + "::text = $3"
	}
	var conds []string
	kc := e.KeyColumn
	if kc == "" {
		kc = e.Column
	}
	if e.Key == KeyUserID {
		conds = append(conds, "(t."+kc+"::text = ANY($4::text[]) AND t."+e.Column+" IS NOT NULL)")
	}
	// A global (unscoped) table keyed by email, such as the roadmap, is
	// matched on the value alone.
	if e.Scoped || e.Key == KeyEmail {
		val := "t." + e.Column + "::text"
		if e.Table == "audit_log" {
			val = "to_jsonb(t)::text"
		}
		conds = append(conds, "("+val+" ILIKE '%' || $1 || '%' OR "+val+" ILIKE '%' || $2 || '%')")
	}
	if len(conds) == 0 {
		t.Fatalf("%s.%s: nothing to check", e.Table, e.Column)
	}
	q := `SELECT count(*) FROM ` + e.Table + ` t WHERE (` + strings.Join(conds, " OR ") + `)` + scope +
		` AND $1::text IS NOT NULL AND $2::text IS NOT NULL AND $3::text IS NOT NULL AND $4::text[] IS NOT NULL`
	var n int
	if err := su.QueryRow(ctx, q, email, name, customerID, ids).Scan(&n); err != nil {
		t.Fatalf("leftovers %s.%s: %v\n%s", e.Table, e.Column, err, q)
	}
	return n
}

func TestErase(t *testing.T) {
	ctx, su, st, mk := setup(t)
	a, b := mk("privacy-erase-a"), mk("privacy-erase-b")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := su.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	user := func(cust, email, name, provider string) string {
		t.Helper()
		return q(`INSERT INTO users (customer_id, email, full_name, password_hash, provider)
			VALUES ($1, $2, $3, 'secret-hash', $4) RETURNING id::text`, cust, email, name, provider)
	}
	alice := user(a, "Alice@Example.com", "Alice A", "entra")
	aliceB := user(b, "alice@example.com", "Alice A", "local")
	bob := user(a, "bob@example.com", "Bob B", "local")
	// An earlier account of Alice's in A, hard-deleted: only the user.delete
	// entry links its id to her email.
	former := "0f0f0f0f-aaaa-4bbb-8ccc-0123456789ab"
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, before)
		VALUES ($1, 'bob@example.com', 'user.delete', 'user', $2, '{"email":"alice@example.com","role":"viewer"}')`, a, former)

	for _, uid := range []string{alice, aliceB, bob} {
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at, ip_address, user_agent)
			VALUES ($1, $2, now() + interval '1 day', '10.0.0.1', 'Firefox')`, uid, "es-"+uid)
		exec(`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, requester_ip, user_agent)
			VALUES ($1, $2, now() + interval '1 day', '10.0.0.2', 'Firefox')`, uid, "ep-"+uid)
	}
	exec(`INSERT INTO magic_link_tokens (user_id, token_hash, expires_at, issued_by, requester_ip, user_agent)
		VALUES ($1, $2, now() + interval '1 day', $3, '10.0.0.3', 'Edge')`, alice, "em-"+alice, bob)
	exec(`INSERT INTO magic_link_tokens (user_id, token_hash, expires_at, issued_by)
		VALUES ($1, $2, now() + interval '1 day', $3)`, bob, "em-"+bob, alice)

	coll := func(cust string) string {
		return q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c', '\x00') RETURNING id::text`, cust)
	}
	collA, collB := coll(a), coll(b)
	exec(`INSERT INTO collector_enrollment_tokens (collector_id, token_hash, expires_at, issued_by, requester_ip, user_agent)
		VALUES ($1, $2, now() + interval '1 day', $3, '10.0.0.4', 'Chrome')`, collA, "ee-"+alice, alice)
	exec(`INSERT INTO collector_enrollment_tokens (collector_id, token_hash, expires_at, issued_by, requester_ip, user_agent)
		VALUES ($1, $2, now() + interval '1 day', $3, '10.0.0.4', 'Chrome')`, collB, "ee-"+aliceB, aliceB)

	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, created_by, last_used_ip)
		VALUES ($1, 'ci', 'pfx', $2, $3, '10.0.0.5')`, a, "et-"+alice, alice)
	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, created_by, revoked_by, revoked_at)
		VALUES ($1, 'bob', 'pfx', $2, $3, $4, now())`, a, "et-"+bob, bob, alice)
	exec(`INSERT INTO api_tokens (customer_id, name, token_prefix, token_hash, created_by, last_used_ip)
		VALUES ($1, 'ci', 'pfx', $2, $3, '10.9.9.9')`, b, "et-"+aliceB, aliceB)

	dev := func(cust, coll string) string {
		return q(`INSERT INTO devices (customer_id, collector_id) VALUES ($1, $2) RETURNING id::text`, cust, coll)
	}
	devA, devB := dev(a, collA), dev(b, collB)
	remote := func(cust, dev, uid, actor, reason, label string) {
		exec(`INSERT INTO remote_session (customer_id, device_id, user_id, actor, host_label, scheme, port, launch_code_hash, expires_at, end_reason)
			VALUES ($1, $2, $3::uuid, $4, $5, 'https', 443, '\x01', now() + interval '1 hour', $6)`,
			cust, dev, uid, actor, label, reason)
	}
	remote(a, devA, alice, "Alice@Example.com", "ended by ALICE@example.com.", "er-a1-"+devA)
	remote(a, devA, former, "alice@example.com", "expired", "er-a2-"+devA)
	remote(a, devA, bob, "bob@example.com", "ended by bob@example.com", "er-a3-"+devA)
	remote(b, devB, aliceB, "alice@example.com", "ended by alice@example.com", "er-b-"+devB)

	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'Mine', 'Alice@Example.com')`, a, alice)
	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'Old', 'alice@example.com')`, a, former)
	shared := q(`INSERT INTO dashboards (customer_id, name, created_by) VALUES ($1, 'Estate', 'alice@example.com') RETURNING id::text`, a)
	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'B', 'alice@example.com')`, b, aliceB)
	exec(`INSERT INTO dashboard_display_links (customer_id, name, token_hash, dashboard_ids, created_by)
		VALUES ($1, 'Wall', $2, ARRAY[$3::uuid], 'ALICE@example.com')`, a, "ed-"+shared, shared)
	exec(`INSERT INTO firmware_targets (customer_id, make, model, updated_by) VALUES ($1, 'Cisco', 'Room Kit', 'alice@example.com')`, a)
	exec(`INSERT INTO firmware_targets (customer_id, make, model, updated_by) VALUES ($1, 'Cisco', 'Room Kit', 'alice@example.com')`, b)
	exec(`INSERT INTO schedule_date (customer_id, on_date, closed, created_by) VALUES ($1, '2026-12-25', true, 'Alice@example.com')`, a)
	exec(`INSERT INTO schedule_date (customer_id, on_date, closed, created_by) VALUES ($1, '2026-12-25', true, 'alice@example.com')`, b)
	// Global roadmap rows edited by Alice (vendor staff share the register).
	// Hold the roadmap test lock from here to the end of the test, so the
	// roadmap tests in other packages don't overwrite these rows meanwhile.
	unlock, err := roadmap.TestLock(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	exec(`UPDATE roadmap_settings SET updated_by = 'Alice@Example.com'`)
	exec(`UPDATE roadmap_items SET updated_by = 'alice@example.com' WHERE title = 'Bulk device import'`)
	exec(`INSERT INTO roadmap_share_links (name, token_hash, expires_at, created_by) VALUES ('Acme', $1, now() + interval '1 day', 'ALICE@example.com')`, "erl-"+a)

	audit := func(cust, actor, targetID, after string) {
		exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, "after")
			VALUES ($1, $2, 't', 'user', nullif($3, ''), $4::jsonb)`, cust, actor, targetID, after)
	}
	audit(a, "Alice@Example.com", "", `{"role":"admin"}`)
	audit(a, "bob@example.com", alice, `{"email":"alice@example.com","full_name":"Alice A","role":"admin"}`)
	audit(a, "bob@example.com", strings.ToUpper(former), `{"role":"viewer"}`) // her earlier account, id upper-cased
	audit(a, "bob@example.com", "", `{"note":"asked by Alice@Example.com."}`)
	audit(a, "bob@example.com", bob, `{"role":"viewer"}`) // unrelated
	audit(a, "user:"+alice, "", `{"tables":3}`)           // her own privacy action, recorded by account id
	audit(b, "alice@example.com", aliceB, `{"email":"alice@example.com","full_name":"Alice A"}`)
	audit(b, "user:"+aliceB, "", `{"tables":3}`)

	exec(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, 'Facilities', 'email', 'alice@example.com')`, a)
	// Free text holding her address: reported, never edited.
	exec(`UPDATE customers SET support_contact = 'Helpdesk: ALICE@example.com, 9-5' WHERE id = $1`, a)
	for _, ur := range [][2]string{{alice, a}, {aliceB, b}, {bob, a}} {
		exec(`INSERT INTO user_roles (user_id, role_id) SELECT $1, r.id FROM roles r WHERE r.customer_id = $2 AND r.name = 'admin' AND r.is_system_default`, ur[0], ur[1])
	}

	ids := []string{alice, former}
	idsB := []string{aliceB}
	auditCount := func(cust string) int {
		var n int
		if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id = $1`, cust).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	auditA := auditCount(a)

	// The seed must reach every entry Erase changes, or the final check proves nothing.
	for _, e := range Personal {
		if e.Action == ActReport {
			continue
		}
		if n := leftovers(t, ctx, su, e, a, ids, "alice@example.com", "Alice A"); n == 0 {
			t.Fatalf("seed has no row for %s.%s", e.Table, e.Column)
		}
	}

	snapA := registerSnapshot(t, ctx, su, a, ids)
	snapB := registerSnapshot(t, ctx, su, b, idsB)

	// Dry run: counts, but nothing changes.
	_, dry := eraseIn(t, ctx, st.AdminPool(), a, "", "ALICE@example.com", true)
	if got := registerSnapshot(t, ctx, su, a, ids); got != snapA {
		t.Fatalf("dry run changed customer A:\nbefore:\n%s\nafter:\n%s", snapA, got)
	}
	var total int64
	for _, n := range dry.Counts {
		total += n
	}
	if total == 0 || dry.Counts["audit_log"] == 0 || dry.Counts["users"] != 1 {
		t.Errorf("dry run counts = %v", dry.Counts)
	}
	if dry.StandIn != "erased-user-"+strings.ReplaceAll(alice, "-", "")[:8] {
		t.Errorf("dry run stand-in = %q", dry.StandIn)
	}

	s, res := eraseIn(t, ctx, st.AdminPool(), a, "", "ALICE@example.com", false)
	if s.UserID != alice {
		t.Fatalf("subject = %+v", s)
	}
	if fmt.Sprint(res.Counts) != fmt.Sprint(dry.Counts) {
		t.Errorf("counts %v differ from dry run %v", res.Counts, dry.Counts)
	}
	want := map[string]int64{
		"user_sessions": 1, "password_reset_tokens": 1, "magic_link_tokens": 1, "magic_link_tokens.issued_by": 1,
		"collector_enrollment_tokens": 1, "api_tokens.last_used_ip": 1, "api_tokens.created_by": 1, "api_tokens.revoked_by": 1,
		"remote_session.actor": 2, "remote_session.end_reason": 1, "remote_session.user_id": 2,
		"dashboards.created_by": 3, "dashboards": 2, "dashboard_display_links.created_by": 1,
		"firmware_targets.updated_by": 1, "schedule_date.created_by": 1, "audit_log": 6, "user_roles": 1, "users": 1,
	}
	for k, n := range want {
		if res.Counts[k] != n {
			t.Errorf("count %s = %d, want %d", k, res.Counts[k], n)
		}
	}

	for _, e := range Personal {
		if e.Action == ActReport {
			continue
		}
		if n := leftovers(t, ctx, su, e, a, ids, "alice@example.com", "Alice A"); n != 0 {
			t.Errorf("%s.%s: %d rows still identify her", e.Table, e.Column, n)
		}
	}
	if got := auditCount(a); got != auditA {
		t.Errorf("audit_log rows for A = %d, want %d (anonymised, not deleted)", got, auditA)
	}
	var createdBy, layoutName string
	if err := su.QueryRow(ctx, `SELECT created_by, name FROM dashboards WHERE id = $1`, shared).Scan(&createdBy, &layoutName); err != nil {
		t.Fatalf("shared dashboard gone: %v", err)
	}
	if createdBy != res.StandIn || layoutName != "Estate" {
		t.Errorf("shared dashboard = %q/%q, want created_by %q", createdBy, layoutName, res.StandIn)
	}
	var reason string
	if err := su.QueryRow(ctx, `SELECT end_reason FROM remote_session WHERE host_label = $1`, "er-a1-"+devA).Scan(&reason); err != nil || reason != "ended by "+res.StandIn+"." {
		t.Errorf("end_reason = %q (err %v)", reason, err)
	}
	var bobRows int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id = $1 AND actor = 'bob@example.com' AND target_id = $2`, a, bob).Scan(&bobRows); err != nil || bobRows != 1 {
		t.Errorf("unrelated audit row changed (n=%d err=%v)", bobRows, err)
	}
	var bobSess int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM user_sessions WHERE user_id = $1`, bob).Scan(&bobSess); err != nil || bobSess != 1 {
		t.Errorf("bob's session touched (n=%d err=%v)", bobSess, err)
	}
	if err := su.QueryRow(ctx, `SELECT count(*) FROM user_roles WHERE user_id = $1`, bob).Scan(&bobSess); err != nil || bobSess != 1 {
		t.Errorf("bob's role touched (n=%d err=%v)", bobSess, err)
	}
	var idLeft int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log t WHERE customer_id = $1 AND to_jsonb(t)::text ILIKE '%' || $2 || '%'`,
		a, "user:"+alice).Scan(&idLeft); err != nil || idLeft != 0 {
		t.Errorf("audit rows still hold user:%s: %d (err %v)", alice, idLeft, err)
	}
	var support string
	if err := su.QueryRow(ctx, `SELECT support_contact FROM customers WHERE id = $1`, a).Scan(&support); err != nil || support != "Helpdesk: ALICE@example.com, 9-5" {
		t.Errorf("support_contact = %q (err %v), want it unchanged", support, err)
	}
	if got := registerSnapshot(t, ctx, su, b, idsB); got != snapB {
		t.Errorf("customer B changed:\nbefore:\n%s\nafter:\n%s", snapB, got)
	}

	joined := strings.Join(res.Actions, "\n")
	if !strings.Contains(joined, "Facilities") {
		t.Errorf("actions don't name the notification channel: %v", res.Actions)
	}
	if !strings.Contains(joined, "publishes this email as its support contact") {
		t.Errorf("actions don't report the free-text support contact: %v", res.Actions)
	}
	if !strings.Contains(joined, "Microsoft Entra ID") {
		t.Errorf("actions lack the Entra note: %v", res.Actions)
	}
	if strings.Contains(joined, "@") {
		t.Errorf("actions carry an email: %v", res.Actions)
	}
	if !strings.HasPrefix(res.StandIn, "erased-user-") {
		t.Errorf("stand-in %q", res.StandIn)
	}
}

// Review Focus 3: no account left. The email still finds the audit trail and
// the old id's rows, and the stand-in comes from the email's hash.
func TestEraseNoAccount(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-erase-gone")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var uid string
	if err := su.QueryRow(ctx, `INSERT INTO users (customer_id, email, full_name, password_hash) VALUES ($1, 'Carol@Example.com', 'Carol C', 'h') RETURNING id::text`, c).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind) VALUES ($1, 'carol@example.com', 'device.update', 'device')`, c)
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, "after") VALUES ($1, 'op@example.com', 'user.update', 'user', $2, '{"role":"viewer"}')`, c, uid)
	exec(`INSERT INTO dashboards (customer_id, owner_user_id, name, created_by) VALUES ($1, $2, 'Mine', 'carol@example.com')`, c, uid)
	// Hard delete, as DeleteUser does it.
	exec(`DELETE FROM users WHERE id = $1`, uid)
	exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, target_id, before) VALUES ($1, 'op@example.com', 'user.delete', 'user', $2, '{"email":"carol@example.com","role":"viewer"}')`, c, uid)

	s, res := eraseIn(t, ctx, st.AdminPool(), c, "", "CAROL@example.com", false)
	if s.UserID != "" {
		t.Fatalf("UserID = %q, want none", s.UserID)
	}
	sum := sha256.Sum256([]byte("carol@example.com"))
	if want := "erased-user-" + hex.EncodeToString(sum[:])[:8]; res.StandIn != want {
		t.Errorf("stand-in = %q, want %q", res.StandIn, want)
	}
	if res.Counts["audit_log"] != 3 {
		t.Errorf("audit_log count = %d, want 3", res.Counts["audit_log"])
	}
	if res.Counts["users"] != 0 || res.Counts["dashboards"] != 1 {
		t.Errorf("counts = %v", res.Counts)
	}
	var left int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log t WHERE customer_id = $1 AND to_jsonb(t)::text ILIKE '%carol@example.com%'`, c).Scan(&left); err != nil || left != 0 {
		t.Errorf("audit rows still name her: %d (err %v)", left, err)
	}
	var anon int
	if err := su.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE customer_id = $1 AND actor = $2`, c, res.StandIn).Scan(&anon); err != nil || anon != 1 {
		t.Errorf("actor not replaced by stand-in: %d (err %v)", anon, err)
	}
	for _, a := range res.Actions {
		if strings.Contains(a, "Entra") {
			t.Errorf("Entra note without an account: %q", a)
		}
	}
}

// Vendor staff: their account is found in the vendor tenant; email-keyed rows
// and audit entries in customers are erased. Customer C, where the same email
// is also a live account, is left alone entirely (its rows can't be told
// apart from its own user's) and named in Actions; customer D, with only the
// staff member's traces, is anonymised.
func TestEraseVendorStaff(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-erase-vendor-c")
	d := mk("privacy-erase-vendor-d")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	cName := q(`SELECT name FROM customers WHERE id = $1`, c)
	vt := q(`INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id::text`, "vte-"+c)
	email := "staff-" + c[:8] + "@involve.example"
	uid := q(`INSERT INTO users (vendor_tenant_id, email, password_hash) VALUES ($1, $2, 'h') RETURNING id::text`, vt, strings.ToUpper(email[:1])+email[1:])
	custUID := q(`INSERT INTO users (customer_id, email, full_name, password_hash) VALUES ($1, $2, 'Cust Staff', 'h') RETURNING id::text`, c, email)
	for _, u := range []string{uid, custUID} {
		exec(`INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, u, "evs-"+u)
	}
	exec(`INSERT INTO user_roles (user_id, role_id) SELECT $1, r.id FROM roles r WHERE r.customer_id = $2 AND r.name = 'admin' AND r.is_system_default`, custUID, c)

	// The same rows in both customers: the email as actor and in text, the
	// staff account id as actor and as remote_session.user_id.
	for i, cust := range []string{c, d} {
		exec(`INSERT INTO audit_log (customer_id, actor, action, target_kind, actor_is_vendor) VALUES
			($1, $2, 't', 'device', true), ($1, $3, 'customer.export', 'customer', true)`, cust, email, "user:"+uid)
		exec(`INSERT INTO schedule_date (customer_id, on_date, closed, created_by) VALUES ($1, '2026-12-25', true, $2)`, cust, email)
		exec(`INSERT INTO dashboards (customer_id, name, created_by) VALUES ($1, 'Estate', $2)`, cust, email)
		exec(`INSERT INTO notification_channels (customer_id, name, type, target) VALUES ($1, $2, 'email', $3)`, cust, fmt.Sprintf("Channel%d-%s", i, c[:8]), email)
		coll := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'c', '\x00') RETURNING id::text`, cust)
		dev := q(`INSERT INTO devices (customer_id, collector_id) VALUES ($1, $2) RETURNING id::text`, cust, coll)
		exec(`INSERT INTO remote_session (customer_id, device_id, user_id, actor, host_label, scheme, port, launch_code_hash, expires_at, end_reason)
			VALUES ($1, $2, $3, $4, $5, 'https', 443, '\x01', now() + interval '1 hour', $6)`,
			cust, dev, uid, email, "evr-"+dev, "ended by "+email)
	}

	snapC := registerSnapshot(t, ctx, su, c, []string{custUID})
	_, dry := eraseIn(t, ctx, st.AdminPool(), "", vt, email, true)
	s, res := eraseIn(t, ctx, st.AdminPool(), "", vt, email, false)
	if s.UserID != uid {
		t.Fatalf("subject = %+v", s)
	}
	if fmt.Sprint(res.Counts) != fmt.Sprint(dry.Counts) || strings.Join(res.Actions, "\n") != strings.Join(dry.Actions, "\n") {
		t.Errorf("result %+v differs from dry run %+v", res, dry)
	}
	want := map[string]int64{
		"users": 1, "user_sessions": 1, "audit_log": 2, "schedule_date.created_by": 1, "dashboards.created_by": 1,
		"remote_session.actor": 1, "remote_session.end_reason": 1, "remote_session.user_id": 1,
	}
	if fmt.Sprint(res.Counts) != fmt.Sprint(want) {
		t.Errorf("counts = %v, want %v", res.Counts, want)
	}

	// C: untouched, the customer account included.
	if got := registerSnapshot(t, ctx, su, c, []string{custUID}); got != snapC {
		t.Errorf("customer C changed:\nbefore:\n%s\nafter:\n%s", snapC, got)
	}
	// D: nothing names the staff member, by email or by account id.
	for _, tbl := range []string{"audit_log", "schedule_date", "dashboards", "remote_session"} {
		var n int
		if err := su.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` t WHERE customer_id = $1
			AND (to_jsonb(t)::text ILIKE '%' || $2 || '%' OR to_jsonb(t)::text ILIKE '%' || $3 || '%')`, d, email, uid).Scan(&n); err != nil || n != 0 {
			t.Errorf("customer D %s: %d rows still name the staff member (err %v)", tbl, n, err)
		}
	}

	wantNote := fmt.Sprintf(OverlapNote, cName)
	joined := strings.Join(res.Actions, "\n")
	if !strings.Contains(joined, wantNote) {
		t.Errorf("actions don't name customer C (%q): %v", wantNote, res.Actions)
	}
	if !strings.Contains(joined, "Channel1-"+c[:8]) || strings.Contains(joined, "Channel0-"+c[:8]) {
		t.Errorf("actions should report D's channel and not C's: %v", res.Actions)
	}
}

// Erase rejects a subject it can't scope.
func TestEraseArgs(t *testing.T) {
	ctx, _, st, _ := setup(t)
	tx, err := st.AdminPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, s := range []Subject{
		{Email: "a@x.io"},
		{CustomerID: "00000000-0000-0000-0000-000000000001", VendorTenantID: "00000000-0000-0000-0000-000000000002", Email: "a@x.io"},
		{CustomerID: "00000000-0000-0000-0000-000000000001", Email: " "},
	} {
		if _, err := Erase(ctx, tx, s, true); err == nil {
			t.Errorf("Erase(%+v) should fail", s)
		}
	}
}
