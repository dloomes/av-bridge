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

	// Scoped must equal "table has its scope column"; KeyColumn and ScopeColumn must exist.
	for _, e := range Personal {
		sc := e.ScopeColumn
		if sc == "" {
			sc = "customer_id"
		}
		if has := cols[e.Table+"."+sc]; e.Scoped != has {
			t.Errorf("%s.%s: Scoped=%v but table has %s=%v", e.Table, e.Column, e.Scoped, sc, has)
		}
		if e.ScopeColumn != "" && !e.Scoped {
			t.Errorf("%s.%s: ScopeColumn set but Scoped is false", e.Table, e.Column)
		}
		if e.KeyColumn != "" && !cols[e.Table+"."+e.KeyColumn] {
			t.Errorf("%s.%s: KeyColumn %q does not exist", e.Table, e.Column, e.KeyColumn)
		}
		// An unscoped table found by email text can't be confined to the
		// subject's customer, so a customer's request would reach every row
		// with that email: it must be Involve's own (VendorOnly), which Collect
		// and Erase skip for customers. Unscoped user-id tables are confined
		// by the account ids instead.
		if !e.Scoped && (e.Key == KeyEmail || e.Key == KeyEmailInText || e.Key == KeyEmailJSON) && !e.VendorOnly {
			t.Errorf("%s.%s: unscoped email-keyed entry must be VendorOnly", e.Table, e.Column)
		}
		if e.VendorOnly && e.Scoped {
			t.Errorf("%s.%s: VendorOnly on a Scoped table", e.Table, e.Column)
		}
	}

	// Processing order: an entry found through key column K precedes any entry
	// in the same table that changes K; users entries come last.
	for i, e := range Personal {
		if e.KeyColumn != "" {
			for _, later := range Personal[:i] {
				if later.Table == e.Table && later.Column == e.KeyColumn {
					t.Errorf("%s.%s is found through %s, which is changed earlier in the register", e.Table, e.Column, e.KeyColumn)
				}
			}
		}
		if i > 0 && e.Table != "users" && Personal[i-1].Table == "users" {
			t.Errorf("%s.%s comes after a users entry; users must be last", e.Table, e.Column)
		}
	}
}
