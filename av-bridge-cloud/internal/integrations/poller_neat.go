package integrations

// Neat Pulse polling. Each cycle makes one ListEndpoints and one SensorData
// call per integration, plus endpoint-details calls that are cached for
// neatDetailTTL per device (and capped per cycle), to stay polite: Pulse
// documents no rate limits. Only a room's main unit (Bar, Board, Frame...) is
// a M.A.R.C.U.S. device; the Neat Pads paired with it are its peripherals.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/integrations/neatpulse"
)

const (
	// neatDetailTTL is how long one endpoint's details are reused.
	neatDetailTTL = 15 * time.Minute
	// neatMaxDetailCalls caps the details calls per integration per cycle.
	neatMaxDetailCalls = 30
	// neatSensorTTL is how long sensor data is reused when the sensor call
	// fails.
	neatSensorTTL = 15 * time.Minute
	// neatSampleMaxAge: an older sample is left out (it would count people in
	// the wrong hour of Room usage).
	neatSampleMaxAge = 10 * time.Minute
	// neatSampleMaxAhead: a sample this far in the future is left out (a
	// clock or unit error).
	neatSampleMaxAhead = 5 * time.Minute
	// neatRateLimitUnknown: once Pulse has held us off for this long, the
	// linked devices show unknown (so last_seen_at keeps moving and they never
	// go stale, then offline).
	neatRateLimitUnknown = 10 * time.Minute
	msgNeatRateLimited   = "Neat Pulse rate limiting"
)

// neatState is one Neat integration's client and caches. It is only touched
// by that integration's poll, which never runs concurrently with itself.
type neatState struct {
	key       string
	client    *neatpulse.Client
	details   map[string]neatDetail // endpoint id -> details
	sensors   map[string]neatpulse.EndpointSensors
	sensorsAt time.Time
	seen      map[string]time.Time // endpoint id -> last seen connected
	notBefore time.Time            // Retry-After from a 429
	rlSince   time.Time            // first 429 of the current hold-off; zero when none
}

// rateLimited records a 429 and its (already capped) Retry-After.
func (s *neatState) rateLimited(now time.Time, wait time.Duration) {
	if s.rlSince.IsZero() {
		s.rlSince = now
	}
	if until := now.Add(wait); until.After(s.notBefore) {
		s.notBefore = until
	}
}

type neatDetail struct {
	ep neatpulse.Endpoint
	at time.Time
}

// neatFor returns an integration's Neat state, rebuilt when the key or
// organisation changes.
func (p *Poller) neatFor(in integrationRow, secret string) *neatState {
	key := in.accountID + "|" + secret
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.neat[in.id]; ok && s.key == key {
		return s
	}
	var opts []neatpulse.Option
	if p.neatBaseURL != "" {
		opts = append(opts, neatpulse.WithBaseURL(p.neatBaseURL))
	}
	s := &neatState{key: key, client: neatpulse.New(secret, opts...), details: map[string]neatDetail{},
		sensors: map[string]neatpulse.EndpointSensors{}, seen: map[string]time.Time{}}
	p.neat[in.id] = s
	return s
}

