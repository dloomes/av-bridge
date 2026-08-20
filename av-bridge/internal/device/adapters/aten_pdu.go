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
// Session model: **one command per Telnet session**. Observed on real PE6108G
// firmware, the device closes the socket immediately after emitting a
// command's response, so any attempt to keep a long-lived session for
// pipelining fails on the second write. Every exec() opens a fresh socket,
// runs the login handshake, sends the command, reads the reply, and closes.
// It's ~250ms per command over LAN but eliminates the whole class of
// broken-pipe / EOF failures the persistent-session approach was fighting.
//
// Login flow: ATEN prompts "Login:" then "Password:" — plain text, single line
// each. After successful login the device sits at a `>` prompt awaiting a
// single command.
//
// Commands we use (from ATEN eco PDU CLI reference):
//
//	read status o<N> simple            — outlet on/off state
//	read status o<N> name              — user-configured outlet label
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

	// cmdMu serialises exec calls so we don't open two Telnet sessions to
	// the same PDU at once — some ATEN firmwares cap concurrent sessions.
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
//
// The adapter holds no persistent Telnet session (see the type doc). Connect
// is a one-shot reachability probe — it opens a session, runs the login
// handshake, sends a cheap read command, and closes; success flips the device
// online, failure flips it offline. Disconnect is a no-op.

func (a *ATENPDUAdapter) Connect(ctx context.Context) error {
	if _, err := a.exec(ctx, "read dev info"); err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aten_pdu connect probe %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("aten_pdu ready", "device", a.Cfg.ID, "address", a.address, "outlets", a.outletCount)
	return nil
}

func (a *ATENPDUAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
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

	// Surface the raw device reply at INFO so operators can see how the PDU
	// actually reacted (helps diagnose "success but nothing changed" cases
	// where the CLI accepted the input but the outlet didn't switch).
	slog.Info("aten_pdu command response",
		"device", a.Cfg.ID, "command", req.Name, "raw_cli", raw, "response", resp)

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

// exec runs one CLI command against the PDU in its own Telnet session and
// returns the response text. The full lifecycle happens per call: dial,
// login, write, read, close. Serialised via cmdMu so concurrent Poll +
// SendCommand don't open two sessions to the PDU simultaneously (some ATEN
// firmwares cap concurrent sessions and reject the second).
func (a *ATENPDUAdapter) exec(ctx context.Context, cmd string) (string, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	conn, reader, err := a.dialAndLogin(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := writeCRLF(conn, cmd); err != nil {
		return "", fmt.Errorf("aten_pdu write %q: %w", cmd, err)
	}

	timeout := atenPDUReadTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if r := time.Until(deadline); r > 0 && r < timeout {
			timeout = r
		}
	}

	// Read until the wire goes quiet or the socket closes (ATEN closes it
	// itself after emitting the response — that's an end-of-reply signal).
	// The prompt check catches firmwares that don't drop the session.
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
				if buf.Len() > 0 {
					return strings.TrimSpace(buf.String()), nil
				}
				continue
			}
			// EOF / broken pipe here is normal — ATEN just told us it's done.
			return strings.TrimSpace(buf.String()), nil
		}
		if isATENPrompt(line) {
			return strings.TrimSpace(buf.String()), nil
		}
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("aten_pdu: no response within %s", timeout)
	}
	return strings.TrimSpace(buf.String()), nil
}

