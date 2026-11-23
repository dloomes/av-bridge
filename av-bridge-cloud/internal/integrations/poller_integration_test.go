package integrations

// Integration tests for the Poly Lens poller. Run only when TEST_DATABASE_URL
// points at a disposable Postgres. A fake Lens serves the token and GraphQL
// endpoints; all names and ids are fake.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
)

type capture struct {
	mu  sync.Mutex
	evs []wsfanout.Event
}

func (c *capture) Publish(_ string, ev wsfanout.Event) {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
}
func (c *capture) CloseCustomer(string) int { return 0 }
func (c *capture) byDevice(id string) []wsfanout.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []wsfanout.Event
	for _, e := range c.evs {
		if e.DeviceID == id {
			out = append(out, e)
		}
	}
	return out
}

type recAlerts struct {
	mu   sync.Mutex
	seen map[string]Readings
}

func (r *recAlerts) Evaluate(_ context.Context, _, dev string, rd Readings) error {
	r.mu.Lock()
	r.seen[dev] = rd
	r.mu.Unlock()
	return nil
}

func TestPoller(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping poller integration test")
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

	// Fake Lens. mode: "ok", "unauth" or "ratelimit". Only ext-1 and ext-2
	// exist in the account; ext-gone does not.
	var mode atomic.Value
	mode.Store("ok")
	var gql atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == "unauth" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"access_denied"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		gql.Add(1)
		if mode.Load() == "ratelimit" {
			w.Header().Set("ratelimit-reset", "30")
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[
		 {"node":{"id":"ext-1","name":"Fake Room One","serialNumber":"SN-FAKE-1","macAddress":"00:00:5e:00:53:01","hardwareModel":"Fake Model","softwareVersion":"9.9.9","connected":true,"lastDetected":"2026-11-23T10:00:00Z",
		   "activeApplicationName":"Microsoft Teams Rooms","activeApplicationVersion":"5.1","callStatus":"InCall","teamsDeviceHealthStatus":"Healthy","teamsPresenceAvailability":"Available","allPeripheralsLinked":true}},
		 {"node":{"id":"ext-2","name":"Fake Room Two","serialNumber":"SN-FAKE-2","hardwareModel":"Fake Model","softwareVersion":"9.9.9","connected":false,"activeApplicationName":"Microsoft Teams Rooms"}}
		]}}}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	enc, _ := cipher.Encrypt([]byte("fake-lens-secret"))
	cust := q(`INSERT INTO customers (name) VALUES ('poller-fake') RETURNING id::text`)
	integ := q(`INSERT INTO integrations (customer_id, provider, status, client_id, secret_enc, account_id)
	            VALUES ($1, 'poly_lens', 'connected', 'fake-client', $2, 'acct-a') RETURNING id::text`, cust, enc)
	mkDev := func(name, ext string) string {
		id := q(`INSERT INTO devices (customer_id, source, platform, name, type)
		         VALUES ($1, 'poly_lens', 'windows', $2, 'teams_room') RETURNING id::text`, cust, name)
		exec(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id) VALUES ($1,$2,$3,$4)`,
			id, cust, integ, ext)
		return id
	}
	d1 := mkDev("Fake Room One", "ext-1")
	d2 := mkDev("Fake Room Two", "ext-2")
	dGone := mkDev("Fake Room Gone", "ext-gone")

	live := &capture{}
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := NewPoller(st.AdminPool(), cipher, live, alerts, 0, quiet).SetLensBaseURLs(srv.URL+"/token", srv.URL+"/graphql")
	p.onlyCustomer = cust

	dev := func(id string) (status, metrics string, fresh bool) {
		t.Helper()
		if err := su.QueryRow(ctx, `SELECT COALESCE(latest_status,''), COALESCE(latest_metrics::text,''), last_seen_at > now() - interval '1 minute'
		                             FROM devices WHERE id = $1`, id).Scan(&status, &metrics, &fresh); err != nil {
			t.Fatal(err)
		}
		return
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := su.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("TestPollWritesReadings", func(t *testing.T) {
		if err := p.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		status, metrics, fresh := dev(d1)
		if status != "online" || !fresh {
			t.Fatalf("d1 status=%q fresh=%v", status, fresh)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(metrics), &m)
		if m["teams_signed_in"] != true || m["call_state"] != "in_call" || m["active_calls"] != float64(1) ||
			m["teams_health"] != "Healthy" || m["peripherals_linked"] != true || m["software_version"] != "9.9.9" ||
			m["teams_app_version"] != "5.1" || m["lens_last_seen"] != "2026-11-23T10:00:00Z" {
			t.Fatalf("metrics: %s", metrics)
		}
		var model, serial, fw string
		var online bool
		if err := su.QueryRow(ctx, `SELECT model, serial_number, firmware_version, last_online_at IS NOT NULL FROM devices WHERE id=$1`, d1).
			Scan(&model, &serial, &fw, &online); err != nil {
			t.Fatal(err)
		}
		if model != "Fake Model" || serial != "SN-FAKE-1" || fw != "9.9.9" || !online {
			t.Fatalf("identity %q %q %q %v", model, serial, fw, online)
		}
		if n := count(`SELECT count(*) FROM telemetry WHERE device_id=$1 AND status='online'`, d1); n != 1 {
			t.Fatalf("telemetry rows=%d", n)
		}
		if evs := live.byDevice(d1); len(evs) != 1 || evs[0].EventType != "status" || evs[0].Payload["status"] != "online" {
			t.Fatalf("live events: %+v", evs)
		}
		// d2: connected=false, no optional fields: offline, readings omitted not bad.
		s2, m2, _ := dev(d2)
		if s2 != "offline" || strings.Contains(m2, "teams_signed_in") || strings.Contains(m2, "peripherals_linked") {
			t.Fatalf("d2 %q %s", s2, m2)
		}
		if n := count(`SELECT count(*) FROM devices WHERE id=$1 AND last_online_at IS NULL`, d2); n != 1 {
			t.Fatal("offline device must not set last_online_at")
		}
		if r := alerts.seen[d1]; r.Status != "online" || r.SignedIn == nil || !*r.SignedIn || r.TeamsHealth != "Healthy" {
			t.Fatalf("alert readings: %+v", r)
		}
		if r := alerts.seen[d2]; r.SignedIn != nil || r.PeripheralsLinked != nil {
			t.Fatalf("d2 alert readings should be unknown: %+v", r)
		}
		if got := q(`SELECT status || '|' || COALESCE(last_error,'') FROM integrations WHERE id=$1`, integ); got != "connected|" {
			t.Fatalf("integration %q", got)
		}
	})

	t.Run("DeviceMissingFromAccountIsUnknown", func(t *testing.T) {
		status, metrics, fresh := dev(dGone)
		if status != "unknown" || !fresh || !strings.Contains(metrics, "Not found in this Lens account") {
			t.Fatalf("%q %v %s", status, fresh, metrics)
		}
	})

	t.Run("RateLimitSkipsCycle", func(t *testing.T) {
		mode.Store("ratelimit")
		defer mode.Store("ok")
		before, _, _ := dev(d1)
		if err := p.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		after, _, _ := dev(d1)
		if before != "online" || after != "online" {
			t.Fatalf("rate limit must not change status: %q -> %q", before, after)
		}
		if got := q(`SELECT status FROM integrations WHERE id=$1`, integ); got != "connected" {
			t.Fatalf("integration status %q", got)
		}
	})

	t.Run("TestPollFailureMarksUnknownNotOffline", func(t *testing.T) {
		mode.Store("unauth")
		defer mode.Store("ok")
		// Age the device so only the poller's last_seen_at refresh can keep it fresh.
		exec(`UPDATE devices SET last_seen_at = now() - interval '1 hour' WHERE id = $1`, d1)
		if err := p.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		status, metrics, fresh := dev(d1)
		if status != "unknown" || !fresh || !strings.Contains(metrics, "Poly Lens unreachable") {
			t.Fatalf("%q fresh=%v %s", status, fresh, metrics)
		}
		got := q(`SELECT status || '|' || COALESCE(last_error,'') FROM integrations WHERE id=$1`, integ)
		if !strings.HasPrefix(got, "error|") || strings.Contains(got, "fake-lens-secret") {
			t.Fatalf("integration %q", got)
		}
		// Effective status stays unknown (not offline) after the refresh.
		if alerts.seen[d1].Status != "unknown" {
			t.Fatalf("alert readings %+v", alerts.seen[d1])
		}
		// Recovery: next good poll reconnects.
		mode.Store("ok")
		if err := p.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if s, _, _ := dev(d1); s != "online" {
			t.Fatalf("not recovered: %q", s)
		}
		if got := q(`SELECT status FROM integrations WHERE id=$1`, integ); got != "connected" {
			t.Fatalf("integration %q", got)
		}
	})

	t.Run("DisconnectedIntegrationNotPolled", func(t *testing.T) {
		exec(`UPDATE integrations SET status='disconnected', secret_enc=NULL WHERE id=$1`, integ)
		before := gql.Load()
		if err := p.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if gql.Load() != before {
			t.Fatal("disconnected integration must not call Lens")
		}
		status, metrics, fresh := dev(d1)
		if status != "unknown" || !fresh || !strings.Contains(metrics, "Integration disconnected") {
			t.Fatalf("%q %v %s", status, fresh, metrics)
		}
		if got := q(`SELECT status FROM integrations WHERE id=$1`, integ); got != "disconnected" {
			t.Fatalf("integration status changed to %q", got)
		}
	})
}
