package integrations

// Integration tests for the Poly Lens poller. Run only when TEST_DATABASE_URL
// points at a disposable Postgres. A fake Lens serves the token and GraphQL
// endpoints; all names and ids are fake. Every subtest builds its own
// customers, so they are independent of order and of other tests' data.

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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
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
func (r *recAlerts) get(dev string) (Readings, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.seen[dev]
	return v, ok
}

const deviceJSON = `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[
 {"node":{"id":"ext-1","name":"Fake Room One","serialNumber":"SN-FAKE-1","macAddress":"00:00:5e:00:53:01","hardwareModel":"Fake Model","softwareVersion":"9.9.9","connected":true,"lastDetected":"2026-11-23T10:00:00Z",
   "activeApplicationName":"Microsoft Teams Rooms","activeApplicationVersion":"5.1","callStatus":"InCall","teamsDeviceHealthStatus":"Healthy","teamsPresenceAvailability":"Available","allPeripheralsLinked":true}},
 {"node":{"id":"ext-2","name":"Fake Room Two","serialNumber":"SN-FAKE-2","hardwareModel":"Fake Model","softwareVersion":"9.9.9","connected":false,"activeApplicationName":"Microsoft Teams Rooms"}}
]}}}}}`

type env struct {
	t      *testing.T
	su     *pgx.Conn
	st     *db.Store
	cipher secrets.Cipher
	quiet  *slog.Logger
	srv    *httptest.Server
	mode   atomic.Value // "ok", "unauth", "ratelimit"
	hang   atomic.Value // account id whose requests hang
	gql    sync.Map     // account id -> *atomic.Int32
}

func (e *env) gqlCalls(account string) int32 {
	if v, ok := e.gql.Load(account); ok {
		return v.(*atomic.Int32).Load()
	}
	return 0
}

