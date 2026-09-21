package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Telnet (RFC 854) byte values we handle. The Tesira's Telnet server sends
// IAC sequences during the handshake to negotiate options like ECHO and
// SUPPRESS-GO-AHEAD; without an answer some firmwares hold off sending any
// application data, which is exactly what we were seeing.
const (
	telnetIAC  = 0xFF
	telnetSE   = 0xF0
	telnetSB   = 0xFA
	telnetWILL = 0xFB
	telnetWONT = 0xFC
	telnetDO   = 0xFD
	telnetDONT = 0xFE
)

// telnetFilter wraps a net.Conn and:
//  1. Strips IAC sequences from the inbound byte stream so they don't pollute
//     the TTP parser's view of "lines".
//  2. Auto-replies to any WILL/DO with WONT/DONT, telling the server we
//     refuse to negotiate any options. That satisfies servers that block on
//     negotiation but keeps the connection in plain-text mode.
//
// It implements io.Reader so a bufio.Reader can sit on top of it transparently.
type telnetFilter struct {
	conn net.Conn
	buf  []byte // bytes from conn awaiting IAC processing
}

func newTelnetFilter(conn net.Conn) *telnetFilter {
	return &telnetFilter{conn: conn}
}

func (t *telnetFilter) Read(p []byte) (int, error) {
	if len(t.buf) == 0 {
		raw := make([]byte, len(p)+32)
		n, err := t.conn.Read(raw)
		if n == 0 {
			return 0, err
		}
		t.buf = raw[:n]
	}

	out := 0
	for out < len(p) && len(t.buf) > 0 {
		b := t.buf[0]
		if b != telnetIAC {
			p[out] = b
			out++
			t.buf = t.buf[1:]
			continue
		}
		// IAC sequence — need at least one more byte to decode
		if len(t.buf) < 2 {
			break
		}
		cmd := t.buf[1]
		switch cmd {
		case telnetIAC:
			// Escaped 0xFF in data
			p[out] = telnetIAC
			out++
			t.buf = t.buf[2:]
		case telnetWILL, telnetWONT, telnetDO, telnetDONT:
			if len(t.buf) < 3 {
				return out, nil
			}
			opt := t.buf[2]
			t.buf = t.buf[3:]
			// Refuse all options: respond WONT to WILL/WONT, DONT to DO/DONT.
			var reply [3]byte
			reply[0] = telnetIAC
			if cmd == telnetWILL || cmd == telnetWONT {
				reply[1] = telnetDONT
			} else {
				reply[1] = telnetWONT
			}
			reply[2] = opt
			_, _ = t.conn.Write(reply[:])
		case telnetSB:
			// Subnegotiation: skip until IAC SE
			end := bytes.Index(t.buf[2:], []byte{telnetIAC, telnetSE})
			if end < 0 {
				return out, nil // wait for more bytes
			}
			t.buf = t.buf[2+end+2:]
		default:
			// 2-byte commands (NOP, GA, IP, etc.) — just drop
			t.buf = t.buf[2:]
		}
	}
	return out, nil
}

// TesiraAdapter communicates with Biamp Tesira DSPs using the Tesira Text
// Protocol (TTP) over a persistent TCP/Telnet connection.
//
// It extends the basic Telnet approach with two Tesira-specific behaviours:
//  1. TTP subscription — on connect it subscribes to configured DSP block
//     attributes (mute state, gain level, call status). The Tesira pushes
//     notifications whenever a value changes, so av-bridge receives events
//     in real-time without needing to poll every few seconds.
//  2. TTP response parsing — responses follow a structured format
//     (+OK, -ERR, or ! for subscription notifications) that the adapter
//     parses and routes appropriately.
//
// Config tags used:
//   - mute_instance_tag:  name of the mute DSP block (e.g. "MicMuteLevel1")
//   - gain_instance_tag:  name of the gain DSP block (e.g. "ProgramLevel1")
//   - call_instance_tag:  name of the VoIP/POTS dialler block (optional)
type TesiraAdapter struct {
	device.Base

	conn    net.Conn
	reader  *bufio.Reader
	connMu  sync.Mutex

	// Last parsed state from TTP responses and subscription notifications
	stateMu    sync.RWMutex
	lastMetrics map[string]any

	// Cached device identity — queried once at Connect and reapplied on
	// every telemetry payload. TTP identity attributes don't change
	// mid-session, so this avoids re-querying them each poll.
	identityMu       sync.RWMutex
	partNumber       string
	softwareVersion  string
	hostname         string
	ipAddress        string
	macAddress       string

	// Pending command response — single-request-at-a-time model matches TTP
	respCh chan string
}

