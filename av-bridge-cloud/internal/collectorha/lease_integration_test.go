package collectorha_test

// Integration test for warm-standby collector groups: lease, failover,
// handover, group-aware poll / config / ingest / results, device status,
// alerts and portal guards. Runs only when TEST_DATABASE_URL points at a
// disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/collectorha/ -run TestCollectorGroup -v

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
	"github.com/dloomes/av-bridge-cloud/internal/collectorha"
	"github.com/dloomes/av-bridge-cloud/internal/collectorhealth"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/devicestatus"
	"github.com/dloomes/av-bridge-cloud/internal/ingest"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

func TestCollectorGroup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping collector group integration test")
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
	cipher, _ := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))

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

	const secret = "group-secret"
	enc, _ := cipher.Encrypt([]byte(secret))
	cust := q(`INSERT INTO customers (name) VALUES ('ha-test') RETURNING id::text`)
	machine := func(name, standbyFor string) (id, bridgeID string) {
		bridgeID = name + "-" + cust
		var sf any
		if standbyFor != "" {
			sf = standbyFor
		}
		id = q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, standby_for, last_seen_at)
		        VALUES ($1, $2, $3, $4, $5, now()) RETURNING id::text`, cust, bridgeID, name, enc, sf)
		return id, bridgeID
	}
	primary, primaryBID := machine("ha-primary", "")
	standby, standbyBID := machine("ha-standby", primary)
	single, singleBID := machine("ha-single", "")
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, protocol, address, latest_status, last_seen_at)
	          VALUES ($1, $2, 'disp-1', 'ping', '10.0.0.1', 'online', now()) RETURNING id::text`, cust, primary)

	mgr := collectorha.NewManager(st, quiet)
	bridge := commands.NewBridgeHandler(st, cipher, 500*time.Millisecond, quiet)
	bridge.SetHA(mgr)
	cfg := bridgecfg.NewHandler(st, cipher, quiet)
	cfg.SetHA(mgr)
	ing := ingest.NewHandler(st, cipher, nil, nil, quiet)

	signed := func(path string, body map[string]any) *http.Request {
		b, _ := json.Marshal(body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return req
	}
	type pollOut struct {
		Commands []struct {
			ID string `json:"id"`
		} `json:"commands"`
		Role string `json:"role"`
		TTL  int    `json:"lease_ttl_seconds"`
	}
	poll := func(bridgeID string) pollOut {
		t.Helper()
		rec := httptest.NewRecorder()
		bridge.Poll(rec, signed("/bridge/poll", map[string]any{"collector_id": bridgeID}))
		if rec.Code != 200 {
			t.Fatalf("poll %s: %d %s", bridgeID, rec.Code, rec.Body.String())
		}
		var out pollOut
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	type cfgOut struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
		HA bool `json:"ha"`
	}
	pull := func(bridgeID string) cfgOut {
		t.Helper()
		rec := httptest.NewRecorder()
		cfg.Get(rec, signed("/bridge/config", map[string]any{"collector_id": bridgeID}))
		if rec.Code != 200 {
			t.Fatalf("config %s: %d %s", bridgeID, rec.Code, rec.Body.String())
		}
		var out cfgOut
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	effective := func() string {
		return q(`SELECT `+devicestatus.EffectiveStatusSQL+` FROM devices d
		          LEFT JOIN collectors c ON c.id = d.collector_id WHERE d.id = $1`, dev)
	}

	// 1. A collector without standbys is unaffected: no role.
	if out := poll(singleBID); out.Role != "" || out.TTL != 0 {
		t.Errorf("single collector: role %q ttl %d, want none", out.Role, out.TTL)
	}
	_ = single

	// 2. First poll from the primary takes the lease; the standby stands by.
	if out := poll(primaryBID); out.Role != "active" || out.TTL != 60 {
		t.Fatalf("primary: role %q ttl %d, want active/60", out.Role, out.TTL)
	}
	if out := poll(standbyBID); out.Role != "standby" {
		t.Fatalf("standby: role %q, want standby", out.Role)
	}

	// 3. Config: devices only to the holder; both told they're grouped.
	if c := pull(primaryBID); len(c.Devices) != 1 || !c.HA {
		t.Errorf("primary config: %+v", c)
	}
	if c := pull(standbyBID); len(c.Devices) != 0 || !c.HA {
		t.Errorf("standby config should be empty: %+v", c)
	}
	rec := httptest.NewRecorder()
	cfg.Put(rec, signed("/bridge/config", map[string]any{"collector_id": standbyBID, "devices": []any{}}))
	if rec.Code != http.StatusConflict {
		t.Errorf("standby seed: want 409, got %d", rec.Code)
	}

	// 4. Commands go only to the holder.
	cmd1 := q(`INSERT INTO commands (customer_id, collector_id, device_id, name) VALUES ($1, $2, $3, 'power_on') RETURNING id::text`, cust, primary, dev)
	if out := poll(standbyBID); len(out.Commands) != 0 {
		t.Error("standby must not claim commands")
	}
	if out := poll(primaryBID); len(out.Commands) != 1 || out.Commands[0].ID != cmd1 {
		t.Errorf("primary should claim the command, got %+v", out.Commands)
	}

	// 5. Failover: the primary goes quiet past its lease.
	versionBefore := q(`SELECT config_version::text FROM collectors WHERE id = $1`, primary)
	exec(`UPDATE collectors SET lease_expires_at = now() - interval '1 second',
	      last_seen_at = now() - interval '10 minutes' WHERE id = $1`, primary)
	if out := poll(standbyBID); out.Role != "active" {
		t.Fatalf("after lease expiry the standby should take over, got %q", out.Role)
	}
	if v := q(`SELECT config_version::text FROM collectors WHERE id = $1`, primary); v == versionBefore {
		t.Error("failover must bump the group config so every machine re-pulls")
	}
	if n := q(`SELECT count(*)::text FROM audit_log WHERE action = 'collector.failover' AND target_id = $1`, primary); n != "1" {
		t.Errorf("want one collector.failover audit row, got %s", n)
	}
	if c := pull(standbyBID); len(c.Devices) != 1 {
		t.Errorf("new holder should get the devices: %+v", c)
	}
	if s := effective(); s != "online" {
		t.Errorf("device status while the standby serves and the primary is down: %s, want online", s)
	}

	// 6. The standby serves: claims, completes under the group, ingests
	//    onto the primary's device (no duplicate on the standby).
	cmd2 := q(`INSERT INTO commands (customer_id, collector_id, device_id, name) VALUES ($1, $2, $3, 'mute') RETURNING id::text`, cust, primary, dev)
	if out := poll(standbyBID); len(out.Commands) != 1 || out.Commands[0].ID != cmd2 {
		t.Fatalf("active standby should claim the group's command, got %+v", out.Commands)
	}
	rec = httptest.NewRecorder()
	req := signed("/bridge/commands/"+cmd2+"/result", map[string]any{"collector_id": standbyBID, "result": map[string]any{"raw": "ok"}})
	req.SetPathValue("id", cmd2)
	bridge.PostResult(rec, req)
	if s := q(`SELECT status FROM commands WHERE id = $1`, cmd2); s != "succeeded" {
		t.Errorf("standby's result for a group command: status %s (HTTP %d %s)", s, rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ing.ServeHTTP(rec, signed("/ingest", map[string]any{
		"collector_id": standbyBID,
		"telemetry": []map[string]any{{"device_id": "disp-1", "status": "offline",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano)}},
	}))
	if rec.Code != 200 {
		t.Fatalf("ingest from standby: %d %s", rec.Code, rec.Body.String())
	}
	if s := q(`SELECT latest_status FROM devices WHERE id = $1`, dev); s != "offline" {
		t.Errorf("standby telemetry should land on the primary's device, latest_status=%s", s)
	}
	if n := q(`SELECT count(*)::text FROM devices WHERE collector_id = $1`, standby); n != "0" {
		t.Errorf("standby must not get its own device rows, has %s", n)
	}

	// 7. The primary returns: no automatic failback.
	exec(`UPDATE collectors SET last_seen_at = now() WHERE id = $1`, primary)
	if out := poll(primaryBID); out.Role != "standby" {
		t.Errorf("returning primary should stand by, got %q", out.Role)
	}
	if c := pull(primaryBID); len(c.Devices) != 0 {
		t.Errorf("standing-by primary should get no devices: %+v", c)
	}

	// 8. Make active (handover back to the primary): the holder steps
	//    down at its next poll; the primary waits out the grace period.
	if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
		_, err := collectorha.RequestHandover(ctx, tx, primary)
		return err
	}); err != nil {
		t.Fatalf("handover: %v", err)
	}
	if out := poll(standbyBID); out.Role != "standby" {
		t.Errorf("holder should step down on handover, got %q", out.Role)
	}
	if out := poll(primaryBID); out.Role != "standby" {
		t.Error("target must wait for the handover grace before going active")
	}
	exec(`UPDATE collectors SET lease_not_before = now() - interval '1 second' WHERE id = $1`, primary)
	if out := poll(primaryBID); out.Role != "active" {
		t.Errorf("after the grace the target should be active, got %q", out.Role)
	}
	if out := poll(standbyBID); out.Role != "standby" {
		t.Errorf("old holder stays standby, got %q", out.Role)
	}

	// An offline machine can't be made active.
	exec(`UPDATE collectors SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, standby)
	err = st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
		_, err := collectorha.RequestHandover(ctx, tx, standby)
		return err
	})
	if err != collectorha.ErrMachineOffline {
		t.Errorf("handover to an offline machine: %v, want ErrMachineOffline", err)
	}

	// 9. Alerts: a standby offline is a warning, as is a primary whose
	//    standby is covering; a lone collector offline stays critical.
	exec(`UPDATE collectors SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, single)
	collectorhealth.NewWatcher(st.AdminPool(), nil, time.Minute, 5*time.Minute, quiet).Sweep(ctx)
	sev := func(id string) string {
		return q(`SELECT severity || ':' || message FROM alerts WHERE collector_id = $1 AND alert_key = 'collector_offline' AND status = 'open'`, id)
	}
	if s := sev(standby); !strings.HasPrefix(s, "warning:Standby collector offline") {
		t.Errorf("standby offline alert: %s", s)
	}
	if s := sev(single); !strings.HasPrefix(s, "critical:") {
		t.Errorf("single collector offline alert: %s", s)
	}
	exec(`UPDATE collectors SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, primary)
	exec(`UPDATE collectors SET last_seen_at = now() WHERE id = $1`, standby)
	collectorhealth.NewWatcher(st.AdminPool(), nil, time.Minute, 5*time.Minute, quiet).Sweep(ctx)
	if s := sev(primary); !strings.HasPrefix(s, "warning:Collector offline: its standby has taken over") {
		t.Errorf("covered primary offline alert: %s", s)
	}
}
