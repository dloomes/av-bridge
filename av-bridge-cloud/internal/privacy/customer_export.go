package privacy

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
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

// TableCount is one table of a customer export and its row count.
type TableCount struct {
	Name string
	Rows int
}

// customerTables lists what a customer export reads, as (table, where)
// pairs over alias t with $1 = the customer id: the customers row, every
// table with a customer_id column (found in the catalogue, so new tables are
// included without a code change) except customerExportSkipped, then
// customerChildren. The list comes from pg_catalog, not information_schema,
// so a table the role cannot read fails the export loudly instead of being
// skipped.
func customerTables(ctx context.Context, tx pgx.Tx) ([][2]string, error) {
	names, err := db.CustomerIDTables(ctx, tx, customerExportSkipped...)
	if err != nil {
		return nil, err
	}
	out := [][2]string{{"customers", `t.id = $1::uuid`}}
	for _, n := range names {
		out = append(out, [2]string{n, `t.customer_id = $1::uuid`})
	}
	for _, c := range customerChildren {
		out = append(out, [2]string{c.name, c.where})
	}
	return out, nil
}

// WriteCustomerExport writes a customer's whole dataset to w as a ZIP:
// <table>.csv and <table>.json for every table of customerTables (empty
// ones too, so the archive shows what was looked at), then README.txt
// (CustomerREADME). Credential columns are left out and secrets redacted as
// in Collect (exportExcluded, redactRow).
//
// Rows are streamed from the database into the archive, one row in memory
// at a time; each table's JSON is spooled to a temporary file while its CSV
// is written, since ZIP entries are written one after the other. Write to a
// file, not a response: on any error the caller must discard w, so an export
// is never partial. Run it in one AdminPool transaction (RLS is bypassed;
// every query filters on the customer), REPEATABLE READ for one snapshot.
func WriteCustomerExport(ctx context.Context, tx pgx.Tx, customerID, name string, w io.Writer) ([]TableCount, error) {
	if strings.TrimSpace(customerID) == "" {
		return nil, fmt.Errorf("privacy: customer export needs a customer")
	}
	tables, err := customerTables(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("privacy: list customer tables: %w", err)
	}
	spool, err := os.CreateTemp("", "customer-export-json-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		spool.Close()
		os.Remove(spool.Name())
	}()

	zw := zip.NewWriter(w)
	args := []any{customerID}
	counts := make([]TableCount, 0, len(tables))
	for _, tw := range tables {
		n, err := writeTable(ctx, tx, zw, spool, tw[0], tw[1], args)
		if err != nil {
			return nil, fmt.Errorf("privacy: export %s: %w", tw[0], err)
		}
		counts = append(counts, TableCount{Name: tw[0], Rows: n})
	}
	f, err := zw.Create("README.txt")
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(f, CustomerREADME(name, counts)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return counts, nil
}

// writeTable streams one table into <name>.csv and, through spool,
// <name>.json. Returns the row count.
func writeTable(ctx context.Context, tx pgx.Tx, zw *zip.Writer, spool *os.File, name, where string, args []any) (int, error) {
	cols, err := exportColumns(ctx, tx, name)
	if err != nil {
		return 0, err
	}
	if err := spool.Truncate(0); err != nil {
		return 0, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	js := bufio.NewWriter(spool)
	f, err := zw.Create(name + ".csv")
	if err != nil {
		return 0, err
	}
	cw := csv.NewWriter(f)
	if err := cw.Write(cols); err != nil {
		return 0, err
	}
	js.WriteString("[")
	n := 0
	err = streamRows(ctx, tx, name, cols, where, args, func(row []any) error {
		rec, err := csvRecord(row)
		if err != nil {
			return err
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
		obj, err := jsonRow(cols, row)
		if err != nil {
			return err
		}
		if n > 0 {
			js.WriteString(",")
		}
		js.WriteString("\n  ")
		js.Write(obj)
		n++
		return nil
	})
	if err != nil {
		return 0, err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, err
	}
	js.WriteString("\n]\n")
	if err := js.Flush(); err != nil {
		return 0, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	f, err = zw.Create(name + ".json")
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(f, spool); err != nil {
		return 0, err
	}
	return n, nil
}

// CustomerREADME explains a customer export: what it holds, what is
// redacted and, per table, its row count and the columns the register marks
// as personal data.
func CustomerREADME(name string, tables []TableCount) string {
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
	b.WriteString("Sign-in sessions and one-time sign-in tokens are not included.\n")
	b.WriteString(RedactionNote)
	b.WriteString("In the CSV files, lists and objects are written as JSON, and text that a spreadsheet could\n")
	b.WriteString("run as a formula (starting with =, +, -, @, a tab or a carriage return) is prefixed with ' ;\n")
	b.WriteString("the JSON files hold the exact values.\n\n")
	b.WriteString("Tables\n------\n")
	for _, tb := range tables {
		fmt.Fprintf(&b, "\n%s (%d rows)\n", tb.Name, tb.Rows)
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
	// Downloads are the customer exports taken during the offboarding, in
	// order: who (operator identity) and when (RFC 3339).
	Downloads []Download
}

// Download is one customer export recorded on a deletion.
type Download struct{ By, At string }

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
// block of field,value lines, a blank line, downloaded_by,downloaded_at lines
// (each customer export taken), a blank line, then table,rows lines (the rows
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
		{"downloaded_by", "downloaded_at"},
	}
	for _, dl := range d.Downloads {
		recs = append(recs, []string{csvSafe(dl.By), csvSafe(dl.At)})
	}
	recs = append(recs, []string{""}, []string{"table", "rows"})
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
