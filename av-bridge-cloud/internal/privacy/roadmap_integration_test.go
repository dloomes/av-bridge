package privacy

// Integration tests: the global roadmap rows (VendorOnly entries) are Involve's,
// so a customer's data request never reaches them; vendor staff's does.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
)

// seedRoadmap takes the roadmap test lock for the rest of the test and makes
// every VendorOnly column hold email: roadmap_settings.updated_by (restored at
// cleanup), and a new theme, item and share link (deleted at cleanup). by
// returns the three values as they are now.
func seedRoadmap(t *testing.T, ctx context.Context, su *pgx.Conn, email, tag string) (by func() [3]string) {
	t.Helper()
	unlock, err := roadmap.TestLock(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	var oldBy *string
	if err := su.QueryRow(ctx, `SELECT updated_by FROM roadmap_settings`).Scan(&oldBy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { su.Exec(ctx, `UPDATE roadmap_settings SET updated_by = $1`, oldBy) })
	if _, err := su.Exec(ctx, `UPDATE roadmap_settings SET updated_by = $1`, strings.ToUpper(email[:1])+email[1:]); err != nil {
		t.Fatal(err)
	}
	var themeID, itemID, linkID string
	if err := su.QueryRow(ctx, `INSERT INTO roadmap_themes (position, name) VALUES (99, 'Privacy test') RETURNING id::text`).Scan(&themeID); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO roadmap_items (theme_id, period_position, status, title, description, updated_by)
		VALUES ($1, 0, 'planned', 'Privacy test item', 'Seeded by a privacy test', $2) RETURNING id::text`, themeID, email).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO roadmap_share_links (name, token_hash, expires_at, created_by)
		VALUES ('Prospect Co', $1, now() + interval '1 day', $2) RETURNING id::text`, "prl-"+tag, strings.ToUpper(email)).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		su.Exec(ctx, `DELETE FROM roadmap_share_links WHERE id = $1`, linkID)
		su.Exec(ctx, `DELETE FROM roadmap_items WHERE id = $1`, itemID)
		su.Exec(ctx, `DELETE FROM roadmap_themes WHERE id = $1`, themeID)
	})
	return func() [3]string {
		t.Helper()
		var v [3]string
		if err := su.QueryRow(ctx, `SELECT
			(SELECT coalesce(updated_by, '') FROM roadmap_settings),
			(SELECT coalesce(updated_by, '') FROM roadmap_items WHERE id = $1),
			(SELECT created_by FROM roadmap_share_links WHERE id = $2)`, itemID, linkID).Scan(&v[0], &v[1], &v[2]); err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func roadmapTables(got map[string]Table) []string {
	var out []string
	for name := range got {
		if strings.HasPrefix(name, "roadmap_") {
			out = append(out, name)
		}
	}
	return out
}

// A customer's access export never holds roadmap rows: not for an email with
// no account there (an Involve editor's), nor for the customer's own user
// whose email matches the roadmap's editor.
func TestCollectCustomerSkipsRoadmap(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-collect-roadmap")
	editor := "editor-" + c[:8] + "@involve.example"
	user := "user-" + c[:8] + "@example.com"
	if _, err := su.Exec(ctx, `INSERT INTO users (customer_id, email, password_hash) VALUES ($1, $2, 'h')`, c, user); err != nil {
		t.Fatal(err)
	}
	seedRoadmap(t, ctx, su, editor, c+"-editor")
	for _, email := range []string{editor, user} {
		if email == user {
			// The customer's user now also holds the roadmap's editor email.
			if _, err := su.Exec(ctx, `UPDATE roadmap_share_links SET created_by = $1 WHERE token_hash = $2`, user, "prl-"+c+"-editor"); err != nil {
				t.Fatal(err)
			}
			if _, err := su.Exec(ctx, `UPDATE roadmap_settings SET updated_by = $1`, user); err != nil {
				t.Fatal(err)
			}
		}
		s, got := collectFor(t, ctx, st.AdminPool(), c, "", email)
		if email == user && s.UserID == "" {
			t.Fatalf("subject %s has no account", email)
		}
		if names := roadmapTables(got); len(names) > 0 {
			t.Errorf("customer export for %s holds roadmap tables %v", email, names)
		}
	}
}

// Vendor staff's access export does hold the roadmap rows they wrote.
func TestCollectVendorStaffRoadmap(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-collect-roadmap-vendor")
	var vt string
	if err := su.QueryRow(ctx, `INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id::text`, "vtr-"+c).Scan(&vt); err != nil {
		t.Fatal(err)
	}
	email := "editor-" + c[:8] + "@involve.example"
	if _, err := su.Exec(ctx, `INSERT INTO users (vendor_tenant_id, email, password_hash, roadmap_editor) VALUES ($1, $2, 'h', true)`, vt, email); err != nil {
		t.Fatal(err)
	}
	seedRoadmap(t, ctx, su, email, c)
	_, got := collectFor(t, ctx, st.AdminPool(), "", vt, email)
	for _, name := range []string{"roadmap_settings", "roadmap_items", "roadmap_share_links"} {
		if n := len(got[name].Rows); n != 1 {
			t.Errorf("%s: %d rows, want 1", name, n)
		}
	}
}

// Vendor staff's erase rewrites the roadmap rows they wrote.
func TestEraseVendorStaffRoadmap(t *testing.T) {
	ctx, su, st, mk := setup(t)
	c := mk("privacy-erase-roadmap-vendor")
	var vt string
	if err := su.QueryRow(ctx, `INSERT INTO vendor_tenants (name, entra_tenant_id) VALUES ('Involve', $1) RETURNING id::text`, "vte-r-"+c).Scan(&vt); err != nil {
		t.Fatal(err)
	}
	email := "editor-" + c[:8] + "@involve.example"
	if _, err := su.Exec(ctx, `INSERT INTO users (vendor_tenant_id, email, password_hash, roadmap_editor) VALUES ($1, $2, 'h', true)`, vt, email); err != nil {
		t.Fatal(err)
	}
	by := seedRoadmap(t, ctx, su, email, c)

	_, dry := eraseIn(t, ctx, st.AdminPool(), "", vt, email, true)
	_, res := eraseIn(t, ctx, st.AdminPool(), "", vt, email, false)
	for _, k := range []string{"roadmap_settings.updated_by", "roadmap_items.updated_by", "roadmap_share_links.created_by"} {
		if dry.Counts[k] != 1 || res.Counts[k] != 1 {
			t.Errorf("count %s = %d (dry run %d), want 1", k, res.Counts[k], dry.Counts[k])
		}
	}
	if got := by(); got != [3]string{res.StandIn, res.StandIn, res.StandIn} {
		t.Errorf("roadmap by = %v, want all %q", got, res.StandIn)
	}
}