func (e *env) q(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.su.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.su.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.su.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping poller integration test")
	}
	ctx := context.Background()
	e := &env{t: t, quiet: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := db.Migrate(ctx, dsn, e.quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { su.Close(ctx) })
	e.su = su
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e.st = st
	e.cipher, _ = secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	e.mode.Store("ok")
	e.hang.Store("")

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if e.mode.Load() == "unauth" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"access_denied"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		acct, _ := req.Variables["tenantId"].(string)
		c, _ := e.gql.LoadOrStore(acct, new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		if h, _ := e.hang.Load().(string); h != "" && h == acct {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		if e.mode.Load() == "unauth" {
			w.WriteHeader(401)
			return
		}
		if e.mode.Load() == "ratelimit" {
			w.Header().Set("ratelimit-reset", "30")
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, deviceJSON)
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

// tenantFixture is one customer with a connected integration and devices.
type tenantFixture struct {
	cust, integ string
	devs        map[string]string // external id -> device id
}

func (e *env) tenant(account string, externals ...string) tenantFixture {
	enc, _ := e.cipher.Encrypt([]byte("fake-lens-secret"))
	cust := e.q(`INSERT INTO customers (name) VALUES ('poller-fake') RETURNING id::text`)
	integ := e.q(`INSERT INTO integrations (customer_id, provider, status, client_id, secret_enc, account_id)
	              VALUES ($1, 'poly_lens', 'connected', 'fake-client', $2, $3) RETURNING id::text`, cust, enc, account)
	f := tenantFixture{cust: cust, integ: integ, devs: map[string]string{}}
	for _, ext := range externals {
		id := e.q(`INSERT INTO devices (customer_id, source, platform, name, type)
		           VALUES ($1, 'poly_lens', 'windows', $2, 'teams_room') RETURNING id::text`, cust, "Fake "+ext)
		e.exec(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id) VALUES ($1,$2,$3,$4)`,
			id, cust, integ, ext)
		f.devs[ext] = id
	}
	return f
}

func (e *env) poller(live wsfanout.Publisher, alerts AlertEvaluator, customers ...string) *Poller {
	p := NewPoller(e.st.AdminPool(), e.cipher, live, alerts, 0, e.quiet).
		SetLensBaseURLs(e.srv.URL+"/token", e.srv.URL+"/graphql")
	p.onlyCustomers = customers
	return p
}

func (e *env) dev(id string) (status, metrics string, fresh bool) {
	e.t.Helper()
	if err := e.su.QueryRow(context.Background(), `SELECT COALESCE(latest_status,''), COALESCE(latest_metrics::text,''),
	      COALESCE(last_seen_at > now() - interval '1 minute', false) FROM devices WHERE id = $1`, id).Scan(&status, &metrics, &fresh); err != nil {
		e.t.Fatal(err)
	}
	return
}

func (e *env) integration(id string) string {
	return e.q(`SELECT status || '|' || COALESCE(last_error,'') FROM integrations WHERE id=$1`, id)
}

func TestPollWritesReadings(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-readings", "ext-1", "ext-2")
	live := &capture{}
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := e.poller(live, alerts, f.cust)
	d1, d2 := f.devs["ext-1"], f.devs["ext-2"]
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, metrics, fresh := e.dev(d1)
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
	var make_, model, serial, fw string
	var online bool
	if err := e.su.QueryRow(context.Background(), `SELECT make, model, serial_number, firmware_version, last_online_at IS NOT NULL FROM devices WHERE id=$1`, d1).
		Scan(&make_, &model, &serial, &fw, &online); err != nil {
		t.Fatal(err)
	}
	if make_ != "Poly" || model != "Fake Model" || serial != "SN-FAKE-1" || fw != "9.9.9" || !online {
		t.Fatalf("identity %q %q %q %q %v", make_, model, serial, fw, online)
	}
	if n := e.count(`SELECT count(*) FROM telemetry WHERE device_id=$1 AND status='online'`, d1); n != 1 {
		t.Fatalf("telemetry rows=%d", n)
	}
	if evs := live.byDevice(d1); len(evs) != 1 || evs[0].EventType != "status" || evs[0].Payload["status"] != "online" {
		t.Fatalf("live events: %+v", evs)
	}
	// d2: connected=false, no optional fields: offline, readings omitted not bad.
	s2, m2, _ := e.dev(d2)
	if s2 != "offline" || strings.Contains(m2, "teams_signed_in") || strings.Contains(m2, "peripherals_linked") {
		t.Fatalf("d2 %q %s", s2, m2)
	}
	if n := e.count(`SELECT count(*) FROM devices WHERE id=$1 AND last_online_at IS NULL`, d2); n != 1 {
		t.Fatal("offline device must not set last_online_at")
	}
	if r, _ := alerts.get(d1); r.Status != "online" || r.SignedIn == nil || !*r.SignedIn || r.TeamsHealth != "Healthy" {
		t.Fatalf("alert readings: %+v", r)
	}
	if r, _ := alerts.get(d2); r.SignedIn != nil || r.PeripheralsLinked != nil {
		t.Fatalf("d2 alert readings should be unknown: %+v", r)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Fatalf("integration %q", got)
	}
}

func TestPollDeviceMissingFromAccountIsUnknown(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-missing", "ext-1", "ext-gone")
	if err := e.poller(nil, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, metrics, fresh := e.dev(f.devs["ext-gone"])
	if status != "unknown" || !fresh || !strings.Contains(metrics, "Not found in this Lens account") {
		t.Fatalf("%q %v %s", status, fresh, metrics)
	}
}

func TestPollRateLimitSkipsCycle(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-rate", "ext-1")
	p := e.poller(nil, nil, f.cust)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.mode.Store("ratelimit")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.dev(f.devs["ext-1"]); s != "online" {
		t.Fatalf("rate limit must not change status: %q", s)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Fatalf("integration %q", got)
	}
}

func TestPollFailureMarksUnknownNotOffline(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-unauth", "ext-1")
	d1 := f.devs["ext-1"]
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := e.poller(nil, alerts, f.cust)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Age the device so only the poller's last_seen_at refresh keeps it fresh.
	e.exec(`UPDATE devices SET last_seen_at = now() - interval '1 hour' WHERE id = $1`, d1)
	e.mode.Store("unauth")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, metrics, fresh := e.dev(d1)
	if status != "unknown" || !fresh || !strings.Contains(metrics, "Poly Lens unreachable") {
		t.Fatalf("%q fresh=%v %s", status, fresh, metrics)
	}
	if got := e.integration(f.integ); got != "error|Poly Lens rejected the credentials" {
		t.Fatalf("integration %q", got)
	}
	if r, _ := alerts.get(d1); r.Status != "unknown" {
		t.Fatalf("alert readings %+v", r)
	}
	// Recovery: the next good poll reconnects.
	e.mode.Store("ok")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.dev(d1); s != "online" {
		t.Fatalf("not recovered: %q", s)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Fatalf("integration %q", got)
	}
}

func TestPollNetworkFailureIsUnreachable(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-net", "ext-1")
	p := e.poller(nil, nil, f.cust)
	// Connection refused: a non-401 failure.
	p.SetLensBaseURLs("http://127.0.0.1:1/token", "http://127.0.0.1:1/graphql")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.integration(f.integ); got != "error|Poly Lens unreachable" {
		t.Fatalf("integration %q", got)
	}
	status, metrics, fresh := e.dev(f.devs["ext-1"])
	if status != "unknown" || !fresh || !strings.Contains(metrics, "Poly Lens unreachable") {
		t.Fatalf("%q %v %s", status, fresh, metrics)
	}
}

func TestPollDisconnectedIntegrationNotPolled(t *testing.T) {
	e := newEnv(t)
	// Status disconnected while a secret is still present: must not be polled.
	f := e.tenant("acct-disc", "ext-1")
	e.exec(`UPDATE integrations SET status='disconnected' WHERE id=$1`, f.integ)
	if err := e.poller(nil, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.gqlCalls("acct-disc"); n != 0 {
		t.Fatalf("disconnected integration called Lens %d times", n)
	}
	status, metrics, fresh := e.dev(f.devs["ext-1"])
	if status != "unknown" || !fresh || !strings.Contains(metrics, "Integration disconnected") {
		t.Fatalf("%q %v %s", status, fresh, metrics)
	}
	if got := e.integration(f.integ); got != "disconnected|" {
		t.Fatalf("integration changed to %q", got)
	}
	if n := e.count(`SELECT count(*) FROM telemetry WHERE device_id=$1`, f.devs["ext-1"]); n != 0 {
		t.Fatalf("disconnected must not write telemetry, got %d", n)
	}
	// No secret at all (as after a disconnect): also not polled.
	f2 := e.tenant("acct-disc2", "ext-1")
	e.exec(`UPDATE integrations SET status='disconnected', secret_enc=NULL WHERE id=$1`, f2.integ)
	if err := e.poller(nil, nil, f2.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.gqlCalls("acct-disc2"); n != 0 {
		t.Fatalf("called Lens %d times", n)
	}
}

func TestPollZeroLinkedKeepsStatus(t *testing.T) {
	e := newEnv(t)
	f := e.tenant("acct-zero")
	e.exec(`UPDATE integrations SET status='error', last_error='Poly Lens unreachable', last_success_at = now() - interval '1 day' WHERE id=$1`, f.integ)
	before := e.q(`SELECT last_success_at::text FROM integrations WHERE id=$1`, f.integ)
	if err := e.poller(nil, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.integration(f.integ); got != "error|Poly Lens unreachable" {
		t.Fatalf("integration %q", got)
	}
	if after := e.q(`SELECT last_success_at::text FROM integrations WHERE id=$1`, f.integ); after != before {
		t.Fatalf("last_success_at moved: %s -> %s", before, after)
	}
}

func TestPollHungLensDoesNotStarveOthers(t *testing.T) {
	e := newEnv(t)
	hung := e.tenant("acct-hung", "ext-1")
	good := e.tenant("acct-good", "ext-1")
	e.hang.Store("acct-hung")
	e.exec(`UPDATE devices SET latest_status='online', last_seen_at = now() - interval '1 hour' WHERE id=$1`, hung.devs["ext-1"])
	p := e.poller(nil, nil, hung.cust, good.cust)
	p.timeout = 300 * time.Millisecond
	start := time.Now()
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("cycle took %v", el)
	}
	if s, _, _ := e.dev(good.devs["ext-1"]); s != "online" {
		t.Fatalf("good integration not polled: %q", s)
	}
	status, metrics, fresh := e.dev(hung.devs["ext-1"])
	if status != "unknown" || !fresh || !strings.Contains(metrics, "Poly Lens unreachable") {
		t.Fatalf("hung device %q fresh=%v %s", status, fresh, metrics)
	}
	if got := e.integration(hung.integ); got != "error|Poly Lens unreachable" {
		t.Fatalf("hung integration %q", got)
	}
}

func TestPollIgnoresLinkToAnotherCustomersDevice(t *testing.T) {
	e := newEnv(t)
	victim := e.tenant("acct-victim")
	vdev := e.q(`INSERT INTO devices (customer_id, source, platform, name, type)
	             VALUES ($1, 'poly_lens', 'windows', 'Fake victim', 'teams_room') RETURNING id::text`, victim.cust)
	attacker := e.tenant("acct-attacker")
	// A link row of the attacker's integration that points at the victim's device.
	e.exec(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id) VALUES ($1,$2,$3,'ext-1')`,
		vdev, attacker.cust, attacker.integ)
	live := &capture{}
	alerts := &recAlerts{seen: map[string]Readings{}}
	if err := e.poller(live, alerts, attacker.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, metrics, _ := e.dev(vdev); status != "" || metrics != "" {
		t.Fatalf("victim device written: %q %s", status, metrics)
	}
	if n := e.count(`SELECT count(*) FROM telemetry WHERE device_id=$1`, vdev); n != 0 {
		t.Fatalf("telemetry rows for victim: %d", n)
	}
	if len(live.byDevice(vdev)) != 0 {
		t.Fatal("event published for victim device")
	}
	if _, ok := alerts.get(vdev); ok {
		t.Fatal("alerts evaluated for victim device")
	}
}

func TestClientCachedPerIntegration(t *testing.T) {
	p := NewPoller(nil, nil, nil, nil, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	in := integrationRow{id: "i1", clientID: "c", accountID: "a"}
	c1 := p.clientFor(in, "s")
	if c2 := p.clientFor(in, "s"); c1 != c2 {
		t.Fatal("client not reused")
	}
	if c3 := p.clientFor(in, "s2"); c3 == c1 {
		t.Fatal("changed secret must rebuild the client")
	}
	in.accountID = "b"
	if c4 := p.clientFor(in, "s2"); c4 == c1 {
		t.Fatal("changed account must rebuild the client")
	}
	p.pruneClients(nil)
	if len(p.clients) != 0 {
		t.Fatal("stale clients not pruned")
	}
}

func TestReadingsMapping(t *testing.T) {
	tr := true
	cases := []struct {
		name     string
		d        polylens.Device
		signedIn string // "", "true", "false"
		call     string
		active   int
	}{
		{"offline presence", polylens.Device{PresenceAvailability: "Offline"}, "false", "unknown", 0},
		{"presence unknown", polylens.Device{PresenceAvailability: "presenceunknown"}, "false", "unknown", 0},
		{"signed in", polylens.Device{PresenceAvailability: "Busy"}, "true", "unknown", 0},
		{"no presence", polylens.Device{}, "", "unknown", 0},
		{"in a meeting", polylens.Device{CallStatus: "In a meeting"}, "", "in_call", 1},
		{"idle", polylens.Device{CallStatus: "Not_In_Call"}, "", "idle", 0},
		{"unrecognised", polylens.Device{CallStatus: "Ringing"}, "", "unknown", 0},
		{"peripherals", polylens.Device{PeripheralsLinked: &tr}, "", "unknown", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, signed := readingsFor(c.d)
			if m["call_state"] != c.call || m["active_calls"] != c.active {
				t.Fatalf("call_state=%v active_calls=%v", m["call_state"], m["active_calls"])
			}
			v, has := m["teams_signed_in"]
			switch c.signedIn {
			case "":
				if has || signed != nil {
					t.Fatalf("teams_signed_in should be omitted: %v", v)
				}
			default:
				if !has || signed == nil || *signed != (c.signedIn == "true") || v != (c.signedIn == "true") {
					t.Fatalf("teams_signed_in=%v signed=%v", v, signed)
				}
			}
			if _, ok := m["peripherals_linked"]; ok != (c.d.PeripheralsLinked != nil) {
				t.Fatalf("peripherals_linked presence wrong: %v", m)
			}
		})
	}
}
