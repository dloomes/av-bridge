package adapters

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// mtrScriptOutput is the JSON object mtrwindows.ps1 prints (schema 1).
// A section the PC could not read is null.
type mtrScriptOutput struct {
	Schema        int               `json:"schema"`
	EventLog      *bool             `json:"event_log"`
	Events        []mtrEvent        `json:"events"`
	Errors        []mtrEvent        `json:"errors"`
	Mic           *mtrInUse         `json:"mic"`
	Camera        *mtrInUse         `json:"camera"`
	UDP           *mtrUDP           `json:"udp"`
	PC            *mtrPC            `json:"pc"`
	SectionErrors map[string]string `json:"section_errors"`
}

type mtrEvent struct {
	ID      int    `json:"id"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

type mtrInUse struct {
	InUse bool     `json:"in_use"`
	Apps  []string `json:"apps"`
}

type mtrUDP struct {
	TeamsProcesses int `json:"teams_processes"`
	MediaSockets   int `json:"media_sockets"`
}

type mtrPC struct {
	OSCaption    string `json:"os_caption"`
	OSVersion    string `json:"os_version"`
	UptimeS      int64  `json:"uptime_s"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Serial       string `json:"serial"`
}

// Event IDs in the Skype Room System log.
// unverified: confirm on a Windows MTR — IDs per Microsoft's Teams Rooms
// event log documentation: 2000 information (heartbeat, hardware check),
// 2001 and 3001 errors.
const (
	mtrEventInfo   = 2000
	mtrEventError  = 2001
	mtrEventError2 = 3001
)

// mtrErrorWindow is how recent an error event must be to show as last_error.
const mtrErrorWindow = 15 * time.Minute

// mtrReadings turns the script's JSON into readings (latest_metrics keys
// shared with Poly Lens Teams Rooms). Anything the script could not read is
// left out — unknown, never reported as bad. It also returns the script's
// per-section errors, for logging.
func mtrReadings(raw []byte, now time.Time) (map[string]any, map[string]string, error) {
	var out mtrScriptOutput
	if err := json.Unmarshal(mtrJSONObject(raw), &out); err != nil {
		return nil, nil, fmt.Errorf("Teams Rooms script output unreadable: %w", err)
	}
	m := map[string]any{}

	// ── Event log: heartbeat, errors, peripherals ──
	if hb, fields := mtrHeartbeat(out.Events); hb != nil {
		if t, ok := mtrTime(hb.Time); ok {
			m["last_heartbeat"] = t.Format(time.RFC3339)
		}
		if h := mtrField(fields, "ResourceState"); h != "" {
			m["teams_health"] = mtrHealth(h)
		}
		if v := mtrField(fields, "AppVersion"); v != "" {
			m["teams_app_version"] = v
		}
		if si, ok := mtrSignedIn(fields); ok {
			m["teams_signed_in"] = si
		}
	}
	if _, ok := m["teams_app_version"]; !ok {
		// Any event carries the app version; use the newest that has one.
		for _, e := range mtrNewestFirst(append(append([]mtrEvent{}, out.Events...), out.Errors...)) {
			if v := mtrField(mtrEventFields(e.Message), "AppVersion"); v != "" {
				m["teams_app_version"] = v
				break
			}
		}
	}
	if e := mtrLastError(out.Errors, now); e != "" {
		m["last_error"] = e
	}
	if ps := mtrPeripherals(out.Events, out.Errors); len(ps) > 0 {
		list := make([]map[string]any, 0, len(ps))
		missing := []string{}
		for _, p := range ps {
			e := map[string]any{"name": p.name, "model": p.kind, "connected": p.connected}
			if p.lastSeen != "" {
				e["last_seen"] = p.lastSeen
			}
			list = append(list, e)
			if !p.connected {
				missing = append(missing, p.name)
			}
		}
		m["peripherals"] = list
		m["peripherals_missing"] = missing
		m["peripherals_linked"] = len(missing) == 0
	}

	// ── Call: microphone/camera in use, Teams UDP media sockets ──
	var mic, udp *bool
	if out.Mic != nil || out.Camera != nil {
		v := (out.Mic != nil && out.Mic.InUse) || (out.Camera != nil && out.Camera.InUse)
		mic = &v
		m["call_signal_mic"] = v
	}
	if out.UDP != nil {
		v := out.UDP.MediaSockets > 0
		udp = &v
		m["call_signal_udp"] = v
		m["udp_media_sockets"] = out.UDP.MediaSockets
	}
	// The mic/camera signal decides when it can be read. The new Teams client
	// was seen holding UDP ports >= 50000 while idle, so the UDP signal alone
	// only decides when the mic/camera can't be read. Both raw signals are
	// kept so they can be compared on a real MTR.
	// unverified: confirm on a Windows MTR
	switch {
	case mic != nil && *mic:
		m["call_state"], m["active_calls"] = "in_call", 1
	case mic != nil:
		m["call_state"], m["active_calls"] = "idle", 0
	case udp != nil && *udp:
		m["call_state"], m["active_calls"] = "in_call", 1
	case udp != nil:
		m["call_state"], m["active_calls"] = "idle", 0
	default:
		m["call_state"], m["active_calls"] = "unknown", 0
	}

	// ── PC ──
	if pc := out.PC; pc != nil {
		set := func(k, v string) {
			if v = strings.TrimSpace(v); v != "" {
				m[k] = v
			}
		}
		set("software_version", pc.OSVersion)
		set("os_name", pc.OSCaption)
		set("manufacturer", pc.Manufacturer)
		set("model", pc.Model)
		set("serial_number", pc.Serial)
		if pc.UptimeS > 0 {
			m["uptime_s"] = pc.UptimeS
		}
	}
	return m, out.SectionErrors, nil
}

