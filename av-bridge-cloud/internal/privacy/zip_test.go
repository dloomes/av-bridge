package privacy

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestWriteZIP(t *testing.T) {
	tables := []Table{
		{Name: "users", Columns: []string{"id", "email", "disabled_at"},
			Rows: [][]any{{"u1", "alice@example.com", nil}}},
		{Name: "audit_log", Columns: []string{"id", "actor", "after"},
			Rows: [][]any{
				{json.Number("7"), "alice@example.com", map[string]any{"role": "admin", "note": "a, b"}},
				{json.Number("8"), "alice@example.com", nil},
			}},
	}
	var buf bytes.Buffer
	if err := WriteZIP(&buf, tables, "hello readme"); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = b
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"README.txt", "audit_log.csv", "audit_log.json", "users.csv", "users.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("files = %v, want %v", names, want)
	}
	if string(files["README.txt"]) != "hello readme" {
		t.Errorf("README = %q", files["README.txt"])
	}

	recs, err := csv.NewReader(bytes.NewReader(files["audit_log.csv"])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recs[0], tables[1].Columns) {
		t.Errorf("header = %v", recs[0])
	}
	if len(recs) != 3 {
		t.Fatalf("csv rows = %d, want 3", len(recs))
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(recs[1][2]), &back); err != nil {
		t.Fatalf("JSON cell did not survive CSV: %q: %v", recs[1][2], err)
	}
	if back["note"] != "a, b" || back["role"] != "admin" {
		t.Errorf("JSON cell = %v", back)
	}
	if recs[1][0] != "7" || recs[2][2] != "" {
		t.Errorf("number/null cells = %q %q", recs[1][0], recs[2][2])
	}

	urecs, err := csv.NewReader(bytes.NewReader(files["users.csv"])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urecs[0], tables[0].Columns) {
		t.Errorf("users header = %v", urecs[0])
	}

	// JSON: an array of objects keyed by column, in column order.
	var objs []map[string]any
	if err := json.Unmarshal(files["audit_log.json"], &objs); err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0]["actor"] != "alice@example.com" || objs[1]["after"] != nil {
		t.Errorf("audit_log.json = %v", objs)
	}
	if after, _ := objs[0]["after"].(map[string]any); after["note"] != "a, b" {
		t.Errorf("nested JSON = %v", objs[0]["after"])
	}
	if i, j := strings.Index(string(files["audit_log.json"]), `"id"`), strings.Index(string(files["audit_log.json"]), `"actor"`); i < 0 || j < i {
		t.Errorf("JSON keys not in column order: %s", files["audit_log.json"])
	}
}

// Spreadsheet formulas are neutralised in the CSV only; the JSON stays exact.
func TestWriteZIPEscapesFormulas(t *testing.T) {
	vals := []string{`=HYPERLINK("http://x","y")`, "+1", "-2", "@SUM(A1)", "\tx", "\rx", "plain", "a=b"}
	row := make([]any, len(vals))
	cols := make([]string, len(vals))
	for i, v := range vals {
		row[i] = v
		cols[i] = fmt.Sprint("c", i)
	}
	var buf bytes.Buffer
	if err := WriteZIP(&buf, []Table{{Name: "t", Columns: cols, Rows: [][]any{row}}}, ""); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		f, err := zr.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	recs, err := csv.NewReader(bytes.NewReader(read("t.csv"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		want := v
		if i < 6 {
			want = "'" + v
		}
		// encoding/csv turns a lone \r inside a quoted field into \n on read.
		got := strings.ReplaceAll(recs[1][i], "\n", "\r")
		if got != want {
			t.Errorf("csv cell %d = %q, want %q", i, recs[1][i], want)
		}
	}
	var objs []map[string]any
	if err := json.Unmarshal(read("t.json"), &objs); err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if objs[0][cols[i]] != v {
			t.Errorf("json %s = %q, want exact %q", cols[i], objs[0][cols[i]], v)
		}
	}
}

// collectTable takes the scope from the first entry, so it refuses a table
// whose entries disagree, before touching the database (tx is nil here).
func TestCollectTableScopeDisagreement(t *testing.T) {
	s := Subject{CustomerID: "c", Email: "a@x.io"}
	for _, entries := range [][]Entry{
		{{Table: "x", Column: "a", Key: KeyEmail, Scoped: true}, {Table: "x", Column: "b", Key: KeyEmail}},
		{{Table: "x", Column: "a", Key: KeyEmail, Scoped: true}, {Table: "x", Column: "b", Key: KeyEmail, Scoped: true, ScopeColumn: "id"}},
	} {
		if _, err := collectTable(context.Background(), nil, s, nil, "x", entries); err == nil || !strings.Contains(err.Error(), "disagree on scope") {
			t.Errorf("entries %v: err = %v, want scope disagreement", entries, err)
		}
	}
}

func TestAccessREADME(t *testing.T) {
	r := AccessREADME(Subject{CustomerID: "c", Email: "alice@example.com"}, []Table{
		{Name: "audit_log"}, {Name: "user_sessions"},
	})
	for _, want := range []string{
		"audit_log", "activity", "audit log (24 months)", "actor, target_id, before, after, metadata",
		"user_sessions", "network",
		"application logs (30 days)", "backups (35 days)",
		"prefixed with '", "exact values",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("README lacks %q:\n%s", want, r)
		}
	}
}
