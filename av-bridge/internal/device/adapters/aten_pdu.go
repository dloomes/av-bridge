package adapters

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// ATENPDUAdapter talks to ATEN eco PDUs over their Telnet CLI (default port
// 23). Written against the PE6108G (8 x C13 outlets, per-outlet metering); the
// same CLI grammar covers most of the PE6/PE7/PE8 range, so extending to more
// models is a matter of overriding tags.outlet_count in config.
//
// Login flow: ATEN prompts "Login:" then "Password:" — plain text, single line
// each. After successful login the device sits at a `>` prompt awaiting
// commands. There's no per-command reply terminator we can rely on across
// firmwares, so we read until we see the prompt or a short idle window
// elapses.
//
// Commands we use (from ATEN eco PDU CLI reference):
//
//	read status o<N> simple            — outlet on/off state
//	read meter outlet o<N> simple      — per-outlet current + power
//	read meter dev volt simple         — device-level voltage
//	read meter dev power simple        — device-level power draw
//	sw o<N> on immediate               — power outlet on
//	sw o<N> off immediate              — power outlet off
//	sw o<N> reboot immediate           — power cycle outlet
//
// Parsing is regex-based and forgiving: firmware quirks (extra banners,
// trailing spaces, punctuation drift) degrade a metric rather than fail the
// poll. Raw lines are logged at debug so field-tuning against a live device is
// straightforward.
type ATENPDUAdapter struct {
	device.Base
	address     string
	outletCount int

	connMu sync.Mutex
	conn   net.Conn
	reader *bufio.Reader

	// cmdMu serialises the CLI. The ATEN accepts one command at a time; a
	// concurrent Poll + SendCommand would interleave read bytes.
	cmdMu sync.Mutex
}

const (
	atenPDUDefaultPort    = 23
	atenPDUDefaultOutlets = 8
	atenPDUReadTimeout    = 800 * time.Millisecond
)

func NewATENPDUAdapter(cfg config.DeviceConfig) *ATENPDUAdapter {
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, atenPDUDefaultPort)
	}

	count := atenPDUDefaultOutlets
	if v := cfg.Tags["outlet_count"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 32 {
			count = n
		}
	}

	return &ATENPDUAdapter{
		Base:        device.NewBase(cfg),
		address:     addr,
		outletCount: count,
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *ATENPDUAdapter) Connect(ctx context.Context) error {
	log := slog.With("device", a.Cfg.ID, "address", a.address)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	log.Info("aten_pdu tcp connected")

	reader := bufio.NewReader(newTelnetFilter(conn))

	// Drain the initial banner up to the "Login:" prompt.
	if err := waitFor(conn, reader, []string{"login:", "username:"}, 5*time.Second); err != nil {
		conn.Close()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu: waiting for login prompt: %w", err)
	}
	if err := writeCRLF(conn, a.Cfg.Username); err != nil {
		conn.Close()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu: send username: %w", err)
	}

	if err := waitFor(conn, reader, []string{"password:"}, 5*time.Second); err != nil {
		conn.Close()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu: waiting for password prompt: %w", err)
	}
	if err := writeCRLF(conn, a.Cfg.Password); err != nil {
		conn.Close()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu: send password: %w", err)
	}

	// Drain whatever the device prints after login (welcome text, menu, prompt)
	// with a short settle window. We don't rely on a specific banner — just
	// let the wire go quiet so the first command's response starts clean.
	drainQuiet(conn, reader, 800*time.Millisecond)

	a.connMu.Lock()
	a.conn = conn
	a.reader = reader
	a.connMu.Unlock()

	a.SetStatus(device.StatusOnline)
	log.Info("aten_pdu connected", "outlets", a.outletCount)
	return nil
}