// mtrJSONObject trims anything around the JSON object (a BOM, a stray line
// PowerShell printed first).
func mtrJSONObject(raw []byte) []byte {
	s := string(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return raw
	}
	return []byte(s[i : j+1])
}

func mtrTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// mtrNewestFirst sorts events newest first; events whose time won't parse
// keep their order at the end.
func mtrNewestFirst(evs []mtrEvent) []mtrEvent {
	out := append([]mtrEvent{}, evs...)
	sort.SliceStable(out, func(i, j int) bool {
		ti, oki := mtrTime(out[i].Time)
		tj, okj := mtrTime(out[j].Time)
		if oki != okj {
			return oki
		}
		return ti.After(tj)
	})
	return out
}

// mtrEventFields parses the JSON description MTR writes as an event's
// message. A message that isn't JSON gives a map holding only Description.
func mtrEventFields(msg string) map[string]any {
	fields := map[string]any{}
	if err := json.Unmarshal(mtrJSONObject([]byte(msg)), &fields); err != nil || len(fields) == 0 {
		return map[string]any{"Description": strings.TrimSpace(msg)}
	}
	return fields
}

// mtrField reads a field case-insensitively as text.
func mtrField(fields map[string]any, name string) string {
	for k, v := range fields {
		if !strings.EqualFold(k, name) {
			continue
		}
		switch x := v.(type) {
		case string:
			return strings.TrimSpace(x)
		case bool:
			return fmt.Sprint(x)
		case float64:
			return fmt.Sprint(x)
		}
	}
	return ""
}

// mtrHeartbeat is the newest heartbeat among the info events: the newest
// ID 2000 whose OperationName is Heartbeat, else (no OperationName on any)
// the newest ID 2000 at all.
// unverified: confirm on a Windows MTR — that heartbeats carry
// "OperationName":"Heartbeat" and other 2000s (hardware checks) don't.
func mtrHeartbeat(events []mtrEvent) (*mtrEvent, map[string]any) {
	var fallback *mtrEvent
	var fallbackFields map[string]any
	for _, e := range mtrNewestFirst(events) {
		if e.ID != mtrEventInfo {
			continue
		}
		f := mtrEventFields(e.Message)
		op := mtrField(f, "OperationName")
		if strings.EqualFold(op, "Heartbeat") {
			e := e
			return &e, f
		}
		if op == "" && fallback == nil {
			e := e
			fallback, fallbackFields = &e, f
		}
	}
	return fallback, fallbackFields
}