// dialAndLogin opens a fresh Telnet session and runs the login handshake.
// The returned conn is the caller's to close.
//
// Login must complete quickly — the PDU has an aggressive "Login timeout"
// on the initial prompt (a few seconds). We wait for the buffer to END with
// a known prompt (not just contain one somewhere) so a banner mentioning
// the word "login" doesn't cause us to send our username before the real
// prompt has appeared.
func (a *ATENPDUAdapter) dialAndLogin(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	log := slog.With("device", a.Cfg.ID, "address", a.address)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(dialCtx, "tcp", a.address)
	if err != nil {
		return nil, nil, fmt.Errorf("aten_pdu dial %s: %w", a.address, err)
	}
	reader := bufio.NewReader(newTelnetFilter(conn))

	banner, err := readUntilPrompt(conn, reader, []string{"login:", "username:"}, 5*time.Second)
	log.Info("aten_pdu login banner", "banner", collapse(banner))
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("aten_pdu: login prompt: %w (saw %q)", err, collapse(banner))
	}
	if err := writeCRLF(conn, a.Cfg.Username); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("aten_pdu: send username: %w", err)
	}

	pwPrompt, err := readUntilPrompt(conn, reader, []string{"password:"}, 5*time.Second)
	log.Info("aten_pdu after username", "text", collapse(pwPrompt))
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("aten_pdu: password prompt: %w (saw %q)", err, collapse(pwPrompt))
	}
	if err := writeCRLF(conn, a.Cfg.Password); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("aten_pdu: send password: %w", err)
	}

	// Read whatever the PDU prints after login — welcome banner, main menu,
	// command prompt — so we know we've reached a stable state before firing
	// the real command. Logged so we can spot menu shells that need an
	// extra keystroke to enter command mode.
	postLogin := drainAndReturn(conn, reader, 500*time.Millisecond)
	log.Info("aten_pdu post-login", "text", collapse(postLogin))
	return conn, reader, nil
}

// collapse turns a possibly multi-line captured buffer into a single-line
// visible string suitable for logging. Newlines/tabs → spaces, control
// bytes → dot, trims excess whitespace.
func collapse(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 0x20:
			b.WriteByte('.')
			prevSpace = false
		default:
			b.WriteRune(r)
			prevSpace = r == ' '
		}
	}
	return strings.TrimSpace(b.String())
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

// readUntilPrompt reads bytes until the accumulated buffer (trimmed of
// trailing whitespace / control bytes) ENDS with one of the given
// lowercase needles, or the deadline elapses. Returns everything seen
// (including trailing prompt) so callers can log or attach it to errors.
//
// End-of-buffer matching, not substring, is deliberate: banner text often
// mentions "login" or "password" ahead of the actual prompt, and matching
// early causes us to send credentials into the void before the PDU is
// ready — the PDU then times the login out and closes the socket, and
// every subsequent operation returns empty.
func readUntilPrompt(conn net.Conn, reader *bufio.Reader, needles []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var seen strings.Builder
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return seen.String(), err
		}
		b, err := reader.ReadByte()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				if endsWithAny(seen.String(), needles) {
					_ = conn.SetReadDeadline(time.Time{})
					return seen.String(), nil
				}
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			return seen.String(), err
		}
		seen.WriteByte(b)
		if endsWithAny(seen.String(), needles) {
			_ = conn.SetReadDeadline(time.Time{})
			return seen.String(), nil
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	return seen.String(), fmt.Errorf("timeout waiting for %v", needles)
}

// endsWithAny reports whether the trimmed lowercase form of s ends with
// any of the given (already-lowercase) needles.
func endsWithAny(s string, needles []string) bool {
	tail := strings.ToLower(strings.TrimRight(s, " \t\r\n\x00"))
	for _, n := range needles {
		if strings.HasSuffix(tail, n) {
			return true
		}
	}
	return false
}

// drainAndReturn reads whatever the PDU sends over the given window and
// returns it. Used after login to capture menu / prompt output so operators
// can see what the device actually presents.
func drainAndReturn(conn net.Conn, reader *bufio.Reader, window time.Duration) string {
	deadline := time.Now().Add(window)
	var buf strings.Builder
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return buf.String()
		}
		b, err := reader.ReadByte()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			return buf.String()
		}
		buf.WriteByte(b)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return buf.String()
}

func writeCRLF(conn net.Conn, s string) error {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := fmt.Fprint(conn, s)
	return err
}
