package bridgecfg_test

// End-to-end check of the config resync signal (migration 0049): a device
// change wakes the collector's held /bridge/poll, which answers
// resync=true until the bridge pulls /bridge/config again. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/bridgecfg/ -run TestConfigResync -v

import (
	"bytes"
	"context"
	"crypto/hmac"
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
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/bridgecfg"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

func TestConfigResync(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping config resync integration test")
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
	cipher, err := secrets.NewAESGCMFromHexKey(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}

	const secret = "resync-test-secret"
	enc, _ := cipher.Encrypt([]byte(secret))
	var cust, bridgeID, dev string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('resync-test') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	                            VALUES ($1::uuid, 'resync-' || $1::uuid::text, 'resync', $2)
	                            RETURNING bridge_collector_id`, cust, enc).Scan(&bridgeID); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO devices (customer_id, collector_id, reported_id, protocol, address)
	                            SELECT $1, id, 'disp-1', 'ping', '10.0.0.1' FROM collectors WHERE bridge_collector_id = $2
	                            RETURNING id::text`, cust, bridgeID).Scan(&dev); err != nil {
		t.Fatal(err)
	}

	cfg := bridgecfg.NewHandler(st, cipher, quiet)
	bridge := commands.NewBridgeHandler(st, cipher, 3*time.Second, quiet)
	signed := func(path string) *http.Request {
		body, _ := json.Marshal(map[string]any{"collector_id": bridgeID})
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return req
	}
	pull := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		cfg.Get(rec, signed("/bridge/config"))
		if rec.Code != http.StatusOK {
			t.Fatalf("config pull: %d %s", rec.Code, rec.Body.String())
		}
	}
	poll := func() (resync bool, took time.Duration) {
		t.Helper()
		start := time.Now()
		rec := httptest.NewRecorder()
		bridge.Poll(rec, signed("/bridge/poll"))
		var out struct {
			Resync bool `json:"resync"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Resync, time.Since(start)
	}

	// Freshly pulled: a poll holds for the full window and says no resync.
	pull()
	if resync, took := poll(); resync || took < 2*time.Second {
		t.Fatalf("after a pull: resync=%v took=%v, want false after a full hold", resync, took)
	}

	// A device edit while the poll is held wakes it early with resync.
	go func() {
		time.Sleep(500 * time.Millisecond)
		_, _ = su.Exec(ctx, `UPDATE devices SET address = '10.0.0.2' WHERE id = $1`, dev)
	}()
	if resync, took := poll(); !resync || took > 2*time.Second {
		t.Fatalf("after a device edit: resync=%v took=%v, want true within ~0.5s", resync, took)
	}

	// Pulling clears it.
	pull()
	if resync, _ := poll(); resync {
		t.Fatal("resync should clear once the bridge has pulled")
	}
}
