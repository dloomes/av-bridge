package collectorupdate_test

// Integration test for collector self-update on the cloud side: poll
// metadata, Update requests, offers on /bridge/poll, bridge reports, the
// maintenance window and the timeout sweep. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/collectorupdate/ -run TestCollectorUpdateFlow -v

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/collectorupdate"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

func TestCollectorUpdateFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping collector update integration test")
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
	cipher, _ := secrets.NewAESGCMFromHexKey(strings.Repeat("ef", 32))

	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	const secret = "update-test-secret"
	enc, _ := cipher.Encrypt([]byte(secret))
	cust := q(`INSERT INTO customers (name) VALUES ('update-test') RETURNING id::text`)
	newCollector := func(name string) (id, bridgeID string) {
		bridgeID = name + "-" + cust
		id = q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, bridge_version)
		        VALUES ($1, $2, $3, $4, 'v1.0.19') RETURNING id::text`, cust, bridgeID, name, enc)
		return id, bridgeID
	}
	colA, bridgeA := newCollector("upd-a")
	colB, bridgeB := newCollector("upd-b")

	release := &collectorupdate.Release{Version: "v1.0.20", Files: map[string]collectorupdate.File{
		"av-bridge-linux-amd64": {SHA256: "abc123", Signature: "c2ln"},
	}}
	svc := collectorupdate.NewService(st, release, quiet)
	bridge := commands.NewBridgeHandler(st, cipher, 1*time.Second, quiet)
	bridge.SetUpdates(svc)

	type pollOut struct {
		Update *collectorupdate.Instruction `json:"update"`
	}
	poll := func(bridgeID, version string, capable bool) pollOut {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"collector_id": bridgeID, "version": version, "platform": "linux/amd64",
			"update_capable": capable, "update_blocker": map[bool]string{true: "", false: "runs in a container; update the container image instead"}[capable],
		})
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req := httptest.NewRequest("POST", "/bridge/poll", bytes.NewReader(body))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		bridge.Poll(rec, req)
		if rec.Code != 200 {
			t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
		}
		var out pollOut
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	report := func(bridgeID, state, version, msg string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"collector_id": bridgeID, "state": state, "version": version, "message": msg})
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req := httptest.NewRequest("POST", "/bridge/update-status", bytes.NewReader(body))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		bridge.PostUpdateStatus(rec, req)
		return rec.Code
	}
	stateOf := func(id string) string {
		return q(`SELECT update_state || ':' || COALESCE(update_message, '') FROM collectors WHERE id = $1`, id)
	}
	request := func(id string) error {
		t.Helper()
		return st.WithTenant(ctx, cust, func(tx pgx.Tx) error { return svc.Request(ctx, tx, id) })
	}

	// 1. No request, no window: nothing offered; capability recorded.
	if out := poll(bridgeA, "v1.0.19", true); out.Update != nil {
		t.Fatal("no update should be offered without a request or window")
	}
	if got := q(`SELECT bridge_platform || ':' || update_capable::text FROM collectors WHERE id = $1`, colA); got != "linux/amd64:true" {
		t.Errorf("capability not recorded: %s", got)
	}

	// 2. A blocked collector can't be requested.
	poll(bridgeB, "v1.0.19", false)
	var ne collectorupdate.ErrNotEligible
	if err := request(colB); !errors.As(err, &ne) || !strings.Contains(ne.Reason, "container") {
		t.Errorf("blocked collector: want ErrNotEligible(container), got %v", err)
	}

	// 3. Update pressed: a held poll wakes and carries the offer.
	if err := request(colA); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := request(colA); !errors.Is(err, collectorupdate.ErrInFlight) {
		t.Errorf("second request: want ErrInFlight, got %v", err)
	}
	out := poll(bridgeA, "v1.0.19", true)
	if out.Update == nil || out.Update.Version != "v1.0.20" || out.Update.URL != "/public/downloads/av-bridge-linux-amd64" ||
		out.Update.SHA256 != "abc123" || out.Update.Signature != "c2ln" {
		t.Fatalf("want an offer for v1.0.20, got %+v", out.Update)
	}
	if s := stateOf(colA); s != "in_progress:" {
		t.Errorf("after offer: %s", s)
	}
	if out := poll(bridgeA, "v1.0.19", true); out.Update != nil {
		t.Error("an in-progress update must not be offered twice")
	}

	// 4. Reports: stale version ignored; restarting keeps it in progress;
	//    coming back on the new version completes it.
	if code := report(bridgeA, "failed", "v0.0.1", "stale"); code != http.StatusNoContent {
		t.Errorf("report: %d", code)
	}
	if s := stateOf(colA); s != "in_progress:" {
		t.Errorf("stale report changed state: %s", s)
	}
	report(bridgeA, "restarting", "v1.0.20", "")
	poll(bridgeA, "v1.0.20", true)
	if s := stateOf(colA); s != "succeeded:" {
		t.Errorf("collector back on new version: %s, want succeeded", s)
	}
	if err := request(colA); !errors.As(err, &ne) || ne.Reason != "up to date" {
		t.Errorf("updated collector: want 'up to date', got %v", err)
	}

	// 5. Schedule: window open now → offered once; a failed attempt at
	//    this version isn't retried by the schedule.
	exec(`UPDATE collectors SET bridge_version = 'v1.0.19', update_state = 'idle',
	      update_window_start = (EXTRACT(HOUR FROM now() AT TIME ZONE 'Europe/London')::int * 60) WHERE id = $1`, colA)
	if out := poll(bridgeA, "v1.0.19", true); out.Update == nil {
		t.Fatal("open window: want a scheduled offer")
	}
	report(bridgeA, "rolled_back", "v1.0.20", "v1.0.20 couldn't reach the cloud within 5m0s")
	if s := stateOf(colA); !strings.HasPrefix(s, "rolled_back:") {
		t.Errorf("after rollback report: %s", s)
	}
	if out := poll(bridgeA, "v1.0.19", true); out.Update != nil {
		t.Error("schedule must not retry a version that rolled back")
	}
	// Window closed → nothing.
	exec(`UPDATE collectors SET update_state = 'idle',
	      update_window_start = ((EXTRACT(HOUR FROM now() AT TIME ZONE 'Europe/London')::int + 12) % 24) * 60 WHERE id = $1`, colA)
	if out := poll(bridgeA, "v1.0.19", true); out.Update != nil {
		t.Error("closed window: nothing should be offered")
	}

	// 6. Timeouts.
	exec(`UPDATE collectors SET update_state = 'in_progress', update_target_version = 'v1.0.20',
	      update_state_at = now() - interval '20 minutes' WHERE id = $1`, colA)
	exec(`UPDATE collectors SET update_state = 'requested', update_target_version = 'v1.0.20',
	      update_state_at = now() - interval '25 hours' WHERE id = $1`, colB)
	svc.Sweep(ctx)
	if s := stateOf(colA); !strings.HasPrefix(s, "failed:No result") {
		t.Errorf("quiet in-progress: %s", s)
	}
	if s := stateOf(colB); !strings.HasPrefix(s, "failed:The collector didn't come online") {
		t.Errorf("stale request: %s", s)
	}

	// 7. A request that became ineligible before delivery fails with
	//    the reason instead of hanging.
	exec(`UPDATE collectors SET update_state = 'requested', update_target_version = 'v1.0.20', update_state_at = now() WHERE id = $1`, colB)
	poll(bridgeB, "v1.0.19", false)
	if s := stateOf(colB); !strings.HasPrefix(s, "failed:runs in a container") {
		t.Errorf("ineligible requested collector: %s", s)
	}
}
