package portalapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestAccessExportHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := su.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}

	cust := q(`INSERT INTO customers (name, entra_tenant_id) VALUES ('export-test', gen_random_uuid()) RETURNING id::text`)
	adminID := q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, 'export.admin@example.test', 'x') RETURNING id::text`, cust)
	subject := "Subject.Person@Example.test"
	q(`INSERT INTO users (customer_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`, cust, subject)

	adminP := portalauth.Principal{UserID: adminID, Email: "export.admin@example.test", CustomerID: cust,
		Permissions: map[string]struct{}{portalauth.PermPrivacyManage: {}}}
	viewerP := portalauth.Principal{UserID: adminID, CustomerID: cust,
		Permissions: map[string]struct{}{"view.users": {}}}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.Handle("POST /export", portalauth.RequirePermission(portalauth.PermPrivacyManage)(http.HandlerFunc(h.AccessExport)))
	call := func(p portalauth.Principal, email string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest("POST", "/export", bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := call(adminP, subject)
	if rec.Code != 200 {
		t.Fatalf("admin export: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="access-request-`) || !strings.HasSuffix(cd, `.zip"`) {
		t.Errorf("content-disposition %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control %q", rec.Header().Get("Cache-Control"))
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("zip: %v (%d files)", err, len(zr.File))
	}

	if rec := call(viewerP, subject); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", rec.Code)
	}
	if rec := call(adminP, "nobody.here@example.test"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown email: %d %s, want 404", rec.Code, rec.Body.String())
	}

	sum := sha256.Sum256([]byte(strings.ToLower(subject)))
	var n int
	var row string
	var meta string
	err = su.QueryRow(ctx, `SELECT count(*) OVER (), a::text, metadata->>'email_sha256' FROM audit_log a
		WHERE customer_id = $1 AND action = 'privacy.access_export'`, cust).Scan(&n, &row, &meta)
	if err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if meta != hex.EncodeToString(sum[:]) {
		t.Errorf("email_sha256 = %q", meta)
	}
	if strings.Contains(row, "@") {
		t.Errorf("audit row contains an email: %s", row)
	}
}