func (a *ATENPDUAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.conn == nil {
		return nil
	}
	err := a.conn.Close()
	a.conn = nil
	a.reader = nil
	return err
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll samples per-outlet state + power draw and device-level voltage and
// total power. Each query is best-effort: a parse miss on one outlet leaves
// that metric absent rather than failing the whole poll.
func (a *ATENPDUAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	metrics := map[string]any{}
	start := time.Now()

	for i := 1; i <= a.outletCount; i++ {
		outlet := fmt.Sprintf("o%02d", i)

		if resp, err := a.exec(ctx, "read status "+outlet+" simple"); err == nil {
			if state := parseOutletState(resp); state != "" {
				metrics[fmt.Sprintf("outlet_%d_state", i)] = state
			}
		}
		if resp, err := a.exec(ctx, "read meter outlet "+outlet+" simple"); err == nil {
			if amps, ok := parseNumericMetric(resp, `(?i)current[^0-9\-]*([\-0-9.]+)`); ok {
				metrics[fmt.Sprintf("outlet_%d_current_a", i)] = amps
			}
			if watts, ok := parseNumericMetric(resp, `(?i)power[^0-9\-]*([\-0-9.]+)`); ok {
				metrics[fmt.Sprintf("outlet_%d_power_w", i)] = watts
			}
		}

		// Outlet name — user-configured label on the PDU's web UI (e.g. "Codec",
		// "Display"). YAML tag override wins if set, so operators can rename
		// without touching the device. If neither is available the name simply
		// doesn't populate.
		nameKey := fmt.Sprintf("outlet_%d_name", i)
		if override := a.Cfg.Tags[nameKey]; override != "" {
			metrics[nameKey] = override
		} else if resp, err := a.exec(ctx, "read status "+outlet+" name"); err == nil {
			if name := parseOutletName(resp, outlet); name != "" {
				metrics[nameKey] = name
			}
		}
	}

	if resp, err := a.exec(ctx, "read meter dev volt simple"); err == nil {
		if v, ok := parseNumericMetric(resp, `(?i)volt[^0-9\-]*([\-0-9.]+)`); ok {
			metrics["voltage_v"] = v
		}
	}
	if resp, err := a.exec(ctx, "read meter dev power simple"); err == nil {
		if w, ok := parseNumericMetric(resp, `(?i)power[^0-9\-]*([\-0-9.]+)`); ok {
			metrics["total_power_w"] = w
		}
	}

	metrics["response_ms"] = time.Since(start).Milliseconds()
	metrics["outlet_count"] = a.outletCount
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// ── SendCommand ───────────────────────────────────────────────────────────────

// SendCommand handles the outlet control verbs (outlet_on / outlet_off /
// outlet_reboot). The outlet index arrives as arg "outlet" (1-based). Named
// commands from config.yaml take precedence for site-specific overrides;
// otherwise the request name is looked up in the built-in verb map, and
// finally passed through as a raw CLI string.
func (a *ATENPDUAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	raw, err := a.resolveCommand(req)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	resp, err := a.exec(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("aten_pdu command %q: %w", req.Name, err)
	}

	parsed := map[string]any{"raw": resp}
	if isATENError(resp) {
		parsed["success"] = false
		return &device.CommandResponse{Raw: resp, Parsed: parsed, Latency: time.Since(start)},
			fmt.Errorf("aten_pdu: device rejected command: %s", firstLine(resp))
	}
	parsed["success"] = true
	return &device.CommandResponse{
		Raw:     resp,
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

func (a *ATENPDUAdapter) resolveCommand(req device.CommandRequest) (string, error) {
	// Config-declared commands take precedence — this is the escape hatch
	// for site-specific verbs (delay_on, outlet grouping, schedules, etc.).
	if raw, ok := a.Cfg.Commands[req.Name]; ok {
		for k, v := range req.Args {
			raw = strings.ReplaceAll(raw, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		return raw, nil
	}

	switch req.Name {
	case "outlet_on", "outlet_off", "outlet_reboot":
		outlet, err := a.outletArg(req)
		if err != nil {
			return "", err
		}
		verb := map[string]string{
			"outlet_on":     "on",
			"outlet_off":    "off",
			"outlet_reboot": "reboot",
		}[req.Name]
		return fmt.Sprintf("sw %s %s immediate", outlet, verb), nil
	}

	// Raw passthrough — accept if it looks like an ATEN CLI verb.
	parts := strings.SplitN(req.Name, " ", 2)
	switch parts[0] {
	case "sw", "read", "config", "reset", "show":
		return req.Name, nil
	}
	return "", fmt.Errorf("aten_pdu: unknown command %q — expected outlet_on|outlet_off|outlet_reboot with outlet arg, a raw ATEN CLI string, or a commands: override", req.Name)
}

func (a *ATENPDUAdapter) outletArg(req device.CommandRequest) (string, error) {
	v, ok := req.Args["outlet"]
	if !ok {
		return "", fmt.Errorf("aten_pdu: command %q requires arg %q (outlet number 1..%d)", req.Name, "outlet", a.outletCount)
	}
	var n int
	switch t := v.(type) {
	case int:
		n = t
	case int64:
		n = int(t)
	case float64:
		n = int(t)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return "", fmt.Errorf("aten_pdu: outlet %q is not a number", t)
		}
		n = parsed
	default:
		return "", fmt.Errorf("aten_pdu: outlet arg type %T not supported", v)
	}
	if n < 1 || n > a.outletCount {
		return "", fmt.Errorf("aten_pdu: outlet %d out of range 1..%d", n, a.outletCount)
	}
	return fmt.Sprintf("o%02d", n), nil
}

// ── Capabilities ─────────────────────────────────────────────────────────────

func (a *ATENPDUAdapter) Capabilities() device.Capabilities {
	metrics := []string{"voltage_v", "total_power_w", "outlet_count", "response_ms"}
	for i := 1; i <= a.outletCount; i++ {
		metrics = append(metrics,
			fmt.Sprintf("outlet_%d_state", i),
			fmt.Sprintf("outlet_%d_current_a", i),
			fmt.Sprintf("outlet_%d_power_w", i),
			fmt.Sprintf("outlet_%d_name", i),
		)
	}
	commands := []string{"outlet_on", "outlet_off", "outlet_reboot"}
	// Merge any config-declared commands so the routine builder sees them too.
	for name := range a.Cfg.Commands {
		commands = append(commands, name)
	}
	sortStrings(commands)
	sortStrings(metrics)

	return device.Capabilities{
		// The PDU itself has no power state — it's always powered. Individual
		// outlets are controlled via commands, not the power capability.
		Power:    device.PowerCapability{On: false, Off: false},
		Commands: commands,
		Metrics:  metrics,
	}
}

// ── Transport ────────────────────────────────────────────────────────────────

// exec writes one CLI command and reads the reply. Serialised via cmdMu so
// concurrent Poll + SendCommand don't share the wire.
func (a *ATENPDUAdapter) exec(ctx context.Context, cmd string) (string, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn := a.conn
	reader := a.reader
	a.connMu.Unlock()
	if conn == nil || reader == nil {
		return "", fmt.Errorf("aten_pdu: not connected")
	}

	if err := writeCRLF(conn, cmd); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}

	timeout := atenPDUReadTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if r := time.Until(deadline); r > 0 && r < timeout {
			timeout = r
		}
	}

	// Read until either the prompt reappears or the wire goes quiet.
	// ATEN's per-command output length is small, so a short window is fine.
	var buf strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
			return buf.String(), err
		}
		line, err := reader.ReadString('\n')
		if line != "" {
			buf.WriteString(line)
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// If we've collected anything at all, treat idle as end-of-reply.
				if buf.Len() > 0 {
					_ = conn.SetReadDeadline(time.Time{})
					return strings.TrimSpace(buf.String()), nil
				}
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			return buf.String(), err
		}
		if isATENPrompt(line) {
			_ = conn.SetReadDeadline(time.Time{})
			return strings.TrimSpace(buf.String()), nil
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	if buf.Len() == 0 {
		return "", fmt.Errorf("aten_pdu: no response within %s", timeout)
	}
	return strings.TrimSpace(buf.String()), nil
}

// ── Parsing helpers ──────────────────────────────────────────────────────────

var (
	outletStateOnRE  = regexp.MustCompile(`(?i)\bon\b`)
	outletStateOffRE = regexp.MustCompile(`(?i)\boff\b`)
	atenPromptRE     = regexp.MustCompile(`^[^\s]*>\s*$`)
	atenErrorRE      = regexp.MustCompile(`(?i)(invalid|unknown|error|fail|denied)`)
)

func parseOutletState(resp string) string {
	// Expected shape (varies by firmware):
	//   "Outlet 01: on"     or      "o01 status: On"
	// We just want the on/off. If both markers appear, prefer explicit "off"
	// since some banners include the word "on" incidentally.
	if outletStateOffRE.MatchString(resp) {
		return "off"
	}
	if outletStateOnRE.MatchString(resp) {
		return "on"
	}
	return ""
}

// parseOutletName pulls the outlet's user-configured name out of the ATEN
// `read status oNN name` response. Firmwares vary on the exact wording, so
// we try a few shapes:
//
//	"o01 name: Codec"
//	"Outlet 01 name: Codec"
//	"name: Codec"
//	"Codec"                     — some firmwares just print the raw value
//
// Command echo and empty markers ("N/A", "--", the outlet id repeated back)
// are stripped so we don't surface junk as a label.
func parseOutletName(resp, outletID string) string {
	// Drop any line that just echoes the command back to us, or is our prompt.
	var candidate string
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "read status") {
			continue
		}
		if isATENPrompt(line + "\n") {
			continue
		}
		candidate = line
	}
	if candidate == "" {
		return ""
	}

	// Prefer explicit "name:" if present.
	if m := outletNameRE.FindStringSubmatch(candidate); len(m) == 2 {
		candidate = m[1]
	}

	candidate = strings.TrimSpace(candidate)
	// Reject sentinel values and the outlet id echoed back on its own.
	switch strings.ToLower(candidate) {
	case "", "n/a", "na", "--", "none", outletID:
		return ""
	}
	return candidate
}

var outletNameRE = regexp.MustCompile(`(?i)name\s*[:=]\s*(.+)$`)

func parseNumericMetric(resp, pattern string) (float64, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, false
	}
	m := re.FindStringSubmatch(resp)
	if len(m) < 2 {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func isATENPrompt(line string) bool {
	trimmed := strings.TrimRight(line, "\r\n \t")
	return atenPromptRE.MatchString(trimmed) || trimmed == ">" || strings.HasSuffix(trimmed, ">")
}

func isATENError(resp string) bool {
	return atenErrorRE.MatchString(resp)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// ── Small connection utilities ───────────────────────────────────────────────

// waitFor reads lines until one contains any of the given lowercase needles,
// or the deadline elapses. Case-insensitive substring match.
func waitFor(conn net.Conn, reader *bufio.Reader, needles []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var seen strings.Builder
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return err
		}
		b, err := reader.ReadByte()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				if matchesAny(seen.String(), needles) {
					_ = conn.SetReadDeadline(time.Time{})
					return nil
				}
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			return err
		}
		seen.WriteByte(b)
		if matchesAny(seen.String(), needles) {
			_ = conn.SetReadDeadline(time.Time{})
			return nil
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	return fmt.Errorf("timeout waiting for %v (saw %q)", needles, seen.String())
}

func matchesAny(s string, needles []string) bool {
	lower := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// drainQuiet reads and discards anything received within window (used after
// login to swallow banners / menus before the first real command).
func drainQuiet(conn net.Conn, reader *bufio.Reader, window time.Duration) {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
			return
		}
		if _, err := reader.ReadByte(); err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			return
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
}

func writeCRLF(conn net.Conn, s string) error {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := fmt.Fprint(conn, s)
	return err
}