func (p *Poller) pollNeat(ctx context.Context, in integrationRow, devs []linkedDevice, secret string) error {
	st := p.neatFor(in, secret)
	now := time.Now()
	t := textFor(in.provider)
	// holdOff reports a long rate-limit hold-off on the devices (unknown,
	// never stale); a short one changes nothing.
	holdOff := func() {
		if !st.rlSince.IsZero() && now.Sub(st.rlSince) > neatRateLimitUnknown {
			for _, d := range devs {
				p.markUnknown(ctx, in, d, msgNeatRateLimited, true)
			}
		}
	}
	if now.Before(st.notBefore) {
		holdOff() // Pulse asked us to wait
		return nil
	}
	// Only the Pulse calls have a deadline; the writes below use the parent
	// context so a hung Pulse can still be recorded as unknown.
	nctx, cancel := context.WithTimeout(ctx, p.lensTimeout())
	defer cancel()
	eps, err := st.client.ListEndpoints(nctx, in.accountID)
	if err != nil {
		var rl *neatpulse.RateLimitError
		switch {
		case errors.As(err, &rl):
			st.rateLimited(now, rl.RetryAfter)
			p.log.Info("neat pulse rate limited; skipping cycle", "integration", in.id, "retry_after", rl.RetryAfter)
			holdOff()
		case errors.Is(err, neatpulse.ErrUnauthorized):
			p.failIntegration(ctx, in, devs, t.rejected)
		case errors.Is(err, neatpulse.ErrNotFound):
			p.failIntegration(ctx, in, devs, t.orgNotFound)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			p.failIntegration(ctx, in, devs, t.unreachable)
		}
		return nil
	}
	st.rlSince = time.Time{} // the hold-off is over

	// Sensor data: a failure keeps the last good data for a while rather
	// than dropping the readings for one cycle.
	sensorsFresh := false
	if sens, err := st.client.SensorData(nctx, in.accountID); err == nil {
		sensorsFresh = true
		st.sensors = make(map[string]neatpulse.EndpointSensors, len(sens))
		for _, s := range sens {
			st.sensors[s.EndpointID] = s
		}
		st.sensorsAt = now
	} else {
		var rl *neatpulse.RateLimitError
		if errors.As(err, &rl) {
			st.rateLimited(now, rl.RetryAfter)
		}
		p.log.Info("neat pulse sensor data failed", "integration", in.id, "error", err)
		if now.Sub(st.sensorsAt) > neatSensorTTL {
			st.sensors = map[string]neatpulse.EndpointSensors{}
		}
	}

	byID := make(map[string]neatpulse.BasicEndpoint, len(eps))
	bySerial := make(map[string]neatpulse.BasicEndpoint, len(eps))
	for _, e := range eps {
		byID[e.ID] = e
		if s := normSerial(e.Serial); s != "" {
			bySerial[s] = e
		}
		if e.Connected {
			st.seen[e.ID] = now
		}
	}
	calls := 0
	detail := func(id string) (neatpulse.Endpoint, bool) {
		c, ok := st.details[id]
		if ok && now.Sub(c.at) < neatDetailTTL {
			return c.ep, true
		}
		if calls >= neatMaxDetailCalls || nctx.Err() != nil || now.Before(st.notBefore) {
			return c.ep, ok // stale or nothing; refreshed on a later cycle
		}
		calls++
		ep, err := st.client.Endpoint(nctx, in.accountID, id)
		if err != nil {
			var rl *neatpulse.RateLimitError
			if errors.As(err, &rl) {
				st.rateLimited(now, rl.RetryAfter)
			}
			p.log.Info("neat pulse endpoint details failed", "integration", in.id, "endpoint", id, "error", err)
			return c.ep, ok
		}
		st.details[id] = neatDetail{ep: ep, at: now}
		return ep, true
	}

	for _, d := range devs {
		main, ok := byID[d.externalID]
		if !ok {
			p.markUnknown(ctx, in, d, t.notFound, true)
			continue
		}
		det, hasDet := detail(main.ID)
		pads := neatPeripherals(main, det, hasDet, eps, bySerial, detail)
		var sens *neatpulse.EndpointSensors
		if s, ok := st.sensors[main.ID]; ok {
			sens = &s
		}
		status := "offline"
		if main.Connected {
			status = "online"
		}
		// The details say the unit is paired with nothing: no Pad is missing
		// (this is what clears the alert after a Pad is unpaired).
		unpaired := hasDet && normSerial(det.PairingSerial) == "" && len(pads) == 0
		metrics := neatReadings(main, det, hasDet, pads, unpaired, st.seen, sens, sensorsFresh, now)
		r := Readings{Status: status, Source: ProviderNeatPulse, Label: roomLabel(d.typ)}
		switch {
		case len(pads) > 0:
			r.LinkedDevices = len(pads)
			r.MissingPeripherals = missingPads(pads)
			all := len(r.MissingPeripherals) == 0
			r.PeripheralsLinked = &all
		case unpaired:
			all := true
			r.PeripheralsLinked = &all
		}
		id := identity{make: "Neat", model: main.Model, serial: main.Serial}
		if hasDet {
			id.firmware, id.mac = det.FirmwareVersion, det.MAC()
			if id.model == "" {
				id.model = det.Model
			}
		}
		p.writeReadings(ctx, in, d, status, metrics, id, r)
	}
	// Forget details of endpoints that left the organisation.
	for id := range st.details {
		if _, ok := byID[id]; !ok {
			delete(st.details, id)
		}
	}
	for id := range st.seen {
		if _, ok := byID[id]; !ok {
			delete(st.seen, id)
		}
	}
	return p.setIntegration(ctx, in, "")
}

// roomLabel names a room device type in alert messages.
func roomLabel(typ string) string {
	if typ == "zoom_room" {
		return "Zoom Room"
	}
	return "Teams Room"
}

// neatPad is a device paired with a room's main unit. known is false for a
// pairingSerial that matches no Pulse endpoint: its state is unknown.
type neatPad struct {
	id, model string
	connected bool
	known     bool
}

const msgPadNotInPulse = "Paired device (not in Pulse)"

// neatPeripherals finds the Pads paired with a main unit: the endpoint whose
// serial is the main unit's pairingSerial, and any companion endpoint in the
// same Pulse room whose own pairingSerial is the main unit's serial. A
// pairingSerial that matches no endpoint is listed with an unknown state,
// never as missing.
func neatPeripherals(main neatpulse.BasicEndpoint, det neatpulse.Endpoint, hasDet bool, eps []neatpulse.BasicEndpoint,
	bySerial map[string]neatpulse.BasicEndpoint, detail func(string) (neatpulse.Endpoint, bool)) []neatPad {
	var out []neatPad
	seen := map[string]bool{main.ID: true}
	add := func(e neatpulse.BasicEndpoint) {
		if seen[e.ID] {
			return
		}
		seen[e.ID] = true
		out = append(out, neatPad{id: e.ID, model: e.Model, connected: e.Connected, known: true})
	}
	if hasDet && normSerial(det.PairingSerial) != "" {
		if e, ok := bySerial[normSerial(det.PairingSerial)]; ok {
			add(e)
		} else {
			out = append(out, neatPad{model: msgPadNotInPulse})
		}
	}
	mine := normSerial(main.Serial)
	if mine == "" || main.RoomID == 0 {
		return out
	}
	for _, e := range eps {
		if seen[e.ID] || e.RoomID != main.RoomID || !neatpulse.IsCompanionModel(e.Model) {
			continue
		}
		if pd, ok := detail(e.ID); ok && normSerial(pd.PairingSerial) == mine {
			add(e)
		}
	}
	return out
}

