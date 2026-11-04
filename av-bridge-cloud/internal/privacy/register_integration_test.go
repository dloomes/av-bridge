package privacy

import (
	"sort"
	"strings"
	"testing"
)

func TestRegisterCoversSchema(t *testing.T) {
	ctx, su, _, _ := setup(t)

	rows, err := su.Query(ctx, `SELECT c.table_name, c.column_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE' AND c.table_name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var tb, col string
		if err := rows.Scan(&tb, &col); err != nil {
			t.Fatal(err)
		}
		cols[tb+"."+col] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	for _, e := range Personal {
		known[e.Table+"."+e.Column] = true
		if !cols[e.Table+"."+e.Column] {
			t.Errorf("Personal entry %s.%s does not exist in the schema", e.Table, e.Column)
		}
		if e.Action == ActReport && e.Reason == "" {
			t.Errorf("%s.%s: ActReport needs a Reason", e.Table, e.Column)
		}
		if e.Action == ActStripJSON && e.Table != "audit_log" && len(e.JSONKeys) == 0 {
			t.Errorf("%s.%s: ActStripJSON needs JSONKeys", e.Table, e.Column)
		}
	}
	for _, e := range NotPersonal {
		if known[e.Table+"."+e.Column] {
			t.Errorf("%s.%s is in both Personal and NotPersonal", e.Table, e.Column)
		}
		known[e.Table+"."+e.Column] = true
		if e.Reason == "" {
			t.Errorf("NotPersonal %s.%s has no reason", e.Table, e.Column)
		}
		if !cols[e.Table+"."+e.Column] {
			t.Errorf("NotPersonal entry %s.%s does not exist in the schema", e.Table, e.Column)
		}
	}

	var missing []string
	for k := range cols {
		col := k[strings.Index(k, ".")+1:]
		if GuardPattern.MatchString(col) && !known[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d columns match the guard pattern but are not registered:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}

	// Scoped must equal "table has a customer_id column".
	for _, e := range Personal {
		has := cols[e.Table+".customer_id"]
		if e.Scoped != has {
			t.Errorf("%s.%s: Scoped=%v but table has customer_id=%v", e.Table, e.Column, e.Scoped, has)
		}
	}
}