func NewTesiraAdapter(cfg config.DeviceConfig) *TesiraAdapter {
	return &TesiraAdapter{
		Base:        device.NewBase(cfg),
		lastMetrics: map[string]any{},
		respCh:      make(chan string, 1),
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *TesiraAdapter) Connect(ctx context.Context) error {
	log := slog.With("device", a.Cfg.ID, "address", a.Cfg.Address)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.Cfg.Address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("tesira connect %s (%s): %w", a.Cfg.ID, a.Cfg.Address, err)
	}
	log.Info("tesira tcp connected")
	// Wrap the conn so Telnet IAC negotiation is handled transparently and
	// stripped from the byte stream the TTP parser sees.
	reader := bufio.NewReader(newTelnetFilter(conn))

	// rawWrite operates on the local conn — the lock-managed a.conn isn't
	// published yet, so we avoid the writeLine() path (and its lock) here.
	rawWrite := func(s string) error {
		if !strings.HasSuffix(s, "\r\n") {
			s += "\r\n"
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := fmt.Fprint(conn, s)
		return err
	}

	// Drain the welcome banner (Telnet negotiation + "Welcome to TTP server")
	// so it doesn't get mixed into the first command response.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break // timeout — banner fully consumed
		}
		log.Debug("tesira banner", "line", strings.TrimSpace(line))
	}
	_ = conn.SetReadDeadline(time.Time{})

	if a.Cfg.Username != "" {
		log.Info("tesira sending credentials", "user", a.Cfg.Username)
		if err := rawWrite(a.Cfg.Username); err != nil {
			conn.Close()
			a.SetStatus(device.StatusOffline)
			return fmt.Errorf("tesira auth username: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := rawWrite(a.Cfg.Password); err != nil {
			conn.Close()
			a.SetStatus(device.StatusOffline)
			return fmt.Errorf("tesira auth password: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Publish the connection so reader/writer paths can use it.
	a.connMu.Lock()
	a.conn = conn
	a.reader = reader
	a.connMu.Unlock()

	a.SetStatus(device.StatusOnline)

	// Start the read loop now so sendAndReceive below works.
	go a.readLoop(ctx)

	// Serialise the handshake. Sending probe + subscriptions back-to-back
	// causes Telnet echo and responses to interleave on the byte stream;
	// waiting for each response keeps the streams cleanly separated.
	log.Info("tesira sending probe", "cmd", "DEVICE get serialNumber")
	if resp, err := a.sendAndReceive(ctx, "DEVICE get serialNumber", 3*time.Second); err != nil {
		log.Warn("tesira probe response timeout", "error", err)
	} else {
		log.Info("tesira probe response", "resp", resp)
	}

	// Query and cache device identity — these attributes don't change
	// mid-session, so we pay the cost once at Connect and reapply the
	// cached values on every telemetry payload. Best-effort: any
	// individual failure just leaves that field empty (older firmwares
	// may not expose all of these).
	a.fetchIdentity(ctx)

	a.sendSubscriptions(ctx)

	log.Info("tesira connected")
	return nil
}

// fetchIdentity queries the static TTP attributes we care about (model,
// firmware, hostname, network coords) and caches them on the adapter.
// Called once from Connect; the poll path reads the cached values.
func (a *TesiraAdapter) fetchIdentity(ctx context.Context) {
	log := slog.With("device", a.Cfg.ID)

	getStr := func(attr string) string {
		resp, err := a.sendAndReceive(ctx, "DEVICE get "+attr, 3*time.Second)
		if err != nil || !strings.HasPrefix(resp, "+OK") {
			log.Debug("tesira identity fetch failed", "attr", attr, "err", err, "resp", resp)
			return ""
		}
		return parseTTPValue(resp)
	}

	partNumber := getStr("partNumber")
	software := getStr("softwareVersion")
	hostname := getStr("hostname")
	netStatus := getStr("networkStatus")

	ip, mac := parseTesiraNetworkStatus(netStatus)

	a.identityMu.Lock()
	a.partNumber = partNumber
	a.softwareVersion = software
	a.hostname = hostname
	a.ipAddress = ip
	a.macAddress = mac
	a.identityMu.Unlock()

	log.Info("tesira identity",
		"part_number", partNumber,
		"software_version", software,
		"hostname", hostname,
		"ip", ip,
		"mac", mac)
}

func (a *TesiraAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

// ── TTP subscription setup ────────────────────────────────────────────────────

// sendSubscriptions registers for push notifications from the Tesira.
// The Tesira will proactively send a message whenever the subscribed
// attribute changes — eliminating the need to poll for those values.
//
// TTP subscription syntax:
//   <InstanceTag> subscribe <Attribute> <Index> <CustomLabel> [<MinRate>]
//
// Each subscribe is issued synchronously (wait for ack) so its echo doesn't
// interleave with the next subscribe's response on the byte stream.
//
// Sources of subscription specs, in order:
//  1. cfg.Subscriptions list — preferred, supports arbitrary number of blocks.
//  2. cfg.Tags{mute,gain,call}_instance_tag — legacy single-tag form, retained
//     for backward compatibility with the original PoC config.
func (a *TesiraAdapter) sendSubscriptions(ctx context.Context) {
	log := slog.With("device", a.Cfg.ID)

	subscribe := func(label, tag, attribute string, channel, rate int) {
		if channel <= 0 {
			channel = 1
		}
		if rate <= 0 {
			rate = 500
		}
		if label == "" {
			label = fmt.Sprintf("%s_%s", tag, attribute)
		}
		cmd := fmt.Sprintf("%s subscribe %s %d %s %d", tag, attribute, channel, label, rate)
		resp, err := a.sendAndReceive(ctx, cmd, 3*time.Second)
		switch {
		case err != nil:
			log.Warn("tesira subscribe failed", "label", label, "tag", tag, "error", err)
		case strings.HasPrefix(resp, "-ERR"):
			log.Warn("tesira subscribe rejected", "label", label, "tag", tag, "resp", resp)
		default:
			log.Info("subscribed", "label", label, "tag", tag, "attribute", attribute, "channel", channel)
		}
	}

	if len(a.Cfg.Subscriptions) > 0 {
		for _, s := range a.Cfg.Subscriptions {
			if s.Tag == "" || s.Attribute == "" {
				log.Warn("tesira subscription spec missing tag or attribute", "spec", s)
				continue
			}
			// mode=poll entries are queried during Poll() rather than
			// subscribed to. Skip here; the read loop never sees them.
			if s.Mode == "poll" {
				continue
			}
			subscribe(s.Label, s.Tag, s.Attribute, s.Channel, s.Rate)
		}
		return
	}

	// Legacy fallback — single tag per attribute via tags map.
	if tag := a.Cfg.Tags["mute_instance_tag"]; tag != "" {
		subscribe("mute_state", tag, "mute", 1, 500)
	}
	if tag := a.Cfg.Tags["gain_instance_tag"]; tag != "" {
		subscribe("gain_level", tag, "level", 1, 500)
	}
	if tag := a.Cfg.Tags["call_instance_tag"]; tag != "" {
		subscribe("call_state", tag, "callState", 1, 500)
	}
}

// ── Read loop — parses TTP responses and subscription notifications ────────────

// TTP response formats:
//   +OK                        — success, no value returned
//   +OK "value"                — success with a value
//   +OK {"key":"val",...}      — success with JSON object (networkInfo etc.)
//   -ERR <code> <message>      — error response
//   ! <CustomLabel> <value>    — subscription notification (push)
func (a *TesiraAdapter) readLoop(ctx context.Context) {
	log := slog.With("device", a.Cfg.ID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.connMu.Lock()
		reader := a.reader
		a.connMu.Unlock()
		if reader == nil {
			return
		}

		_ = a.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Warn("tesira read error", "error", err)
			a.SetStatus(device.StatusOffline)
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		log.Debug("tesira rx", "line", line)

		switch {
		case strings.HasPrefix(line, "! "):
			// Subscription notification — parse and emit as event
			a.HandleSubscriptionNotification(line)

		case strings.HasPrefix(line, "+OK") || strings.HasPrefix(line, "-ERR"):
			// Command response — forward to waiting SendCommand call
			select {
			case a.respCh <- line:
			default:
				// No one waiting — store as last metric anyway
				a.stateMu.Lock()
				a.lastMetrics["last_response"] = line
				a.stateMu.Unlock()
			}

		default:
			// Informational line (banner, prompt etc.) — log and ignore
			log.Debug("tesira info line", "line", line)
		}
	}
}

// handleSubscriptionNotification parses lines like:
//
//	Old firmware:  ! mute_state true
//	               ! gain_level -10.0
//	New firmware:  ! "publishToken":"mute_state" "value":false
//	               ! "publishToken":"gain_level" "value":-30.000000
//
// Both forms collapse to a label (the publishToken / custom label) and a
// stringified value. Notifications that don't match either form are dropped.
func (a *TesiraAdapter) HandleSubscriptionNotification(line string) {
	label, value := parseSubscriptionNotification(line)
	if label == "" {
		return
	}

	// Update cached metrics
	a.stateMu.Lock()
	a.lastMetrics[label] = value
	a.stateMu.Unlock()

	// Emit as a device event so the hub forwards it to the cloud
	a.Emit(&device.Event{
		DeviceID:   a.Cfg.ID,
		DeviceName: a.Cfg.Name,
		DeviceType: a.Cfg.Type,
		EventType:  "tesira_subscription:" + label,
		Payload: map[string]any{
			"label": label,
			"value": value,
			"raw":   line,
		},
		Timestamp: time.Now().UTC(),
	})

	slog.Debug("tesira subscription update",
		"device", a.Cfg.ID,
		"label", label,
		"value", value)
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll sends a TTP probe command and merges the response with any cached
// subscription state. Subscription-based attributes (mute, gain, call state)
// are served from the cached state updated by the read loop, so the probe
// Heartbeat implements device.Heartbeater. Sends the cheapest TTP query
// on the persistent telnet session to prove the socket + session are
// still alive. Runs between polls so cold-path reconnect never surprises
// a user issuing a command.
func (a *TesiraAdapter) Heartbeat(ctx context.Context) error {
	_, err := a.sendAndReceive(ctx, "DEVICE get serialNumber", 3*time.Second)
	return err
}

// only needs to confirm the session is still alive.
func (a *TesiraAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	// Serial number as the reachability probe — same TTP round-trip
	// we've always used. If this fails, everything below is skipped
	// and we flip offline.
	resp, err := a.sendAndReceive(ctx, "DEVICE get serialNumber", 3*time.Second)

	t := a.BaseTelemetry()

	a.stateMu.RLock()
	metrics := make(map[string]any, len(a.lastMetrics))
	for k, v := range a.lastMetrics {
		metrics[k] = v
	}
	a.stateMu.RUnlock()

	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Error = err.Error()
		metrics["poll_error"] = err.Error()
		t.Metrics = metrics
		t.Status = a.Status()
		return t, nil
	}

	if strings.HasPrefix(resp, "-ERR") {
		// Device responded with a protocol error — session isn't
		// clean. Report offline; the ttp_error is on telemetry.
		a.SetStatus(device.StatusOffline)
		metrics["ttp_error"] = resp
		t.Metrics = metrics
		t.Status = a.Status()
		return t, nil
	}

	if val := parseTTPValue(resp); val != "" {
		metrics["serial_number"] = val
	}
	a.SetStatus(device.StatusOnline)

	// Re-apply cached identity fields on every telemetry payload so
	// the portal always has model / firmware / hostname / IP / MAC
	// visible, even in poll cycles where no subscription pushed
	// anything.
	a.applyIdentity(metrics)

	// Active fault list — the big health signal. Empty response
	// means all clear. On failure (older firmware, restricted role)
	// we silently omit — status probe already succeeded so we know
	// the device is talking to us.
	if resp, err := a.sendAndReceive(ctx, "DEVICE get faultList", 3*time.Second); err == nil && strings.HasPrefix(resp, "+OK") {
		raw := parseTTPValue(resp)
		count, faults := parseTesiraFaultList(raw)
		metrics["fault_count"] = count
		if len(faults) > 0 {
			metrics["faults"] = faults
		}
	}

	// DSP load percentage — only supported on some Tesira models. Best-
	// effort: -ERR "not supported" from certain firmwares is expected.
	if resp, err := a.sendAndReceive(ctx, "DEVICE get dspLoadPct", 3*time.Second); err == nil && strings.HasPrefix(resp, "+OK") {
		if v := parseTTPValue(resp); v != "" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				metrics["dsp_load_pct"] = f
			} else {
				metrics["dsp_load_pct"] = v
			}
		}
	}

	// Active preset — the currently loaded scene, if the operator has
	// wired presets. Cheap query; skips silently on unsupported firmware.
	if resp, err := a.sendAndReceive(ctx, "DEVICE get activePresetId", 3*time.Second); err == nil && strings.HasPrefix(resp, "+OK") {
		if v := parseTTPValue(resp); v != "" {
			metrics["active_preset_id"] = v
		}
	}

	// Polled subscription entries — snapshot arbitrary DSP block values
	// on each poll cycle. Complements the push subscriptions for cases
	// where the operator wants a periodic sample rather than realtime.
	a.runPolledSubscriptions(ctx, metrics)

	metrics["last_poll"] = time.Now().UTC().Format(time.RFC3339)
	t.Metrics = metrics
	t.Status = a.Status()
	return t, nil
}

// applyIdentity copies the cached identity fields into the metrics map.
// Zero-value fields are omitted so downstream doesn't see empty strings.
func (a *TesiraAdapter) applyIdentity(m map[string]any) {
	a.identityMu.RLock()
	defer a.identityMu.RUnlock()
	if a.partNumber != "" {
		m["part_number"] = a.partNumber
	}
	if a.softwareVersion != "" {
		m["software_version"] = a.softwareVersion
	}
	if a.hostname != "" {
		m["hostname"] = a.hostname
	}
	if a.ipAddress != "" {
		m["ip_address"] = a.ipAddress
	}
	if a.macAddress != "" {
		m["mac_address"] = a.macAddress
	}
}

// runPolledSubscriptions queries every mode=poll SubscriptionSpec on
// this device and merges the results into metrics under the configured
// Label. Failures per entry are logged at debug level and don't fail
// the poll — a single bad block shouldn't blank the whole payload.
func (a *TesiraAdapter) runPolledSubscriptions(ctx context.Context, metrics map[string]any) {
	for _, s := range a.Cfg.Subscriptions {
		if s.Mode != "poll" || s.Tag == "" || s.Attribute == "" {
			continue
		}
		label := s.Label
		if label == "" {
			label = fmt.Sprintf("%s_%s", s.Tag, s.Attribute)
		}
		channel := s.Channel
		if channel <= 0 {
			channel = 1
		}
		cmd := fmt.Sprintf("%s get %s %d", s.Tag, s.Attribute, channel)
		resp, err := a.sendAndReceive(ctx, cmd, 3*time.Second)
		if err != nil || !strings.HasPrefix(resp, "+OK") {
			slog.Debug("tesira polled sub failed",
				"device", a.Cfg.ID, "label", label, "err", err, "resp", resp)
			continue
		}
		if v := parseTTPValue(resp); v != "" {
			metrics[label] = v
		}
	}
}

// ── SendCommand ───────────────────────────────────────────────────────────────

// SendCommand translates a named command from config.yaml into a TTP string
// and sends it to the Tesira, then waits for the response.
//
// Named commands are looked up in config.yaml Commands map first.
// If not found, the command Name is sent as a raw TTP string — useful for
// ad-hoc commands during testing.
func (a *TesiraAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	// Resolve the TTP command string
	ttp, ok := a.Cfg.Commands[req.Name]
	if !ok {
		// Allow raw TTP strings as the command name for flexibility
		ttp = req.Name
	}

	// Replace any args placeholders — e.g. "level" arg for gain commands
	// Args can contain: level, channel, value
	for k, v := range req.Args {
		ttp = strings.ReplaceAll(ttp, "{"+k+"}", fmt.Sprintf("%v", v))
	}

	start := time.Now()
	resp, err := a.sendAndReceive(ctx, ttp, 5*time.Second)
	if err != nil {
		return nil, err
	}

	parsed := map[string]any{"ttp_response": resp}
	if strings.HasPrefix(resp, "+OK") {
		parsed["success"] = true
		if val := parseTTPValue(resp); val != "" {
			parsed["value"] = val
		}
	} else if strings.HasPrefix(resp, "-ERR") {
		parsed["success"] = false
		parsed["error"] = resp
	}

	return &device.CommandResponse{
		Raw:     resp,
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

// ── Capabilities ─────────────────────────────────────────────────────────────
//
// Tesira is unusual — the adapter accepts arbitrary TTP command strings,
// and customers bind their own names to those strings via config.yaml's
// commands: map. So there's no vendor-fixed command list to declare;
// what a device can do depends entirely on which TTP commands the
// customer has configured. Same story for metrics: they come from the
// Subscriptions block.
//
// We surface both dynamically. The routine builder gets a live view of
// what this particular Tesira instance has been wired up to do —
// exactly the shape it needs.

// Capabilities returns the config-derived capability list for this
// Tesira instance. Called once per poll by the bridge sender; runtime
// cost is a couple of map iterations, immaterial.
func (a *TesiraAdapter) Capabilities() device.Capabilities {
	caps := device.Capabilities{
		// Tesiras stay powered. There's no power-off command in the
		// TTP protocol that the adapter understands — always false.
		Power: device.PowerCapability{On: false, Off: false},
	}

	// Commands = the customer-defined names in config.yaml. Sorted
	// for stable JSON so the cloud writer's COALESCE-guarded upsert
	// doesn't churn on identical-set-different-order.
	if len(a.Cfg.Commands) > 0 {
		caps.Commands = make([]string, 0, len(a.Cfg.Commands))
		for name := range a.Cfg.Commands {
			caps.Commands = append(caps.Commands, name)
		}
		sortStrings(caps.Commands)
	}

	// Metrics = the subscription tag/attribute names the customer
	// registered. Each subscription entry pushes updates under a
	// specific label; that label is what shows up in Metrics on the
	// telemetry payload, so it's what the routine builder should
	// offer for check_metric.
	if len(a.Cfg.Subscriptions) > 0 {
		caps.Metrics = make([]string, 0, len(a.Cfg.Subscriptions))
		for _, s := range a.Cfg.Subscriptions {
			if s.Label != "" {
				caps.Metrics = append(caps.Metrics, s.Label)
			}
		}
		sortStrings(caps.Metrics)
	}

	return caps
}

// sortStrings — tiny in-package helper. Could import sort.Strings but
// a) avoids adding an import for one-line use, b) makes intent
// explicit that we're sorting for JSON stability.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// sendAndReceive writes a TTP command and blocks until a response is received
// or the timeout elapses. TTP is strictly sequential — one command at a time.
func (a *TesiraAdapter) sendAndReceive(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()

	if conn == nil {
		return "", fmt.Errorf("device %s not connected", a.Cfg.ID)
	}

	// Drain any stale response
	select {
	case <-a.respCh:
	default:
	}

	if err := a.writeLine(cmd); err != nil {
		a.SetStatus(device.StatusOffline)
		return "", fmt.Errorf("tesira write: %w", err)
	}

	select {
	case resp := <-a.respCh:
		return resp, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("tesira timeout waiting for response to: %q", cmd)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// parseSubscriptionNotification extracts the (label, value) pair from a TTP
// "! ..." subscription notification, supporting both the legacy positional
// form and the newer key/value form emitted by current Tesira firmware.
func parseSubscriptionNotification(line string) (label, value string) {
	content := strings.TrimSpace(strings.TrimPrefix(line, "!"))
	if content == "" {
		return "", ""
	}

	// Newer firmware: ! "publishToken":"<label>" "value":<value>
	if strings.Contains(content, "\"publishToken\":") {
		label = extractQuotedField(content, "publishToken")
		value = extractValueField(content)
		return label, value
	}

	// Legacy firmware: ! <label> <value>
	parts := strings.SplitN(content, " ", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], strings.Trim(parts[1], "\"")
}

// extractQuotedField returns the string contents of `"key":"<contents>"` from
// content. Returns "" if the key isn't present or the value isn't quoted.
func extractQuotedField(content, key string) string {
	needle := "\"" + key + "\":"
	i := strings.Index(content, needle)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(content[i+len(needle):])
	if !strings.HasPrefix(rest, "\"") {
		return ""
	}
	end := strings.Index(rest[1:], "\"")
	if end < 0 {
		return ""
	}
	return rest[1 : 1+end]
}

// extractValueField returns the "value" field from a subscription notification,
// stringified. Handles quoted strings, bare numbers, and bare booleans.
func extractValueField(content string) string {
	needle := "\"value\":"
	i := strings.Index(content, needle)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(content[i+len(needle):])
	if rest == "" {
		return ""
	}
	if strings.HasPrefix(rest, "\"") {
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	}
	// Bare token — take up to the next whitespace.
	end := strings.IndexAny(rest, " \t\r\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// parseTTPValue extracts the value from a TTP +OK response. Tesira firmware
// versions return values in three different shapes:
//
//	+OK "05008305"               (older firmware — bare quoted value)
//	+OK "value":"05008305"       (newer firmware — single-attribute key/value)
//	+OK {"hostname":"foo",...}   (JSON object — multi-attribute results)
//
// JSON objects are returned intact for the caller to unmarshal. Bare and
// key/value forms collapse to just the value string.
func parseTTPValue(resp string) string {
	val := strings.TrimSpace(strings.TrimPrefix(resp, "+OK"))
	if strings.HasPrefix(val, "{") {
		return val
	}
	if idx := strings.Index(val, ":"); idx > 0 && strings.HasSuffix(val[:idx], "\"") {
		val = strings.TrimSpace(val[idx+1:])
	}
	return strings.Trim(val, "\"")
}

// parseTesiraNetworkStatus extracts the primary IP and MAC from a
// Tesira `networkStatus` TTP response. The response shape varies by
// firmware — this function walks a decoded JSON tree defensively and
// returns the first non-empty (address, macAddress) pair it finds.
// Returns empty strings if nothing parseable is present.
//
// Common shapes observed in the field:
//
//	{"interfaces":[{"interfaceName":"Control","addresses":[
//	    {"address":"192.168.1.100","macAddress":"AA:BB:CC:DD:EE:FF"}]}]}
//	{"interfaces":[{"interfaceName":"Control",
//	    "networkAddresses":[{"address":"192.168.1.100"}],
//	    "macAddress":"AA:BB:CC:DD:EE:FF"}]}
func parseTesiraNetworkStatus(raw string) (ip, mac string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return "", ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", ""
	}
	// Recursive walk collecting the first address + macAddress pair
	// we encounter — Tesira firmware nests these under interfaces /
	// addresses / networkAddresses inconsistently between versions.
	var walk func(any)
	walk = func(node any) {
		if ip != "" && mac != "" {
			return
		}
		switch x := node.(type) {
		case map[string]any:
			if ip == "" {
				if s, ok := x["address"].(string); ok && s != "" && looksLikeIP(s) {
					ip = s
				}
			}
			if mac == "" {
				if s, ok := x["macAddress"].(string); ok && s != "" {
					mac = s
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	return ip, mac
}

// looksLikeIP is a lightweight sanity check so we don't accidentally
// pick up a MAC or gateway string as the primary IP. Accepts IPv4;
// IPv6 is uncommon on Tesira control interfaces and can be added if
// needed.
func looksLikeIP(s string) bool {
	if net.ParseIP(s) == nil {
		return false
	}
	// Filter out the unroutable 0.0.0.0 default and multicast — a
	// well-configured Tesira should never report either as its own IP.
	if s == "0.0.0.0" || strings.HasPrefix(s, "224.") {
		return false
	}
	return true
}

// parseTesiraFaultList decodes the JSON array Tesira returns for
// `DEVICE get faultList` and returns (count, faults). The faults value
// is passed through as the parsed slice so the cloud can store it as
// jsonb for querying / rendering later. On unparseable input returns
// (0, nil) — a fault list that we can't interpret shouldn't block the
// rest of the poll.
func parseTesiraFaultList(raw string) (int, []any) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return 0, nil
	}
	if !strings.HasPrefix(raw, "[") {
		return 0, nil
	}
	var faults []any
	if err := json.Unmarshal([]byte(raw), &faults); err != nil {
		return 0, nil
	}
	return len(faults), faults
}

func (a *TesiraAdapter) writeLine(s string) error {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("not connected")
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := fmt.Fprint(a.conn, s)
	return err
}