func missingPads(pads []neatPad) []string {
	out := []string{}
	for _, p := range pads {
		if p.known && !p.connected {
			n := strings.TrimSpace(p.model)
			if n == "" {
				n = "Neat device"
			}
			out = append(out, n)
		}
	}
	return out
}

func normSerial(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// neatReadings maps a Neat main unit to the readings stored in
// latest_metrics. Fields Pulse did not report are omitted (unknown, never
// bad). people_count is the key the Room usage report reads.
//
// unpaired means the details say the unit is paired with nothing: an empty
// peripherals list. Sensor samples must be from the last neatSampleMaxAge
// and not in the future; one without a timestamp is used only from this
// cycle's response (sensorsFresh), never from cached data.
func neatReadings(main neatpulse.BasicEndpoint, det neatpulse.Endpoint, hasDet bool, pads []neatPad, unpaired bool,
	seen map[string]time.Time, sens *neatpulse.EndpointSensors, sensorsFresh bool, now time.Time) map[string]any {
	callStatus := main.InCallStatus
	if callStatus == "" && hasDet {
		callStatus = det.InCallStatus
	}
	m := map[string]any{"call_state": "idle", "active_calls": 0}
	if platform := neatpulse.CallPlatform(callStatus); platform != "" {
		m["call_state"], m["active_calls"], m["call_platform"] = "in_call", 1, platform
	}
	if main.InByod {
		m["byod"] = true
	}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			m[k] = v
		}
	}
	setTime := func(k string, t time.Time) {
		if !t.IsZero() {
			m[k] = t.UTC().Format(time.RFC3339)
		}
	}
	set("pulse_room", main.RoomName)
	if hasDet {
		set("primary_mode", det.PrimaryMode)
		set("software_version", det.FirmwareVersion)
		set("firmware_release", det.FirmwareVersionReleaseName)
		if l := strings.TrimSpace(det.LatestVersion); l != "" && l != strings.TrimSpace(det.FirmwareVersion) {
			m["firmware_update"] = l
		}
		if us := strings.ToLower(strings.TrimSpace(det.UpgradeStatus)); us != "" {
			m["firmware_upgrade_status"] = us
		}
		if det.HasScheduledFirmwareUpdate {
			m["firmware_update_scheduled"] = true
		}
		set("ip_address", det.LocalIPAddress)
		setTime("last_connected", det.ConnectionTime.Time)
		setTime("last_restart", det.RestartTime.Time)
	}
	if len(pads) > 0 {
		ps := make([]map[string]any, 0, len(pads))
		for _, p := range pads {
			name := p.model
			if name == "" {
				name = "Neat device"
			}
			e := map[string]any{"name": name, "model": p.model}
			if p.known {
				e["connected"] = p.connected
			}
			if t, ok := seen[p.id]; ok && p.id != "" {
				e["last_seen"] = t.UTC().Format(time.RFC3339)
			}
			ps = append(ps, e)
		}
		m["peripherals"] = ps
		m["peripherals_missing"] = missingPads(pads)
		m["peripherals_linked"] = len(missingPads(pads)) == 0
	} else if unpaired {
		m["peripherals"] = []map[string]any{}
		m["peripherals_missing"] = []string{}
		m["peripherals_linked"] = true
	}
	if sens != nil {
		if sens.ShutterClosed != nil {
			m["privacy_shutter_closed"] = *sens.ShutterClosed
		}
		if s, ok := sens.Latest(); ok && sampleUsable(s.Timestamp, sensorsFresh, now) {
			num := func(k string, v *float64) {
				if v != nil {
					m[k] = *v
				}
			}
			if s.People != nil && *s.People >= 0 {
				m["people_count"] = *s.People
			}
			num("temperature_c", s.Temp)
			num("humidity_pct", s.Humidity)
			num("co2_ppm", s.CO2)
			num("voc", s.VOC)
			num("voc_index", s.VOCIndex)
			num("illuminance_lux", s.Illumination)
			setTime("sensor_at", s.Timestamp)
		}
	}
	return m
}

// sampleUsable reports whether a sensor sample is current enough to write.
func sampleUsable(ts time.Time, fresh bool, now time.Time) bool {
	if ts.IsZero() {
		return fresh
	}
	return now.Sub(ts) <= neatSampleMaxAge && ts.Sub(now) <= neatSampleMaxAhead
}
