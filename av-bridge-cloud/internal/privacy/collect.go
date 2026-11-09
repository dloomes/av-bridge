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
// returned once. audit_log matches as privacy_anonymise_audit does: actor
// (the email, or 'user:' plus one of the person's ids), target_id = one of
// those ids, or the email in before/after/metadata.
//
// Vendor staff (no CustomerID) act inside many customers, so Scoped tables
// other than users are searched across customers, except customers that have
// a users row with the same email (vendorOverlaps); users is filtered on
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
		if e.skipFor(s) {
			continue // Involve's own rows: never in a customer's export
		}
		if _, ok := byTable[e.Table]; !ok {
			order = append(order, e.Table)
		}
		byTable[e.Table] = append(byTable[e.Table], e)
	}

	ids, err := accountIDs(ctx, tx, s)
	if err != nil {
		return nil, fmt.Errorf("privacy: account ids: %w", err)
	}
	overlaps, err := vendorOverlaps(ctx, tx, s)
	if err != nil {
		return nil, fmt.Errorf("privacy: customer accounts with the email: %w", err)
	}
	excluded := overlapIDs(overlaps)

	var out []Table
	for _, name := range order {
		tb, err := collectTable(ctx, tx, s, ids, excluded, name, byTable[name])
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

// Overlap is a customer in which a vendor subject's email is also a live
// customer account.
type Overlap struct{ CustomerID, Name string }

// vendorOverlaps lists, for vendor staff, the customers that have a users row
// with the same email, by name. Rows in those customers can't be told apart
// from that customer's own user, so vendor-mode Collect and Erase leave them
// out entirely (Erase names them as action items). Empty for a customer's
// subject.
func vendorOverlaps(ctx context.Context, tx pgx.Tx, s Subject) ([]Overlap, error) {
	if s.CustomerID != "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text, c.name FROM customers c
		WHERE EXISTS (SELECT 1 FROM users u WHERE u.customer_id = c.id AND lower(u.email) = lower($1::text))
		ORDER BY c.name, c.id`, s.Email)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Overlap, error) {
		var o Overlap
		err := r.Scan(&o.CustomerID, &o.Name)
		return o, err
	})
}

func overlapIDs(os []Overlap) []string {
	ids := make([]string, 0, len(os))
	for _, o := range os {
		ids = append(ids, o.CustomerID)
	}
	return ids
}

// excludeScope is the condition leaving the excluded customers out of a
// Scoped table (sc, a sanitized column over alias t). Rows with no scope
// value are kept, as before.
func excludeScope(sc, param string) string {
	return ` AND (t.` + sc + ` IS NULL OR t.` + sc + ` <> ALL(` + param + `::uuid[]))`
}

// actorIDsSQL matches an audit actor recorded by account id ('user:<uuid>',
// the privacy and offboarding entries) against uids, a text[] of lower-case
// ids.
func actorIDsSQL(uids string) string {
	return `lower(t.actor) = ANY(SELECT 'user:' || u FROM unnest(` + uids + `) AS u)`
}

func collectTable(ctx context.Context, tx pgx.Tx, s Subject, ids, excluded []string, name string, entries []Entry) (Table, error) {
	// The scope filter is per table, taken from entries[0], so every entry
	// on the table must agree with it. Checked before any query.
	for _, e := range entries[1:] {
		if e.Scoped != entries[0].Scoped || e.ScopeColumn != entries[0].ScopeColumn {
			return Table{}, fmt.Errorf("register entries for %s disagree on scope (%s.%s)", name, e.Table, e.Column)
		}
	}
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
		// Same conditions as privacy_anonymise_audit's array overload
		// (migration 0077), which Erase calls with the same ids.
		if len(ids) > 0 {
			add(`lower(t.target_id) = ANY(` + uids() + `)`) // ids are lower-case
			add(actorIDsSQL(uids()))
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
	case entries[0].Scoped && len(excluded) > 0:
		// Vendor staff: not in customers where the email is also a live account.
		sc := entries[0].ScopeColumn
		if sc == "" {
			sc = "customer_id"
		}
		where += excludeScope(pgx.Identifier{sc}.Sanitize(), param("excluded", excluded))
	}

	return selectRows(ctx, tx, name, where, args)
}

// selectRows reads a table's exported columns (exportColumns) from the rows
// matching where (SQL over alias t, using args), ordered by the first
// column, with secrets redacted (redactRow).
func selectRows(ctx context.Context, tx pgx.Tx, name, where string, args []any) (Table, error) {
	tb := Table{Name: name}
	cols, err := eachRow(ctx, tx, name, where, args, func(row []any) error {
		tb.Rows = append(tb.Rows, row)
		return nil
	})
	tb.Columns = cols
	return tb, err
}

// eachRow streams a table's exported columns from the rows matching where
// (SQL over alias t, using args), ordered by the first column, to fn one row
// at a time. Values are decoded from to_jsonb, so every column type comes
// back as a JSON value; secrets are redacted (redactRow) before fn sees them.
// Returns the column names.
func eachRow(ctx context.Context, tx pgx.Tx, name, where string, args []any, fn func(row []any) error) ([]string, error) {
	cols, err := exportColumns(ctx, tx, name)
	if err != nil {
		return nil, err
	}
	return cols, streamRows(ctx, tx, name, cols, where, args, fn)
}

// streamRows is eachRow with the columns already known.
func streamRows(ctx context.Context, tx pgx.Tx, name string, cols []string, where string, args []any, fn func(row []any) error) error {
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = "t." + pgx.Identifier{c}.Sanitize()
	}
	q := `SELECT to_jsonb(x)::text FROM (SELECT ` + strings.Join(sel, ", ") + ` FROM ` + pgx.Identifier{name}.Sanitize() +
		` t WHERE ` + where + `) x ORDER BY x.` + pgx.Identifier{cols[0]}.Sanitize()
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return err
		}
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = m[c]
		}
		redactRow(name, cols, row)
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
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
