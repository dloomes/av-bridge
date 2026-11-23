package integrations

// Integration tests for the Lens alert evaluator. They need TEST_DATABASE_URL
// (see newEnv) and use an injected clock. All names are fake. Dates are in
// November, so Europe/London is UTC and America/New_York is UTC-5.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

type recDispatch struct {
	mu  sync.Mutex
	evs []notify.AlertEvent
}

func (r *recDispatch) add(e notify.AlertEvent) {
	r.mu.Lock()
	r.evs = append(r.evs, e)
	r.mu.Unlock()
}

func (r *recDispatch) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		out = append(out, e.AlertKey)
	}
	return out
}

type alertFx struct {
	e        *env
	cust     string
	dev      string
	clock    time.Time
	disp     *recDispatch
	alerter  *Alerter
}

func at(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, time.UTC) }

// newAlertFx makes a customer with one Lens device, optionally placed in a
// building with the given time zone.
func newAlertFx(t *testing.T, zone string) *alertFx {
	e := newEnv(t)
	f := &alertFx{e: e, disp: &recDispatch{}, clock: at(12, 0)}
	f.cust = e.q(`INSERT INTO customers (name) VALUES ('alerts-fake') RETURNING id::text`)
	f.dev = e.q(`INSERT INTO devices (customer_id, source, platform, name, type)
	             VALUES ($1, 'poly_lens', 'windows', 'Fake Teams Room', 'teams_room') RETURNING id::text`, f.cust)
	if zone != "" {
		reg := e.q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'r') RETURNING id::text`, f.cust)
		loc := e.q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'l') RETURNING id::text`, f.cust, reg)
		bld := e.q(`INSERT INTO buildings (customer_id, location_id, name, timezone) VALUES ($1, $2, 'b', $3) RETURNING id::text`, f.cust, loc, zone)
		room := e.q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'rm') RETURNING id::text`, f.cust, bld)
		e.exec(`UPDATE devices SET room_id = $1 WHERE id = $2`, room, f.dev)
	}
	f.alerter = f.newAlerter()
	return f
}

func (f *alertFx) newAlerter() *Alerter {
	a := NewAlerter(f.e.st.AdminPool(), nil, func() time.Time { return f.clock }, f.e.quiet)
	a.dispatch = f.disp.add
	return a
}

func (f *alertFx) eval(t *testing.T, ts time.Time, r Readings) {
	t.Helper()
	f.clock = ts
	if err := f.alerter.Evaluate(context.Background(), f.cust, f.dev, r); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
}

func (f *alertFx) open(key string) int {
	return f.e.count(`SELECT count(*) FROM alerts WHERE device_id = $1 AND alert_key = $2 AND status = 'open'`, f.dev, key)
}

func (f *alertFx) states() int {
	return f.e.count(`SELECT count(*) FROM integration_alert_state WHERE device_id = $1`, f.dev)
}

func bp(b bool) *bool { return &b }

var offline = Readings{Status: "offline"}
var online = Readings{Status: "online"}

func TestOpensAfterHoldNotBefore(t *testing.T) {
	f := newAlertFx(t, "")
	f.eval(t, at(12, 0), offline)
	f.eval(t, at(12, 4), offline)
	if f.open("device_offline") != 0 || len(f.disp.keys()) != 0 {
		t.Fatal("opened before the 5 minute hold")
	}
	f.eval(t, at(12, 5), offline)
	if f.open("device_offline") != 1 {
		t.Fatal("not opened at the hold")
	}
	f.eval(t, at(12, 20), offline)
	if f.open("device_offline") != 1 || len(f.disp.keys()) != 1 {
		t.Fatalf("expected one alert and one notification, got %v", f.disp.keys())
	}
	var sev, msg string
	var room *string
	if err := f.e.su.QueryRow(context.Background(), `SELECT severity, message, room_id::text FROM alerts WHERE device_id=$1`, f.dev).Scan(&sev, &msg, &room); err != nil {
		t.Fatal(err)
	}
	if sev != "critical" || msg != "Teams Room offline" || room != nil {
		t.Fatalf("alert: %q %q %v", sev, msg, room)
	}
}

func TestTeamsConditionHolds(t *testing.T) {
	f := newAlertFx(t, "")
	r := Readings{Status: "online", SignedIn: bp(false), PeripheralsLinked: bp(false), TeamsHealth: "Degraded"}
	f.eval(t, at(12, 0), r)
	f.eval(t, at(12, 9), r)
	if f.open("teams_signed_out")+f.open("teams_unhealthy")+f.open("peripherals_missing") != 0 {
		t.Fatal("opened before the 10 minute hold")
	}
	f.eval(t, at(12, 10), r)
	if f.open("teams_signed_out") != 1 || f.open("teams_unhealthy") != 1 || f.open("peripherals_missing") != 1 {
		t.Fatal("expected all three open")
	}
	var sev, msg string
	f.e.su.QueryRow(context.Background(), `SELECT severity, message FROM alerts WHERE device_id=$1 AND alert_key='teams_unhealthy'`, f.dev).Scan(&sev, &msg)
	if sev != "warning" || msg != "Teams health: Degraded" {
		t.Fatalf("unhealthy alert: %q %q", sev, msg)
	}
}

func TestPeripheralListAlert(t *testing.T) {
	f := newAlertFx(t, "")
	msgOf := func() string {
		var m string
		f.e.su.QueryRow(context.Background(), `SELECT message FROM alerts WHERE device_id=$1 AND alert_key='peripherals_missing' AND status='open'`, f.dev).Scan(&m)
		return m
	}
	// The list wins over a flag that says everything is linked.
	miss := Readings{Status: "online", PeripheralsLinked: bp(true), LinkedDevices: 3, MissingPeripherals: []string{"Fake TC10"}}
	f.eval(t, at(12, 0), miss)
	f.eval(t, at(12, 9), miss)
	if f.open("peripherals_missing") != 0 {
		t.Fatal("opened before the hold")
	}
	f.eval(t, at(12, 10), miss)
	if f.open("peripherals_missing") != 1 || msgOf() != "Peripheral missing: Fake TC10" {
		t.Fatalf("alert not opened with the name: %q", msgOf())
	}
	f.eval(t, at(12, 20), Readings{Status: "online", PeripheralsLinked: bp(false), LinkedDevices: 3})
	if f.open("peripherals_missing") != 0 {
		t.Fatal("all connected should clear")
	}
	// No list: the flag decides.
	flag := Readings{Status: "online", PeripheralsLinked: bp(false)}
	f.eval(t, at(13, 0), flag)
	f.eval(t, at(13, 10), flag)
	if f.open("peripherals_missing") != 1 || msgOf() != "Peripheral missing" {
		t.Fatalf("flag fallback failed: %q", msgOf())
	}
	// Message truncation.
	if got := conditions[3].message(Readings{MissingPeripherals: []string{"A", "B", "C", "D", "E"}}); got != "Peripheral missing: A, B, C +2 more" {
		t.Fatal(got)
	}
}

func TestMaintenanceHold(t *testing.T) {
	t.Run("short outage inside the window raises nothing", func(t *testing.T) {
		f := newAlertFx(t, "")
		f.eval(t, at(2, 10), offline)
		f.eval(t, at(2, 20), offline)
		f.eval(t, at(2, 30), offline)
		f.eval(t, at(2, 31), online)
		if f.open("device_offline") != 0 || len(f.disp.keys()) != 0 || f.states() != 0 {
			t.Fatalf("expected nothing, got %v states=%d", f.disp.keys(), f.states())
		}
	})
	t.Run("persisting condition opens at 05:00 once", func(t *testing.T) {
		f := newAlertFx(t, "")
		f.eval(t, at(2, 10), offline)
		f.eval(t, at(2, 20), offline)
		f.eval(t, at(4, 59), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened inside the window")
		}
		f.eval(t, at(5, 0), offline)
		if f.open("device_offline") != 1 {
			t.Fatal("not opened at 05:00")
		}
		f.eval(t, at(5, 5), offline)
		if len(f.disp.keys()) != 1 {
			t.Fatalf("expected one notification, got %v", f.disp.keys())
		}
	})
	t.Run("started 04:58 opens at 05:03", func(t *testing.T) {
		f := newAlertFx(t, "")
		f.eval(t, at(4, 58), offline)
		f.eval(t, at(5, 2), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened before hold elapsed")
		}
		f.eval(t, at(5, 3), offline)
		if f.open("device_offline") != 1 {
			t.Fatal("not opened at 05:03")
		}
	})
	t.Run("started 00:58 held until 05:00", func(t *testing.T) {
		f := newAlertFx(t, "")
		f.eval(t, at(0, 58), offline)
		f.eval(t, at(1, 30), offline)
		f.eval(t, at(4, 59), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened inside the window")
		}
		f.eval(t, at(5, 0), offline)
		if f.open("device_offline") != 1 {
			t.Fatal("not opened at 05:00")
		}
	})
	t.Run("DST transition day follows local time", func(t *testing.T) {
		// 2026-03-29: London moves to BST at 01:00 UTC. 04:30 UTC = 05:30 BST (open);
		// 03:30 UTC = 04:30 BST (held).
		f := newAlertFx(t, "Europe/London")
		d := func(h, m int) time.Time { return time.Date(2026, 3, 29, h, m, 0, 0, time.UTC) }
		f.eval(t, d(2, 0), offline)
		f.eval(t, d(3, 30), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened at 04:30 BST")
		}
		f.eval(t, d(4, 30), offline)
		if f.open("device_offline") != 1 {
			t.Fatal("not opened at 05:30 BST")
		}
	})
	t.Run("building time zone decides the window", func(t *testing.T) {
		f := newAlertFx(t, "America/New_York") // 06:30 UTC = 01:30 local
		f.eval(t, at(6, 0), offline)
		f.eval(t, at(6, 30), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened inside the building's local window")
		}
		f.eval(t, at(10, 0), offline) // 05:00 local
		if f.open("device_offline") != 1 {
			t.Fatal("not opened at 05:00 local")
		}
	})
	t.Run("invalid time zone falls back to London", func(t *testing.T) {
		f := newAlertFx(t, "Not/AZone")
		f.eval(t, at(2, 0), offline)
		f.eval(t, at(2, 30), offline)
		if f.open("device_offline") != 0 {
			t.Fatal("opened inside the London window")
		}
	})
}

func TestAutoResolveAndRecovered(t *testing.T) {
	f := newAlertFx(t, "")
	f.eval(t, at(12, 0), offline)
	f.eval(t, at(12, 5), offline)
	f.eval(t, at(12, 30), online)
	if f.open("device_offline") != 0 || f.states() != 0 {
		t.Fatal("not resolved")
	}
	var by string
	if err := f.e.su.QueryRow(context.Background(), `SELECT status || '|' || resolved_by FROM alerts WHERE device_id=$1`, f.dev).Scan(&by); err != nil || by != "resolved|auto:recovered" {
		t.Fatalf("resolution: %q %v", by, err)
	}
	got := f.disp.keys()
	if len(got) != 2 || got[0] != "device_offline" || got[1] != "device_offline_recovered" {
		t.Fatalf("dispatch: %v", got)
	}
	f.eval(t, at(12, 40), online)
	if len(f.disp.keys()) != 2 {
		t.Fatal("recovered notification repeated")
	}
	if ev := f.disp.evs[1]; ev.Severity != "critical" || ev.Payload["recovered"] != true || ev.Payload["alert_key"] != "device_offline" {
		t.Fatalf("recovered event %q %v", ev.Severity, ev.Payload)
	}
}

func TestRecoveryBeforeHoldIsSilent(t *testing.T) {
	f := newAlertFx(t, "")
	f.eval(t, at(12, 0), offline)
	f.eval(t, at(12, 2), online)
	if len(f.disp.keys()) != 0 || f.states() != 0 {
		t.Fatalf("expected silence, got %v", f.disp.keys())
	}
}

func TestRestartSafe(t *testing.T) {
	f := newAlertFx(t, "")
	f.eval(t, at(12, 0), offline)
	f.alerter = f.newAlerter() // restart
	f.eval(t, at(12, 5), offline)
	if f.open("device_offline") != 1 {
		t.Fatal("state lost across restart")
	}
	f.alerter = f.newAlerter()
	f.eval(t, at(12, 10), offline)
	if len(f.disp.keys()) != 1 {
		t.Fatalf("re-notified after restart: %v", f.disp.keys())
	}
	f.alerter = f.newAlerter()
	f.eval(t, at(12, 15), online)
	if f.open("device_offline") != 0 || len(f.disp.keys()) != 2 {
		t.Fatalf("not cleared after restart: %v", f.disp.keys())
	}
}

func TestUnknownReadingsDoNotFlap(t *testing.T) {
	f := newAlertFx(t, "")
	f.eval(t, at(12, 0), offline)
	f.eval(t, at(12, 3), Readings{Status: "unknown"})
	f.eval(t, at(12, 6), offline)
	if f.open("device_offline") != 1 {
		t.Fatal("unknown reading reset the hold")
	}
	f.eval(t, at(12, 10), Readings{Status: "unknown"})
	if f.open("device_offline") != 1 || len(f.disp.keys()) != 1 {
		t.Fatal("unknown reading cleared an open alert")
	}
	// A missing Teams reading does not clear an open Teams alert either.
	f2 := newAlertFx(t, "")
	f2.eval(t, at(12, 0), Readings{Status: "online", SignedIn: bp(false)})
	f2.eval(t, at(12, 10), Readings{Status: "online", SignedIn: bp(false)})
	f2.eval(t, at(12, 20), Readings{Status: "online"})
	if f2.open("teams_signed_out") != 1 {
		t.Fatal("unreported sign-in cleared the alert")
	}
	// Health values: healthy clears, unknown does not.
	f2.eval(t, at(12, 25), Readings{Status: "online", TeamsHealth: "unknown", SignedIn: bp(true)})
	if f2.open("teams_signed_out") != 0 {
		t.Fatal("signed in did not clear")
	}
}

func TestSwitchedOffSuppression(t *testing.T) {
	f := newAlertFx(t, "Europe/London")
	room := f.e.q(`SELECT room_id::text FROM devices WHERE id = $1`, f.dev)
	run := f.e.q(`INSERT INTO nightly_run (customer_id, room_id, scheduled_at) VALUES ($1, $2, now()) RETURNING id::text`, f.cust, room)
	f.e.exec(`UPDATE devices SET switched_off_by_run = $1 WHERE id = $2`, run, f.dev)
	f.eval(t, at(12, 0), offline)
	f.eval(t, at(12, 30), offline)
	if f.open("device_offline") != 0 {
		t.Fatal("offline alert opened for a device Room Readiness switched off")
	}
	f.e.exec(`UPDATE devices SET switched_off_by_run = NULL WHERE id = $1`, f.dev)
	f.eval(t, at(12, 40), offline)
	if f.open("device_offline") != 1 {
		t.Fatal("alert did not open once the run released the device")
	}
}