// mtrHealth maps ResourceState to healthy / unhealthy, else the raw value.
func mtrHealth(s string) string {
	switch strings.ToLower(s) {
	case "healthy":
		return "healthy"
	case "unhealthy":
		return "unhealthy"
	}
	return s
}

// mtrSignedIn reads the Teams sign-in state from the heartbeat's fields.
// unverified: confirm on a Windows MTR — which field (if any) the heartbeat
// uses for sign-in; these candidate names are guesses.
func mtrSignedIn(fields map[string]any) (bool, bool) {
	for _, name := range []string{"SignInState", "SignInStatus", "SignedIn", "IsSignedIn", "TeamsSignInStatus", "TeamsSignInState"} {
		v := strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(mtrField(fields, name)))
		switch v {
		case "":
			continue
		case "true", "signedin", "yes", "1", "success", "succeeded":
			return true, true
		case "false", "signedout", "notsignedin", "no", "0", "failed", "failure":
			return false, true
		}
	}
	return false, false
}

// mtrLastError is the newest error event's description from the last 15
// minutes, or "".
func mtrLastError(errs []mtrEvent, now time.Time) string {
	for _, e := range mtrNewestFirst(errs) {
		if e.ID != mtrEventError && e.ID != mtrEventError2 {
			continue
		}
		if t, ok := mtrTime(e.Time); ok && now.Sub(t) > mtrErrorWindow {
			continue
		}
		d := mtrField(mtrEventFields(e.Message), "Description")
		if d == "" {
			d = strings.TrimSpace(e.Message)
		}
		if len(d) > 500 {
			d = d[:500]
		}
		if d != "" {
			return d
		}
	}
	return ""
}

// mtrPeripheralLine matches the per-peripheral health lines in event
// descriptions, e.g. "Conference Microphone status : Unhealthy".
// unverified: confirm on a Windows MTR — the "<name> status : Healthy|
// Unhealthy" wording.
var mtrPeripheralLine = regexp.MustCompile(`(?i)((?:[A-Za-z][A-Za-z \-]*?\s)?(?:camera|microphone|mic|speaker|display|screen)[A-Za-z0-9 \-]*?)\s+status\s*:\s*(healthy|unhealthy)`)

type mtrPeripheral struct {
	name, kind string
	connected  bool
	lastSeen   string
}

// mtrPeripherals is each peripheral's latest state across the events,
// oldest to newest so the newest report wins. Ordered by name.
func mtrPeripherals(events, errs []mtrEvent) []mtrPeripheral {
	all := mtrNewestFirst(append(append([]mtrEvent{}, events...), errs...))
	byName := map[string]*mtrPeripheral{}
	for i := len(all) - 1; i >= 0; i-- {
		e := all[i]
		text := mtrField(mtrEventFields(e.Message), "Description")
		if text == "" {
			text = e.Message
		}
		for _, mt := range mtrPeripheralLine.FindAllStringSubmatch(text, -1) {
			name := strings.Join(strings.Fields(mt[1]), " ")
			key := strings.ToLower(name)
			p := byName[key]
			if p == nil {
				p = &mtrPeripheral{name: name, kind: mtrPeripheralKind(name)}
				byName[key] = p
			}
			p.connected = strings.EqualFold(mt[2], "healthy")
			if p.connected {
				if t, ok := mtrTime(e.Time); ok {
					p.lastSeen = t.Format(time.RFC3339)
				}
			}
		}
	}
	out := make([]mtrPeripheral, 0, len(byName))
	for _, p := range byName {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func mtrPeripheralKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "camera"):
		return "Camera"
	case strings.Contains(n, "microphone"), strings.Contains(n, "mic"):
		return "Microphone"
	case strings.Contains(n, "speaker"):
		return "Speaker"
	default:
		return "Display"
	}
}
