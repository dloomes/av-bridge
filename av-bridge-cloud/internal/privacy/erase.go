package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EntraNote is the action item added when the erased account signed in
// through Microsoft Entra ID: Entra sign-in creates the account again.
const EntraNote = "Remove this person from Microsoft Entra ID too, or they will be created again as a new account when they next sign in."

// StandIn is the text that replaces the person wherever a record has to keep
// a value: "erased-user-" plus the first 8 hex characters of the user id
// (dashes removed), or, with no account left, of sha256(lower(email)).
func StandIn(s Subject) string {
	if id := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s.UserID)), "-", ""); len(id) >= 8 {
		return "erased-user-" + id[:8]
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s.Email))))
	return "erased-user-" + hex.EncodeToString(sum[:])[:8]
}

// Result is what an erasure did (or, for a dry run, would do).
type Result struct {
	StandIn string
	// Counts maps "table.column" to the rows changed for a replaced column,
	// and "table" to the rows deleted (audit_log: entries anonymised). Only
	// non-zero counts are present.
	Counts map[string]int64
	// Actions are the things an administrator still has to do: every
	// ActReport match, named, with its reason, and the Entra note.
	Actions []string
}

// Erase anonymises the Subject across every register entry, in register
// order, inside tx (an AdminPool transaction: RLS is bypassed, so every
// Scoped statement is filtered on its scope column here, as in Collect).
//
//   - ActReplace: user-id, IP address and browser columns become NULL;
//     email and name text columns get the stand-in (KeyEmailInText: only the
//     matched address is replaced).
//   - ActDeleteRow: rows found through the entry's key column are deleted,
//     once per table and key column. Personal dashboards (owner_user_id) go;
//     shared ones keep their content and get the stand-in as created_by.
//   - ActStripJSON (audit_log): privacy_anonymise_audit, once per
//     organisation, with every account id the person has had (accountIDs).
//   - ActReport: nothing changes; a named action item is added.
//
// users is last in the register, so the account row goes after everything
// that refers to it. Erase does not write the privacy.erase audit entry: the
// caller, who knows the actor, does that in the same transaction.
//
// With dryRun, the same statements run inside a savepoint that is rolled
// back, so the counts are exact and nothing changes.
func Erase(ctx context.Context, tx pgx.Tx, s Subject, dryRun bool) (Result, error) {
	if strings.TrimSpace(s.Email) == "" {
		return Result{}, errors.New("privacy: subject has no email")
	}
	if (s.CustomerID == "") == (s.VendorTenantID == "") {
		return Result{}, errors.New("privacy: subject needs exactly one of customer and vendor tenant")
	}
	if !dryRun {
		return erase(ctx, tx, s)
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer sp.Rollback(ctx) //nolint:errcheck // rolled back below; this covers the error paths
	res, err := erase(ctx, sp, s)
	if err != nil {
		return Result{}, err
	}
	if err := sp.Rollback(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

func erase(ctx context.Context, tx pgx.Tx, s Subject) (Result, error) {
	ids, err := accountIDs(ctx, tx, s)
	if err != nil {
		return Result{}, fmt.Errorf("privacy: account ids: %w", err)
	}
	res := Result{StandIn: StandIn(s), Counts: map[string]int64{}}
	add := func(key string, n int64) {
		if n > 0 {
			res.Counts[key] += n
		}
	}

	entra := false
	if s.UserID != "" {
		var provider string
		if err := tx.QueryRow(ctx, `SELECT provider FROM users WHERE id = $1::uuid`, s.UserID).Scan(&provider); err != nil {
			return Result{}, fmt.Errorf("privacy: account provider: %w", err)
		}
		entra = provider == "entra"
	}

	deleted := map[string]bool{}
	for _, e := range Personal {
		var (
			n   int64
			err error
		)
		switch e.Action {
		case ActReplace:
			n, err = eraseReplace(ctx, tx, s, ids, res.StandIn, e)
			add(e.Table+"."+e.Column, n)
		case ActDeleteRow:
			k := e.Table + "." + keyColumn(e)
			if deleted[k] {
				continue
			}
			deleted[k] = true
			n, err = eraseDelete(ctx, tx, s, ids, e)
			add(e.Table, n)
		case ActStripJSON:
			if e.Table != "audit_log" {
				return Result{}, fmt.Errorf("privacy: ActStripJSON on %s.%s is not supported", e.Table, e.Column)
			}
			n, err = anonymiseAudit(ctx, tx, s, ids, res.StandIn)
			add(e.Table, n)
		case ActReport:
			var msgs []string
			msgs, err = eraseReport(ctx, tx, s, ids, e)
			res.Actions = append(res.Actions, msgs...)
		default:
			return Result{}, fmt.Errorf("privacy: unknown action %d on %s.%s", e.Action, e.Table, e.Column)
		}
		if err != nil {
			return Result{}, fmt.Errorf("privacy: erase %s.%s: %w", e.Table, e.Column, err)
		}
	}
	if entra {
		res.Actions = append(res.Actions, EntraNote)
	}
	return res, nil
}

func keyColumn(e Entry) string {
	if e.KeyColumn != "" {
		return e.KeyColumn
	}
	return e.Column
}

// stmt builds one statement's WHERE clause and its arguments, numbering
// placeholders as they are first used.
type stmt struct {
	args []any
	slot map[string]string
}

func (b *stmt) param(key string, v any) string {
	if b.slot == nil {
		b.slot = map[string]string{}
	}
	if p, ok := b.slot[key]; ok {
		return p
	}
	b.args = append(b.args, v)
	b.slot[key] = fmt.Sprintf("$%d", len(b.args))
	return b.slot[key]
}

// where returns the condition finding the entry's rows for s: its key
// condition plus the scope filter. ok is false when nothing can match (a
// user-id key and no account id).
func (b *stmt) where(s Subject, ids []string, e Entry) (cond string, ok bool, err error) {
	k := "t." + pgx.Identifier{keyColumn(e)}.Sanitize()
	switch e.Key {
	case KeyUserID:
		if len(ids) == 0 {
			return "", false, nil
		}
		cond = k + ` = ANY(` + b.param("ids", ids) + `::uuid[])`
	case KeyEmail:
		cond = `lower(` + k + `) = lower(` + b.param("email", s.Email) + `::text)`
	case KeyEmailInText:
		cond = k + ` ~* ` + b.param("pattern", EmailPattern(s.Email)) + `::text`
	case KeyEmailJSON:
		cond = `EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(` + k + `) = 'array' THEN ` + k +
			` ELSE '[]'::jsonb END) AS el(v) WHERE lower(el.v->>'target') = lower(` + b.param("email", s.Email) + `::text))`
	default:
		return "", false, fmt.Errorf("unknown key kind %d", e.Key)
	}
	// Scope, as in Collect: a customer's subject is confined to that customer
	// on every Scoped table; vendor staff act across customers, so only their
	// own users row is confined (to the vendor tenant).
	switch {
	case e.Table == "users" && s.VendorTenantID != "":
		cond += ` AND t.vendor_tenant_id = ` + b.param("vendor", s.VendorTenantID) + `::uuid`
	case e.Scoped && s.CustomerID != "":
		sc := e.ScopeColumn
		if sc == "" {
			sc = "customer_id"
		}
		cond += ` AND t.` + pgx.Identifier{sc}.Sanitize() + ` = ` + b.param("customer", s.CustomerID) + `::uuid`
	case e.Table == "users":
		return "", false, errors.New("users statement without a scope")
	}
	return cond, true, nil
}

// eraseReplace clears or replaces one column. NULL for IP address and
// browser columns (Category "network") and for a column that is itself the
// user-id key; the stand-in for email and name text.
func eraseReplace(ctx context.Context, tx pgx.Tx, s Subject, ids []string, standIn string, e Entry) (int64, error) {
	var b stmt
	cond, ok, err := b.where(s, ids, e)
	if err != nil || !ok {
		return 0, err
	}
	col := pgx.Identifier{e.Column}.Sanitize()
	var set string
	switch {
	case e.Category == "network" || (e.Key == KeyUserID && e.KeyColumn == ""):
		set = col + ` = NULL`
		cond += ` AND t.` + col + ` IS NOT NULL`
	case e.Key == KeyEmailInText && e.KeyColumn == "":
		set = col + ` = regexp_replace(t.` + col + `, ` + b.param("pattern", EmailPattern(s.Email)) + `::text, ` +
			b.param("standin", standIn) + `::text, 'gi')`
	case e.Key == KeyEmail:
		set = col + ` = ` + b.param("standin", standIn) + `::text`
	default:
		return 0, fmt.Errorf("no replacement rule for key kind %d, category %q", e.Key, e.Category)
	}
	tag, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{e.Table}.Sanitize()+` t SET `+set+` WHERE `+cond, b.args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func eraseDelete(ctx context.Context, tx pgx.Tx, s Subject, ids []string, e Entry) (int64, error) {
	var b stmt
	cond, ok, err := b.where(s, ids, e)
	if err != nil || !ok {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM `+pgx.Identifier{e.Table}.Sanitize()+` t WHERE `+cond, b.args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// anonymiseAudit runs privacy_anonymise_audit (0075's array overload: every
// account id at once, target_id matched case-insensitively, so each entry is
// counted once). A customer's subject is anonymised in that customer only;
// vendor staff in every customer whose log names them, as Collect finds them.
func anonymiseAudit(ctx context.Context, tx pgx.Tx, s Subject, ids []string, standIn string) (int64, error) {
	if ids == nil {
		ids = []string{}
	}
	customers := []string{s.CustomerID}
	if s.CustomerID == "" {
		rows, err := tx.Query(ctx, `SELECT DISTINCT t.customer_id::text FROM audit_log t
			WHERE lower(t.actor) = lower($1::text)
			   OR lower(t.target_id) = ANY($2::text[])
			   OR (coalesce(t.before::text, '') || ' ' || coalesce(t."after"::text, '') || ' ' || coalesce(t.metadata::text, '')) ~* $3::text
			ORDER BY 1`, s.Email, ids, EmailPattern(s.Email))
		if err != nil {
			return 0, err
		}
		customers, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, err
		}
	}
	var total int64
	for _, c := range customers {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT privacy_anonymise_audit($1::uuid, $2::text, $3::text[], $4::text)`,
			c, s.Email, ids, standIn).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// reportLabels name what an ActReport row is, for the action item. Entries
// without one are reported by table and row count.
var reportLabels = map[string]struct{ expr, msg string }{
	"notification_channels.target": {
		`t.name`, `Notification channel "%s" sends to this email.`},
	"room_nightly_config.notification_recipients": {
		`(SELECT r.name FROM rooms r WHERE r.id = t.room_id)`, `Room "%s" sends its readiness report to this email.`},
	"nightly_schedule.helpdesk_email": {
		``, `The nightly report helpdesk address is this email.`},
	"customers.support_contact": {
		`t.name`, `Organisation "%s" publishes this email as its support contact.`},
	"customers.offboarding_started_by": {
		`t.name`, `This email started the offboarding of organisation "%s".`},
	"customer_deletions.started_by": {
		`t.customer_name`, `This email started the offboarding of organisation "%s".`},
	"customer_deletions.cancelled_by": {
		`t.customer_name`, `This email cancelled the offboarding of organisation "%s".`},
}

func eraseReport(ctx context.Context, tx pgx.Tx, s Subject, ids []string, e Entry) ([]string, error) {
	var b stmt
	cond, ok, err := b.where(s, ids, e)
	if err != nil || !ok {
		return nil, err
	}
	table := pgx.Identifier{e.Table}.Sanitize()
	label, named := reportLabels[e.Table+"."+e.Column]
	if named && label.expr == "" {
		// One fixed message, however many rows match.
		var found bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` t WHERE `+cond+`)`, b.args...).Scan(&found); err != nil || !found {
			return nil, err
		}
		return []string{label.msg + " " + e.Reason}, nil
	}
	if !named {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` t WHERE `+cond, b.args...).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, nil
		}
		return []string{fmt.Sprintf("%s.%s holds this email (%d %s). %s", e.Table, e.Column, n, plural(n, "row", "rows"), e.Reason)}, nil
	}
	rows, err := tx.Query(ctx, `SELECT coalesce(`+label.expr+`, '') FROM `+table+` t WHERE `+cond+` ORDER BY 1`, b.args...)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		out = append(out, fmt.Sprintf(label.msg, n)+" "+e.Reason)
	}
	return out, nil
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
