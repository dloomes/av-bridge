package privacy

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/retention"
)

// WriteZIP writes an access export: <name>.csv and <name>.json for each
// table, then README.txt. Callers build it into a buffer or temp file and
// send it only when this returns nil, so a failure never reaches the person
// as a partial archive.
func WriteZIP(w io.Writer, tables []Table, readme string) error {
	zw := zip.NewWriter(w)
	for _, tb := range tables {
		f, err := zw.Create(tb.Name + ".csv")
		if err != nil {
			return err
		}
		if err := writeCSV(f, tb); err != nil {
			return fmt.Errorf("privacy: %s.csv: %w", tb.Name, err)
		}
		f, err = zw.Create(tb.Name + ".json")
		if err != nil {
			return err
		}
		if err := writeJSON(f, tb); err != nil {
			return fmt.Errorf("privacy: %s.json: %w", tb.Name, err)
		}
	}
	f, err := zw.Create("README.txt")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, readme); err != nil {
		return err
	}
	return zw.Close()
}

func writeCSV(w io.Writer, tb Table) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(tb.Columns); err != nil {
		return err
	}
	rec := make([]string, len(tb.Columns))
	for _, r := range tb.Rows {
		for i := range rec {
			s, err := cell(r[i])
			if err != nil {
				return err
			}
			rec[i] = s
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// cell renders one value for CSV: text as is, NULL as empty, numbers and
// booleans as written, objects and lists as JSON.
func cell(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	default:
		b, err := marshal(x)
		return string(b), err
	}
}

// writeJSON writes an array of objects, one per row, keys in column order.
func writeJSON(w io.Writer, tb Table) error {
	var buf bytes.Buffer
	buf.WriteString("[")
	for ri, r := range tb.Rows {
		if ri > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  {")
		for i, c := range tb.Columns {
			if i > 0 {
				buf.WriteString(", ")
			}
			k, err := marshal(c)
			if err != nil {
				return err
			}
			v, err := marshal(r[i])
			if err != nil {
				return err
			}
			buf.Write(k)
			buf.WriteString(": ")
			buf.Write(v)
		}
		buf.WriteString("}")
	}
	buf.WriteString("\n]\n")
	_, err := w.Write(buf.Bytes())
	return err
}

// marshal is json.Marshal without HTML escaping, so "<" and "&" stay readable.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

var categoryText = map[string]string{
	"account":  "your account and sign-in sessions",
	"network":  "IP addresses and browser details",
	"activity": "things you did, or that were done to your account",
	"contact":  "contact settings that use your email address",
}

// AccessREADME explains an access export: each table with its categories,
// personal columns and retention period, and where else copies may be.
// It does not repeat the email address.
func AccessREADME(s Subject, tables []Table) string {
	keep := map[string]string{}
	for _, r := range retention.Rules {
		keep[r.Table] = r.Name
	}
	var b strings.Builder
	b.WriteString("Personal data export\n")
	b.WriteString("====================\n\n")
	b.WriteString("This archive holds the personal data M.A.R.C.U.S. stores about the person in this request,\n")
	b.WriteString("found by their email address")
	if s.VendorTenantID != "" {
		b.WriteString(" (a service provider staff account)")
	}
	b.WriteString(".\n\n")
	b.WriteString("Each table has two files with the same rows: <table>.csv and <table>.json.\n")
	b.WriteString("Whole rows are included so each value keeps its context; passwords, token hashes and\n")
	b.WriteString("other secrets are left out. In the CSV files, lists and objects are written as JSON.\n\n")
	if len(tables) == 0 {
		b.WriteString("No personal data was found.\n\n")
	} else {
		b.WriteString("Tables\n------\n")
	}
	for _, tb := range tables {
		cats := map[string]bool{}
		var cols []string
		seen := map[string]bool{}
		for _, e := range Personal {
			if e.Table != tb.Name {
				continue
			}
			cats[e.Category] = true
			if !seen[e.Column] {
				seen[e.Column] = true
				cols = append(cols, e.Column)
			}
		}
		if tb.Name == "audit_log" {
			// Matched on more than the register entry: see Collect.
			cols = append(cols, "target_id", "before", "after", "metadata")
		}
		catList := make([]string, 0, len(cats))
		for c := range cats {
			catList = append(catList, c)
		}
		sort.Strings(catList)
		ret := keep[tb.Name]
		if ret == "" {
			ret = "no fixed period; kept until changed or deleted, or until the organisation's data is deleted"
		}
		fmt.Fprintf(&b, "\n%s (%d rows)\n", tb.Name, len(tb.Rows))
		fmt.Fprintf(&b, "  Categories: %s\n", strings.Join(catList, ", "))
		fmt.Fprintf(&b, "  Personal columns: %s\n", strings.Join(cols, ", "))
		fmt.Fprintf(&b, "  Retention: %s\n", ret)
	}
	b.WriteString("\nCategories\n----------\n")
	for _, c := range []string{"account", "network", "activity", "contact"} {
		fmt.Fprintf(&b, "  %-9s %s\n", c, categoryText[c])
	}
	b.WriteString("\nOther copies\n------------\n")
	b.WriteString("Not included here: application logs (30 days) and backups (35 days) may hold copies\n")
	b.WriteString("that expire on their own.\n")
	return b.String()
}
