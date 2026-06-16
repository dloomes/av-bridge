package db

import (
	"testing"
)

// TestLoadEmbedded — the embedded migrations parse cleanly, sort by version,
// and have no duplicate versions. Cheap guardrail so a typo in a filename
// (e.g. 0003a_foo.sql, 0003_bar.sql) is caught at unit-test time, not at
// "this dev box won't boot".
func TestLoadEmbedded(t *testing.T) {
	m, err := loadEmbedded()
	if err != nil {
		t.Fatalf("loadEmbedded: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("no embedded migrations found")
	}
	for i := 1; i < len(m); i++ {
		if m[i].Version <= m[i-1].Version {
			t.Fatalf("migrations not strictly ascending: %s (%d) then %s (%d)",
				m[i-1].Name, m[i-1].Version, m[i].Name, m[i].Version)
		}
	}
	// 0001..0004 should all be present in this slice's shipping set.
	want := map[int]bool{1: false, 2: false, 3: false, 4: false}
	for _, mm := range m {
		if _, ok := want[mm.Version]; ok {
			want[mm.Version] = true
		}
	}
	for v, found := range want {
		if !found {
			t.Errorf("missing migration version %d", v)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := map[string]int{
		"0001_schema.sql":           1,
		"0042_add_some_column.sql":  42,
		"0100_big_change.sql":       100,
	}
	for name, want := range cases {
		got, err := parseVersion(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %d, want %d", name, got, want)
		}
	}
}

func TestParseVersion_Bad(t *testing.T) {
	for _, bad := range []string{"schema.sql", "abc_schema.sql", "schema_0001.sql"} {
		if _, err := parseVersion(bad); err == nil {
			t.Errorf("%s: expected error, got nil", bad)
		}
	}
}
