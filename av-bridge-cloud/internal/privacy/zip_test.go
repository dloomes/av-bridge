package privacy

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
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

func TestAccessREADME(t *testing.T) {
	r := AccessREADME(Subject{CustomerID: "c", Email: "alice@example.com"}, []Table{
		{Name: "audit_log"}, {Name: "user_sessions"},
	})
	for _, want := range []string{
		"audit_log", "activity", "audit log (24 months)",
		"user_sessions", "network",
		"application logs (30 days)", "backups (35 days)",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("README lacks %q:\n%s", want, r)
		}
	}
}
