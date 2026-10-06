package publicapi

// Integration test for HMAC secret rotation on enrolment. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/publicapi/ -run TestEnrollRotatesSecret -v

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

func TestEnrollRotatesSecret(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping enrolment integration test")
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
	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}

	original := "original-secret"
	enc, _ := cipher.Encrypt([]byte(original))
	var cust, col string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('enroll-test') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	                            VALUES ($1::uuid, 'enroll-col-' || $1::uuid::text, 'enroll', $2) RETURNING id::text`, cust, enc).Scan(&col); err != nil {
		t.Fatal(err)
	}
	token := func(raw string) string {
		if _, err := su.Exec(ctx, `INSERT INTO collector_enrollment_tokens (collector_id, token_hash, expires_at)
		                           VALUES ($1, $2, now() + interval '1 hour')`, col, portalauth.HashToken(raw)); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	h := NewHandler(st, quiet, notify.SMTPConfig{}, "", false, cipher, "https://cloud.example.test")
	enroll := func(tok string) (int, enrollResponse) {
		b, _ := json.Marshal(enrollRequest{Token: tok, Hostname: "box"})
		rec := httptest.NewRecorder()
		h.EnrollCollector(rec, httptest.NewRequest("POST", "/public/collectors/enroll", bytes.NewReader(b)))
		var out enrollResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	storedSecret := func() string {
		var e []byte
		if err := su.QueryRow(ctx, `SELECT hmac_secret_enc FROM collectors WHERE id = $1`, col).Scan(&e); err != nil {
			t.Fatal(err)
		}
		p, err := cipher.Decrypt(e)
		if err != nil {
			t.Fatal(err)
		}
		return string(p)
	}

	tok1 := token(randToken(t))
	code, first := enroll(tok1)
	if code != http.StatusOK {
		t.Fatalf("first enrol: %d", code)
	}
	if first.HMACSecret == original || first.HMACSecret != storedSecret() {
		t.Error("enrolment should mint a new secret and store it")
	}
	if first.CollectorID != col || first.CloudBaseURL != "https://cloud.example.test" {
		t.Errorf("unexpected response %+v", first)
	}

	// Token is single-use; a reused token changes nothing.
	if code, _ := enroll(tok1); code != http.StatusBadRequest {
		t.Errorf("reused token: want 400, got %d", code)
	}
	if storedSecret() != first.HMACSecret {
		t.Error("a failed redeem must not rotate the secret")
	}

	// Re-enrolling (replacement hardware) rotates again, cutting off the
	// first machine's secret.
	_, second := enroll(token(randToken(t)))
	if second.HMACSecret == first.HMACSecret || storedSecret() != second.HMACSecret {
		t.Error("re-enrolment should rotate the secret again")
	}
}

func randToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
