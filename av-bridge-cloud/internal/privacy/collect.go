package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Table is one database table's rows about a Subject. Columns are in table
// order; each row holds one value per column, decoded from JSON (string,
// json.Number, bool, nil, map[string]any or []any).
type Table struct {
	Name    string
	Columns []string
	Rows    [][]any
}

// EmailPattern builds the regular expression privacy_anonymise_audit
// (migration 0073) uses to find an email inside text: the address
// regex-escaped, not preceded or followed by an address character, a trailing
// full stop allowed. Use it with ~* (case-insensitive). It must stay identical
// to the SQL: TestEmailPatternMatchesMigration runs the migration's own
// statements and compares.
func EmailPattern(email string) string {
	var b strings.Builder
	for _, r := range email {
		if strings.ContainsRune(`]^$|?*+(){}.\[`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return `(?<![A-Za-z0-9._%+-])` + b.String() + `(?![A-Za-z0-9_%+-]|[.][A-Za-z0-9])`
}

// exportExcluded reports whether a column is left out of the export:
// credential material (hashes, encrypted values) and binary data (logos).
// None of it is information about the person, and a hash is a secret.
func exportExcluded(name, dataType string) bool {
	return dataType == "bytea" || strings.HasSuffix(name, "_hash") || strings.HasSuffix(name, "_enc")
}

// Collect gathers every row the register says holds the Subject's personal
// data, one Table per database table with at least one row, in register
// order. Whole rows are exported (less exportExcluded columns) so each value
// keeps its context. Run it in an AdminPool transaction: RLS is bypassed, so
// every Scoped table is filtered on its scope column here.
//
// Several entries on one table are OR-ed into one query, so a row is
// returned once. audit_log matches as privacy_anonymise_audit does: actor,
// target_id = the user's id, or the email in before/after/metadata.
//
// Vendor staff (no CustomerID) act inside many customers, so Scoped tables
// other than users are searched across customers; users is filtered on
// vendor_tenant_id.
func Collect(ctx context.Context, tx pgx.Tx, s Subject) ([]Table, error) {
	if strings.TrimSpace(s.Email) == "" {
		return nil, errors.New("privacy: subject has no email")
	}
	if (s.CustomerID == "") == (s.VendorTenantID == "") {
		return nil, errors.New("privacy: subject needs exactly one of customer and vendor tenant")
	}

	var order []string
	byTable := map[string][]Entry{}
	for _, e := range Personal {
		if _, ok := byTable[e.Table]; !ok {
			order = append(order, e.Table)
		}
		byTable[e.Table] = append(byTable[e.Table], e)
	}

	ids, err := accountIDs(ctx, tx, s)
	if err != nil {
		return nil, fmt.Errorf("privacy: account ids: %w", err)
	}

	var out []Table
	for _, name := range order {
		tb, err := collectTable(ctx, tx, s, ids, name, byTable[name])
		if err != nil {
			return nil, fmt.Errorf("privacy: collect %s: %w", name, err)
		}
		if len(tb.Rows) > 0 {
			out = append(out, tb)
		}
	}
	return out, nil
}

// accountIDs returns the person's user ids: the current account, if any, and
// for a customer's user, any earlier accounts with the same email. A
// hard-deleted account leaves rows that hold its id with no foreign key
// (dashboards.owner_user_id, remote_session.user_id, audit_log.target_id);
// the user.create, user.delete and user.magic_link_issued audit entries
// record the email against that id, which is how it is found again (an
// Entra user is also recreated with a new id at the next sign-in).
// Vendor staff get only their current account: their search already spans
// every customer, and a customer's user with the same email is not searched
// for by id.
func accountIDs(ctx context.Context, tx pgx.Tx, s Subject) ([]string, error) {
	var ids []string
	if s.UserID != "" {
		ids = append(ids, s.UserID)
	}
	if s.CustomerID == "" {
		return ids, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT t.target_id FROM audit_log t
		WHERE t.customer_id = $1::uuid AND t.target_kind = 'user'
		  AND t.target_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		  AND (lower(t.before->>'email') = lower($2::text)
		       OR lower(t."after"->>'email') = lower($2::text)
		       OR lower(t."after"->>'target_email') = lower($2::text))
		ORDER BY 1`, s.CustomerID, s.Email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !strings.EqualFold(id, s.UserID) {
			ids = append(ids, strings.ToLower(id))
		}
	}
	return ids, rows.Err()
}

func collectTable(ctx context.Context, tx pgx.Tx, s Subject, ids []string, name string, entries []Entry) (Table, error) {
	// Placeholders are numbered as first used, so the query references
	// exactly the arguments it is given.
	var args []any
	slot := map[string]string{}
	param := func(key string, v any) string {
		if p, ok := slot[key]; ok {
			return p
		}
		args = append(args, v)
		slot[key] = fmt.Sprintf("$%d", len(args))
		return slot[key]
	}
	email := func() string { return param("email", s.Email) + "::text" }
	pattern := func() string { return param("pattern", EmailPattern(s.Email)) + "::text" }
	uids := func() string { return param("uids", ids) + "::text[]" }
	var conds []string
	seen := map[string]bool{}
	add := func(c string) {
		if !seen[c] {
			seen[c] = true
			conds = append(conds, c)
		}
	}
	for _, e := range entries {
		kc := e.KeyColumn
		if kc == "" {
			kc = e.Column
		}
		k := "t." + pgx.Identifier{kc}.Sanitize()
		switch e.Key {
		case KeyUserID:
			if len(ids) > 0 {
				add(k + ` = ANY(` + uids() + `::uuid[])`)
			}
		case KeyEmail:
			add(`lower(` + k + `) = lower(` + email() + `)`)
		case KeyEmailInText:
			add(k + ` ~* ` + pattern())
		case KeyEmailJSON:
			add(`EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(` + k + `) = 'array' THEN ` + k +
				` ELSE '[]'::jsonb END) AS el(v) WHERE lower(el.v->>'target') = lower(` + email() + `))`)
		default:
			return Table{}, fmt.Errorf("unknown key kind %d on %s.%s", e.Key, e.Table, e.Column)
		}
	}
	if name == "audit_log" {
		// Same conditions as privacy_anonymise_audit (migration 0073), with
		// target_id widened to earlier accounts (see accountIDs).
		if len(ids) > 0 {
			add(`t.target_id = ANY(` + uids() + `)`)
		}
		add(`(coalesce(t.before::text, '') || ' ' || coalesce(t."after"::text, '') || ' ' || coalesce(t.metadata::text, '')) ~* ` + pattern())
	}
	if len(conds) == 0 {
		return Table{Name: name}, nil
	}

	where := "(" + strings.Join(conds, " OR ") + ")"
	switch {
	case name == "users" && s.VendorTenantID != "":
		where += ` AND t.vendor_tenant_id = ` + param("scope", s.VendorTenantID) + `::uuid`
	case entries[0].Scoped && s.CustomerID != "":
		sc := entries[0].ScopeColumn
		if sc == "" {
			sc = "customer_id"
		}
		where += ` AND t.` + pgx.Identifier{sc}.Sanitize() + ` = ` + param("scope", s.CustomerID) + `::uuid`
	}

	cols, err := exportColumns(ctx, tx, name)
	if err != nil {
		return Table{}, err
	}
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = "t." + pgx.Identifier{c}.Sanitize()
	}
	q := `SELECT to_jsonb(x)::text FROM (SELECT ` + strings.Join(sel, ", ") + ` FROM ` + pgx.Identifier{name}.Sanitize() +
		` t WHERE ` + where + ` ORDER BY ` + sel[0] + `) x`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return Table{}, err
	}
	defer rows.Close()
	tb := Table{Name: name, Columns: cols}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return Table{}, err
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return Table{}, err
		}
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = m[c]
		}
		tb.Rows = append(tb.Rows, row)
	}
	return tb, rows.Err()
}

// exportColumns lists a table's exported columns in table order.
func exportColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c, dt string
		if err := rows.Scan(&c, &dt); err != nil {
			return nil, err
		}
		if !exportExcluded(c, dt) {
			cols = append(cols, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no exportable columns", table)
	}
	return cols, nil
}
