package integrations

// Integration tests for the Neat Pulse poller. Run only when
// TEST_DATABASE_URL points at a disposable Postgres (see newEnv). A fake
// Pulse serves the REST endpoints; every name, serial and id is fake.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePulse struct {
	srv   *httptest.Server
	mode  atomic.Value // "ok", "unauth", "down", "ratelimit", "notfound"
	mu    sync.Mutex
	calls map[string]int // path -> count
	// barConnected and padConnected drive the fake devices' state.
	barConnected, padConnected atomic.Bool
	// unpaired clears both pairingSerials; ghost points the Bar's at a serial
	// no endpoint has.
	unpaired, ghost atomic.Bool
}

func (f *fakePulse) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for p, c := range f.calls {
		if strings.HasPrefix(p, prefix) {
			n += c
		}
	}
	return n
}

// newFakePulse serves org org with a Zoom-mode Neat Bar (ep-bar) paired with
// a Neat Pad (ep-pad), and a Teams-mode Neat Board (ep-board) with no Pad.
func newFakePulse(t *testing.T) *fakePulse {
	t.Helper()
	f := &fakePulse{calls: map[string]int{}}
	f.mode.Store("ok")
	f.barConnected.Store(true)
	f.padConnected.Store(false)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.URL.Path]++
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fake-neat-key" {
			w.WriteHeader(401)
			return
		}
		switch f.mode.Load() {
		case "unauth":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"forbidden","status":403}`))
			return
		case "down":
			w.WriteHeader(503)
			return
		case "ratelimit":
			w.Header().Set("Retry-After", "3600") // capped to 5 minutes
			w.WriteHeader(429)
			return
		case "notfound":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"organisation not found","status":404}`))
			return
		}
		barPair, padPair := "FAKE-NP-1", "FAKE-NB-1"
		if f.unpaired.Load() {
			barPair, padPair = "", ""
		}
		if f.ghost.Load() {
			barPair, padPair = "FAKE-GHOST-1", ""
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v1 orgs <org> endpoints [id]
		if len(parts) < 4 || parts[0] != "v1" || parts[1] != "orgs" || parts[3] != "endpoints" {
			w.WriteHeader(404)
			return
		}
		switch {
		case len(parts) == 4:
			fmt.Fprintf(w, `{"endpoints":[
 {"id":"ep-bar","connected":%t,"inCallStatus":"ZOOM","model":"Neat Bar","serial":"FAKE-NB-1","roomId":7,"roomName":"Fake Huddle","locationId":1,"locationName":"Fake Site"},
 {"id":"ep-pad","connected":%t,"inCallStatus":"NONE","model":"Neat Pad","serial":"FAKE-NP-1","roomId":7,"roomName":"Fake Huddle"},
 {"id":"ep-board","connected":true,"inCallStatus":"NONE","model":"Neat Board","serial":"FAKE-NBD-1","roomId":8,"roomName":"Fake Board Room"}]}`,
				f.barConnected.Load(), f.padConnected.Load())
		case len(parts) == 5 && parts[4] == "sensor":
			fmt.Fprintf(w, `{"data":[{"id":"ep-bar","endpointData":{"shutterClosed":true,"data":[
 {"co2":612.5,"humidity":38.5,"illumination":180,"people":3,"temp":22.25,"voc":95,"vocIndex":110,"timestamp":"%d"}]}}]}`, time.Now().Unix())
		case len(parts) == 5 && parts[4] == "ep-bar":
			fmt.Fprintf(w, `{"id":"ep-bar","connected":true,"model":"Neat Bar","serial":"FAKE-NB-1","firmwareVersion":"NFF1.FAKE.1",
 "latestVersion":"NFF1.FAKE.2","upgradeStatus":"IDLE","primaryMode":"ZOOM_ROOMS","pairingSerial":"%s",
 "connectionTime":"2026-11-20T08:00:00Z","restartTime":"2026-11-19T02:00:00Z",
 "networks":[{"macAddress":"AABeAFMB","type":"NETWORK_TYPE_ETHERNET","linkUp":true}],"roomId":7,"roomName":"Fake Huddle"}`, barPair)
		case len(parts) == 5 && parts[4] == "ep-pad":
			fmt.Fprintf(w, `{"id":"ep-pad","model":"Neat Pad","serial":"FAKE-NP-1","pairingSerial":"%s","primaryMode":"ZOOM_ROOMS","roomId":7}`, padPair)
		case len(parts) == 5 && parts[4] == "ep-board":
			_, _ = w.Write([]byte(`{"id":"ep-board","model":"Neat Board","serial":"FAKE-NBD-1","firmwareVersion":"NFF1.FAKE.2","latestVersion":"NFF1.FAKE.2","primaryMode":"MICROSOFT_TEAMS_ROOMS","roomId":8}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// neatTenant is one customer with a connected Neat integration and linked devices.
func (e *env) neatTenant(org string, links map[string]string) tenantFixture {
	enc, _ := e.cipher.Encrypt([]byte("fake-neat-key"))
	cust := e.q(`INSERT INTO customers (name) VALUES ('neat-poller-fake') RETURNING id::text`)
	integ := e.q(`INSERT INTO integrations (customer_id, provider, status, secret_enc, account_id)
	              VALUES ($1, 'neat_pulse', 'connected', $2, $3) RETURNING id::text`, cust, enc, org)
	f := tenantFixture{cust: cust, integ: integ, devs: map[string]string{}}
	for ext, typ := range links {
		id := e.q(`INSERT INTO devices (customer_id, source, platform, name, type)
		           VALUES ($1, 'neat_pulse', 'android', $2, $3) RETURNING id::text`, cust, "Fake "+ext, typ)
		e.exec(`INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id) VALUES ($1,$2,$3,$4)`,
			id, cust, integ, ext)
		f.devs[ext] = id
	}
	return f
}

func (e *env) neatPoller(pulse *fakePulse, alerts AlertEvaluator, customers ...string) *Poller {
	p := NewPoller(e.st.AdminPool(), e.cipher, nil, alerts, 0, e.quiet).SetNeatBaseURL(pulse.srv.URL)
	p.onlyCustomers = customers
	return p
}

func TestNeatPollWritesReadingsPeripheralsAndSensors(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-read", map[string]string{"ep-bar": "zoom_room", "ep-board": "teams_room", "ep-gone": "teams_room"})
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := e.neatPoller(pulse, alerts, f.cust)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bar := f.devs["ep-bar"]
	status, metrics, fresh := e.dev(bar)
	if status != "online" || !fresh {
		t.Fatalf("bar status=%q fresh=%v", status, fresh)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(metrics), &m)
	want := map[string]any{
		"call_state": "in_call", "active_calls": float64(1), "call_platform": "zoom", "primary_mode": "ZOOM_ROOMS",
		"software_version": "NFF1.FAKE.1", "firmware_update": "NFF1.FAKE.2", "firmware_upgrade_status": "idle",
		"people_count": float64(3), "temperature_c": 22.25, "humidity_pct": 38.5, "co2_ppm": 612.5, "voc": float64(95),
		"voc_index": float64(110), "illuminance_lux": float64(180), "privacy_shutter_closed": true,
		"last_connected": "2026-11-20T08:00:00Z", "last_restart": "2026-11-19T02:00:00Z", "peripherals_linked": false,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("metrics[%s] = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["sensor_at"].(string); !ok {
		t.Errorf("sensor_at missing: %s", metrics)
	}
	ps, _ := m["peripherals"].([]any)
	if len(ps) != 1 {
		t.Fatalf("peripherals = %v", m["peripherals"])
	}
	pad := ps[0].(map[string]any)
	if pad["model"] != "Neat Pad" || pad["connected"] != false {
		t.Errorf("pad = %v", pad)
	}
	if miss, _ := m["peripherals_missing"].([]any); len(miss) != 1 || miss[0] != "Neat Pad" {
		t.Errorf("peripherals_missing = %v", m["peripherals_missing"])
	}
	var make_, model, serial, fw, mac string
	if err := e.su.QueryRow(context.Background(), `SELECT make, model, serial_number, firmware_version, mac_address FROM devices WHERE id=$1`, bar).
		Scan(&make_, &model, &serial, &fw, &mac); err != nil {
		t.Fatal(err)
	}
	if make_ != "Neat" || model != "Neat Bar" || serial != "FAKE-NB-1" || fw != "NFF1.FAKE.1" || mac != "00:00:5e:00:53:01" {
		t.Errorf("identity %q %q %q %q %q", make_, model, serial, fw, mac)
	}
	if n := e.count(`SELECT count(*) FROM telemetry WHERE device_id=$1 AND status='online' AND metrics ? 'people_count'`, bar); n != 1 {
		t.Errorf("telemetry rows with people_count = %d", n)
	}
	r, _ := alerts.get(bar)
	if r.Status != "online" || r.Source != "neat_pulse" || r.Label != "Zoom Room" || r.LinkedDevices != 1 ||
		len(r.MissingPeripherals) != 1 || r.SignedIn != nil || r.TeamsHealth != "" {
		t.Errorf("alert readings %+v", r)
	}

	// The Board is paired with nothing: an empty peripherals list, nothing
	// missing. It has no sensor data: those readings are omitted.
	_, bm, _ := e.dev(f.devs["ep-board"])
	if !strings.Contains(bm, `"peripherals": []`) || !strings.Contains(bm, `"peripherals_linked": true`) ||
		strings.Contains(bm, "people_count") || strings.Contains(bm, "firmware_update\"") ||
		!strings.Contains(bm, `"call_state": "idle"`) {
		t.Errorf("board metrics: %s", bm)
	}
	if r, _ := alerts.get(f.devs["ep-board"]); r.Label != "Teams Room" || r.LinkedDevices != 0 || r.PeripheralsLinked == nil || !*r.PeripheralsLinked {
		t.Errorf("board alert readings %+v", r)
	}
	// A linked device missing from the organisation is unknown.
	if s, gm, _ := e.dev(f.devs["ep-gone"]); s != "unknown" || !strings.Contains(gm, "Not found in this Neat Pulse organisation") {
		t.Errorf("gone device %q %s", s, gm)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Errorf("integration %q", got)
	}

	// One list and one sensor call per cycle; details are cached for 15 min.
	details := pulse.count("/v1/orgs/org-neat-read/endpoints/ep-")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := pulse.count("/v1/orgs/org-neat-read/endpoints/ep-"); n != details {
		t.Errorf("details calls went from %d to %d on the second cycle", details, n)
	}
	if n := pulse.count("/v1/orgs/org-neat-read/endpoints/sensor"); n != 2 {
		t.Errorf("sensor calls = %d, want 2", n)
	}
	// The pad comes back: peripherals all connected.
	pulse.padConnected.Store(true)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, metrics, _ = e.dev(bar)
	if !strings.Contains(metrics, `"peripherals_missing": []`) || !strings.Contains(metrics, `"peripherals_linked": true`) {
		t.Errorf("pad back: %s", metrics)
	}
}

func TestNeatPollFailureMarksUnknown(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-fail", map[string]string{"ep-bar": "zoom_room"})
	bar := f.devs["ep-bar"]
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := e.neatPoller(pulse, alerts, f.cust)

	pulse.mode.Store("unauth")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.integration(f.integ); got != "error|Neat Pulse rejected the API key" {
		t.Fatalf("integration %q", got)
	}
	if s, m, fresh := e.dev(bar); s != "unknown" || !fresh || !strings.Contains(m, "Neat Pulse rejected the API key") {
		t.Fatalf("rejected: %q %v %s", s, fresh, m)
	}
	if r, _ := alerts.get(bar); r.Status != "unknown" {
		t.Fatalf("alert readings %+v", r)
	}

	pulse.mode.Store("down")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.integration(f.integ); got != "error|Neat Pulse unreachable" {
		t.Fatalf("integration %q", got)
	}
	if s, m, _ := e.dev(bar); s != "unknown" || !strings.Contains(m, "Neat Pulse unreachable") {
		t.Fatalf("down: %q %s", s, m)
	}

	// Recovery.
	pulse.mode.Store("ok")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.dev(bar); s != "online" {
		t.Fatalf("not recovered: %q", s)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Fatalf("integration %q", got)
	}

	// A 429 changes nothing, and Retry-After holds off the next cycle.
	pulse.mode.Store("ratelimit")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := pulse.count("/v1/orgs/org-neat-fail/")
	pulse.mode.Store("ok")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := pulse.count("/v1/orgs/org-neat-fail/"); n != before {
		t.Fatalf("polled during Retry-After: %d -> %d calls", before, n)
	}
	if s, _, _ := e.dev(bar); s != "online" {
		t.Fatalf("rate limit changed status: %q", s)
	}
	if got := e.integration(f.integ); got != "connected|" {
		t.Fatalf("integration %q", got)
	}
}

func TestNeatOffboardingCustomerNotPolled(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-offb", map[string]string{"ep-bar": "zoom_room"})
	e.exec(`UPDATE customers SET offboarding_started_at = now(), offboarding_started_by = 'test',
	        delete_after = now() + interval '30 days' WHERE id = $1`, f.cust)
	if err := e.neatPoller(pulse, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := pulse.count("/v1/orgs/org-neat-offb/"); n != 0 {
		t.Fatalf("offboarding customer's integration called Pulse %d times", n)
	}
}

func TestNeatDisconnectedNotPolled(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-disc", map[string]string{"ep-bar": "zoom_room"})
	e.exec(`UPDATE integrations SET status='disconnected', secret_enc=NULL WHERE id=$1`, f.integ)
	if err := e.neatPoller(pulse, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := pulse.count("/v1/orgs/org-neat-disc/"); n != 0 {
		t.Fatalf("disconnected integration called Pulse %d times", n)
	}
	if s, m, _ := e.dev(f.devs["ep-bar"]); s != "unknown" || !strings.Contains(m, "Integration disconnected") {
		t.Fatalf("%q %s", s, m)
	}
}

// The shared Alerter opens the offline (5 min) and pad-missing (10 min)
// alerts for a Neat room, with Zoom Room wording.
func TestNeatAlertsOfflineAndPadMissing(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-alerts", map[string]string{"ep-bar": "zoom_room"})
	bar := f.devs["ep-bar"]
	clock := at(12, 0)
	disp := &recDispatch{}
	alerter := NewAlerter(e.st.AdminPool(), nil, func() time.Time { return clock }, e.quiet)
	alerter.dispatch = disp.add
	p := e.neatPoller(pulse, alerter, f.cust)
	poll := func(ts time.Time) {
		t.Helper()
		clock = ts
		if err := p.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	open := func(key string) (int, string) {
		var n int
		var msg string
		_ = e.su.QueryRow(context.Background(), `SELECT count(*), COALESCE(max(message), '') FROM alerts
		     WHERE device_id = $1 AND alert_key = $2 AND status = 'open'`, bar, key).Scan(&n, &msg)
		return n, msg
	}

	// Pad missing while the Bar is online: opens after 10 minutes.
	poll(at(12, 0))
	poll(at(12, 9))
	if n, _ := open("peripherals_missing"); n != 0 {
		t.Fatal("pad alert opened before the 10 minute hold")
	}
	poll(at(12, 10))
	if n, msg := open("peripherals_missing"); n != 1 || msg != "Peripheral missing: Neat Pad" {
		t.Fatalf("pad alert: %d %q", n, msg)
	}

	// The Bar goes offline: opens after 5 minutes, worded for a Zoom Room.
	pulse.barConnected.Store(false)
	poll(at(12, 20))
	poll(at(12, 25))
	if n, msg := open("device_offline"); n != 1 || msg != "Zoom Room offline" {
		t.Fatalf("offline alert: %d %q", n, msg)
	}
	// Teams-only conditions stay dormant (no data from Pulse).
	if n := e.count(`SELECT count(*) FROM integration_alert_state WHERE device_id = $1 AND condition IN ('teams_signed_out','teams_unhealthy')`, bar); n != 0 {
		t.Fatalf("teams conditions tracked: %d", n)
	}

	// Back online with the pad: both clear.
	pulse.barConnected.Store(true)
	pulse.padConnected.Store(true)
	poll(at(12, 30))
	if n, _ := open("device_offline"); n != 0 {
		t.Fatal("offline alert not resolved")
	}
	if n, _ := open("peripherals_missing"); n != 0 {
		t.Fatal("pad alert not resolved")
	}
	// Overnight hold: an outage starting at 01:00 opens nothing until 05:00.
	pulse.barConnected.Store(false)
	poll(at(1, 0))
	poll(at(1, 30))
	if n, _ := open("device_offline"); n != 0 {
		t.Fatal("alert opened in the overnight hold")
	}
	poll(at(5, 0))
	if n, _ := open("device_offline"); n != 1 {
		t.Fatal("alert not opened after the overnight hold")
	}
}

// After the Pad is unpaired in Pulse, the pad-missing alert resolves.
func TestNeatUnpairClearsPadAlert(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-unpair", map[string]string{"ep-bar": "zoom_room"})
	bar := f.devs["ep-bar"]
	clock := at(12, 0)
	alerter := NewAlerter(e.st.AdminPool(), nil, func() time.Time { return clock }, e.quiet)
	alerter.dispatch = (&recDispatch{}).add
	p := e.neatPoller(pulse, alerter, f.cust)
	poll := func(ts time.Time) {
		t.Helper()
		clock = ts
		if err := p.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	open := func() int {
		return e.count(`SELECT count(*) FROM alerts WHERE device_id = $1 AND alert_key = 'peripherals_missing' AND status = 'open'`, bar)
	}
	poll(at(12, 0))
	poll(at(12, 10))
	if open() != 1 {
		t.Fatal("pad-missing alert not opened")
	}
	pulse.unpaired.Store(true)
	p.neat[f.integ].details = map[string]neatDetail{} // the 15-minute cache has expired
	poll(at(12, 15))
	if open() != 0 {
		t.Fatal("pad-missing alert not resolved after unpairing")
	}
	_, m, _ := e.dev(bar)
	if !strings.Contains(m, `"peripherals": []`) || !strings.Contains(m, `"peripherals_missing": []`) {
		t.Fatalf("metrics after unpairing: %s", m)
	}
}

// A pairingSerial that matches no Pulse endpoint is listed with an unknown
// state and never counts as missing.
func TestNeatUnmatchedPairingNotMissing(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	pulse.ghost.Store(true)
	f := e.neatTenant("org-neat-ghost", map[string]string{"ep-bar": "zoom_room"})
	alerts := &recAlerts{seen: map[string]Readings{}}
	if err := e.neatPoller(pulse, alerts, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, metrics, _ := e.dev(f.devs["ep-bar"])
	var m map[string]any
	_ = json.Unmarshal([]byte(metrics), &m)
	ps, _ := m["peripherals"].([]any)
	if len(ps) != 1 {
		t.Fatalf("peripherals = %v", m["peripherals"])
	}
	pad := ps[0].(map[string]any)
	if pad["name"] != "Paired device (not in Pulse)" {
		t.Errorf("pad = %v", pad)
	}
	if _, has := pad["connected"]; has {
		t.Errorf("unknown pad must have no connected state: %v", pad)
	}
	if miss, _ := m["peripherals_missing"].([]any); len(miss) != 0 {
		t.Errorf("peripherals_missing = %v", m["peripherals_missing"])
	}
	if r, _ := alerts.get(f.devs["ep-bar"]); len(r.MissingPeripherals) != 0 {
		t.Errorf("alert readings %+v", r)
	}
}

// Retry-After is capped at 5 minutes, and a hold-off longer than 10 minutes
// shows the devices unknown so they never go stale, then offline.
func TestNeatLongRateLimitMarksUnknown(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	f := e.neatTenant("org-neat-rl", map[string]string{"ep-bar": "zoom_room"})
	bar := f.devs["ep-bar"]
	alerts := &recAlerts{seen: map[string]Readings{}}
	p := e.neatPoller(pulse, alerts, f.cust)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	pulse.mode.Store("ratelimit")
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := p.neat[f.integ]
	if wait := time.Until(st.notBefore); wait > 5*time.Minute+time.Second || wait < 4*time.Minute {
		t.Fatalf("hold-off %v, want capped at 5 minutes", wait)
	}
	// A short hold-off changes nothing.
	if s, _, _ := e.dev(bar); s != "online" {
		t.Fatalf("short hold-off changed status: %q", s)
	}
	// The hold-off has lasted over 10 minutes.
	st.rlSince = time.Now().Add(-11 * time.Minute)
	e.exec(`UPDATE devices SET last_seen_at = now() - interval '20 minutes' WHERE id = $1`, bar)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, m, fresh := e.dev(bar)
	if s != "unknown" || !fresh || !strings.Contains(m, "Neat Pulse rate limiting") {
		t.Fatalf("long hold-off: %q fresh=%v %s", s, fresh, m)
	}
	if r, _ := alerts.get(bar); r.Status != "unknown" || r.Source != "neat_pulse" || r.Label != "Zoom Room" {
		t.Fatalf("alert readings %+v", r)
	}
	// Pulse answers again: the device recovers and the hold-off ends.
	pulse.mode.Store("ok")
	st.notBefore = time.Time{}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.dev(bar); s != "online" || !st.rlSince.IsZero() {
		t.Fatalf("not recovered: %q rlSince=%v", s, st.rlSince)
	}
}

func TestNeatOrgNotFound(t *testing.T) {
	e := newEnv(t)
	pulse := newFakePulse(t)
	pulse.mode.Store("notfound")
	f := e.neatTenant("org-neat-404", map[string]string{"ep-bar": "zoom_room"})
	if err := e.neatPoller(pulse, nil, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.integration(f.integ); got != "error|Neat Pulse organisation not found" {
		t.Fatalf("integration %q", got)
	}
	if s, m, _ := e.dev(f.devs["ep-bar"]); s != "unknown" || !strings.Contains(m, "Neat Pulse organisation not found") {
		t.Fatalf("%q %s", s, m)
	}
}

func TestNeatSampleUsable(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		ts    time.Time
		fresh bool
		want  bool
	}{
		{"recent", now.Add(-9 * time.Minute), false, true},
		{"too old", now.Add(-11 * time.Minute), true, false},
		{"slightly ahead", now.Add(4 * time.Minute), true, true},
		{"far ahead", now.Add(6 * time.Minute), true, false},
		{"no timestamp, this cycle", time.Time{}, true, true},
		{"no timestamp, cached", time.Time{}, false, false},
	}
	for _, c := range cases {
		if got := sampleUsable(c.ts, c.fresh, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
