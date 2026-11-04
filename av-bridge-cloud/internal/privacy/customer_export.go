package privacy

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// customerExportSkipped are customer_id tables left out of a customer
// export: raw telemetry (telemetry_daily holds its daily summary) and the
// vendor's own deletion evidence, which is not the customer's data.
var customerExportSkipped = []string{"telemetry", "customer_deletions"}

// customerChildren are the customer's tables with no customer_id column,
// found through a parent that has one. Sign-in sessions and one-time tokens
// are credentials, not the organisation's data, and are left out.
var customerChildren = []struct{ name, where string }{
	{"user_roles", `t.user_id IN (SELECT u.id FROM users u WHERE u.customer_id = $1::uuid)`},
	{"role_permissions", `t.role_id IN (SELECT r.id FROM roles r WHERE r.customer_id = $1::uuid)`},
}

// CollectCustomer gathers a customer's whole dataset for its offboarding
// export: the customers row, every table with a customer_id column (found in
// the catalogue, so new tables are included without a code change) except
// customerExportSkipped, and customerChildren. Every table is returned, even
// when empty, so the archive shows what was looked at. Credential columns
// are left out as in Collect (exportExcluded).
//
// Run it in an AdminPool transaction (RLS is bypassed; every query filters
// on the customer). Any error fails the whole collection: an export is never
// partial. The table list is read from pg_catalog, not information_schema,
// so a table the role cannot read fails loudly instead of being skipped.
func CollectCustomer(ctx context.Context, tx pgx.Tx, customerID string) ([]Table, error) {
	if strings.TrimSpace(customerID) == "" {
		return nil, fmt.Errorf("privacy: customer export needs a customer")
	}
	rows, err := tx.Query(ctx, `
		SELECT c.relname::text
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND a.attname = 'customer_id' AND NOT a.attisdropped
		   AND c.relname <> ALL($1::text[])
		 ORDER BY 1`, customerExportSkipped)
	if err != nil {
		return nil, fmt.Errorf("privacy: list customer tables: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("privacy: list customer tables: %w", err)
	}

	args := []any{customerID}
	out := make([]Table, 0, len(names)+1+len(customerChildren))
	add := func(name, where string) error {
		tb, err := selectRows(ctx, tx, name, where, args)
		if err != nil {
			return fmt.Errorf("privacy: export %s: %w", name, err)
		}
		out = append(out, tb)
		return nil
	}
	if err := add("customers", `t.id = $1::uuid`); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := add(name, `t.customer_id = $1::uuid`); err != nil {
			return nil, err
		}
	}
	for _, c := range customerChildren {
		if err := add(c.name, c.where); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CustomerREADME explains a customer export: what it holds and, per table,
// its row count and the columns the register marks as personal data.
func CustomerREADME(name string, tables []Table) string {
	personal := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range Personal {
		k := e.Table + "." + e.Column
		if !seen[k] {
			seen[k] = true
			personal[e.Table] = append(personal[e.Table], e.Column)
		}
	}
	var b strings.Builder
	b.WriteString("Organisation data export\n")
	b.WriteString("========================\n\n")
	fmt.Fprintf(&b, "Organisation: %s\n", name)
	fmt.Fprintf(&b, "Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("This archive holds everything M.A.R.C.U.S. stores for this organisation: one table per\n")
	b.WriteString("pair of files, <table>.csv and <table>.json, with the same rows. Empty tables are included.\n")
	b.WriteString("Device telemetry is given as daily summaries (telemetry_daily), not raw readings.\n")
	b.WriteString("Passwords, token hashes, encrypted secrets and binary data (such as logos) are left out,\n")
	b.WriteString("and so are sign-in sessions and one-time sign-in tokens.\n")
	b.WriteString("In the CSV files, lists and objects are written as JSON, and text that a spreadsheet could\n")
	b.WriteString("run as a formula (starting with =, +, -, @, a tab or a carriage return) is prefixed with ' ;\n")
	b.WriteString("the JSON files hold the exact values.\n\n")
	b.WriteString("Tables\n------\n")
	for _, tb := range tables {
		fmt.Fprintf(&b, "\n%s (%d rows)\n", tb.Name, len(tb.Rows))
		if cols := personal[tb.Name]; len(cols) > 0 {
			fmt.Fprintf(&b, "  Personal data columns: %s\n", strings.Join(cols, ", "))
		}
	}
	return b.String()
}

// Deletion is one customer_deletions row: the evidence of an offboarding.
type Deletion struct {
	ID, CustomerID, CustomerName string
	StartedAt                    time.Time
	StartedBy                    string
	CancelledAt                  *time.Time
	CancelledBy                  string
	DeletedAt                    *time.Time
	RowCounts                    map[string]int64
}

// Status is "deleted", "cancelled" or "offboarding".
func (d Deletion) Status() string {
	switch {
	case d.DeletedAt != nil:
		return "deleted"
	case d.CancelledAt != nil:
		return "cancelled"
	default:
		return "offboarding"
	}
}

// WriteDeletionCertificate writes a deletion certificate as CSV: a header
// block of field,value lines, a blank line, then table,rows lines (the rows
// deleted per table, empty until the deletion has run).
func WriteDeletionCertificate(w io.Writer, d Deletion) error {
	cw := csv.NewWriter(w)
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	recs := [][]string{
		{"M.A.R.C.U.S. customer deletion certificate"},
		{"certificate_id", d.ID},
		{"customer_id", d.CustomerID},
		{"customer_name", csvSafe(d.CustomerName)},
		{"status", d.Status()},
		{"started_at", ts(&d.StartedAt)},
		{"started_by", csvSafe(d.StartedBy)},
		{"cancelled_at", ts(d.CancelledAt)},
		{"cancelled_by", csvSafe(d.CancelledBy)},
		{"deleted_at", ts(d.DeletedAt)},
		{""},
		{"table", "rows"},
	}
	tables := make([]string, 0, len(d.RowCounts))
	for t := range d.RowCounts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		recs = append(recs, []string{csvSafe(t), strconv.FormatInt(d.RowCounts[t], 10)})
	}
	if err := cw.WriteAll(recs); err != nil {
		return err
	}
	return cw.Error()
}
