package adapters

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// NetgearM4250Adapter manages NETGEAR M4250 (AV Line) fully managed switches
// over the switch CLI on Telnet port 23, which is enabled by default. Written
// against the M4250/M4350 CLI Command Reference Manual; the M4350 shares the
// grammar, with unit/slot/port interface names (set tags.port_prefix).
//
// Session model: one Telnet session per poll or command. The switch caps
// concurrent management sessions and times idle ones out, so holding one open
// buys nothing. Each session logs in at "User:" / "Password:", runs `enable`
// if it lands at the ">" User EXEC prompt, runs its commands, then closes.
//
// Paging: output longer than a screen stops at "--More-- or (q)uit". We
// answer with a space (next page) rather than running `length 0`, which on
// the M4250 is a persistent Line Config change.
//
// Poll reads (all Privileged EXEC):
//
//	show version            model, serial, MAC, firmware
//	show sysinfo            system name / location, uptime
//	show environment        temperature, fans, power supplies
//	show poe                PoE budget and consumption
//	show poe port info all  per-port PoE status, class, power, faults
//	show port all           per-port admin state, link and speed
//
// PoE commands on a non-PoE model fail and the metrics are simply omitted.
// Per-port changes (poe_on/off, port_enable/disable) apply to the running
// config only: they're not saved, so a switch reboot restores the saved
// state. That's deliberate — a remote power-cycle of a panel shouldn't
// quietly rewrite the switch's configuration.
type NetgearM4250Adapter struct {
	device.Base
	address    string
	portPrefix string

	// sessMu serialises CLI sessions so a command and a poll never log in
	// at the same time.
	sessMu sync.Mutex

	// ports holds the physical port keys seen at the last poll, so
	// Capabilities can list the per-port metrics this switch really has.
	portsMu sync.RWMutex
	ports   []string
}

const (
	netgearDefaultPort    = 23
	netgearLoginTimeout   = 8 * time.Second
	netgearCommandTimeout = 20 * time.Second
	netgearQuiet          = 250 * time.Millisecond
)

func NewNetgearM4250Adapter(cfg config.DeviceConfig) *NetgearM4250Adapter {
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, netgearDefaultPort)
	}
	prefix := "0/"
	if p := strings.TrimSpace(cfg.Tags["port_prefix"]); p != "" {
		prefix = strings.TrimSuffix(p, "/") + "/"
	}
	return &NetgearM4250Adapter{
		Base:       device.NewBase(cfg),
		address:    addr,
		portPrefix: prefix,
	}
}

// ── Connection ───────────────────────────────────────────────────────────

// Connect proves the address and credentials by logging in and reading
// `show version`. No session is kept (see the type doc).
func (a *NetgearM4250Adapter) Connect(ctx context.Context) error {
	var out string
	err := a.withSession(ctx, func(s *netgearSession) error {
		var err error
		out, err = s.run("show version", nil)
		return err
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("netgear_m4250 connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	kv := netgearKV(out)
	a.SetStatus(device.StatusOnline)
	slog.Info("netgear_m4250 connected", "device", a.Cfg.ID, "address", a.address,
		"model", kv["machine model"], "firmware", kv["software version"])
	return nil
}

func (a *NetgearM4250Adapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

func (a *NetgearM4250Adapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()

	outputs := map[string]string{}
	cmds := []string{"show version", "show sysinfo", "show environment", "show poe", "show poe port info all", "show port all"}
	err := a.withSession(ctx, func(s *netgearSession) error {
		for _, c := range cmds {
			out, err := s.run(c, nil)
			if err != nil {
				return err
			}
			if !netgearIsError(out) {
				outputs[c] = out
			}
		}
		return nil
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		return t, nil
	}

	metrics, ports := netgearMetrics(outputs)
	metrics["response_ms"] = time.Since(start).Milliseconds()

	a.portsMu.Lock()
	a.ports = ports
	a.portsMu.Unlock()

	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// netgearMetrics maps the poll outputs onto telemetry and returns the
// physical port keys it found. Anything missing is omitted, not zeroed.
func netgearMetrics(out map[string]string) (map[string]any, []string) {
	m := map[string]any{}
	set := func(key, val string) {
		if val != "" {
			m[key] = val
		}
	}
	faults := []any{}
	fault := func(level, typ, desc string) {
		faults = append(faults, map[string]any{"level": level, "type": typ, "description": desc})
	}

	// Identity.
	v := netgearKV(out["show version"])
	set("model", v["machine model"])
	set("system_description", v["system description"])
	set("serial_number", v["serial number"])
	set("mac_address", v["burned in mac address"])
	set("software_version", v["software version"])
	set("boot_version", v["boot code version"])

	si := netgearKV(out["show sysinfo"])
	set("system_name", si["system name"])
	set("system_location", si["system location"])
	if up, ok := netgearUptime(si["system up time"]); ok {
		m["uptime_s"] = up
	}

	// Environment.
	env := netgearEnvironment(out["show environment"])
	if env.tempOK {
		m["temperature_c"] = env.temp
	}
	if env.fans > 0 {
		m["fan_count"] = env.fans
	}
	if env.psus > 0 {
		m["psu_count"] = env.psus
	}
	for _, f := range env.faults {
		fault(f[0], f[1], f[2])
	}

	// PoE budget.
	if poe, ok := out["show poe"]; ok {
		p := netgearKV(poe)
		if s := p["pse main operational status"]; s != "" {
			m["poe_status"] = strings.ToLower(s)
			if strings.EqualFold(s, "FAULTY") {
				fault("Error", "PoE", "PoE controller reports FAULTY")
			}
		}
		if w, ok := netgearWatts(p["total power available"]); ok {
			m["poe_budget_w"] = w
		}
		if w, ok := netgearWatts(p["total power consumed"]); ok {
			m["poe_consumed_w"] = w
		}
		if w, ok := netgearWatts(p["threshold power"]); ok {
			m["poe_threshold_w"] = w
		}
	}

	// Per-port PoE.
	delivering := 0
	for _, pp := range netgearPoEPorts(out["show poe port info all"]) {
		k := netgearPortKey(pp.intf)
		m["port_"+k+"_poe_status"] = strings.ToLower(pp.status)
		m["port_"+k+"_poe_power_w"] = float64(pp.powerMW) / 1000
		if pp.class != "" && !strings.EqualFold(pp.class, "Unknown") {
			m["port_"+k+"_poe_class"] = pp.class
		}
		if strings.EqualFold(pp.status, "Delivering Power") {
			delivering++
		}
		if pp.fault != "" && !strings.EqualFold(pp.fault, "No Error") {
			fault("Warning", "PoE", fmt.Sprintf("Port %s: %s", pp.intf, pp.fault))
		}
	}
	if _, ok := out["show poe port info all"]; ok {
		m["poe_ports_delivering"] = delivering
	}

	// Link state.
	var ports []string
	up := 0
	for _, p := range netgearPorts(out["show port all"]) {
		k := netgearPortKey(p.intf)
		ports = append(ports, k)
		m["port_"+k+"_link"] = p.link
		m["port_"+k+"_admin"] = p.admin
		if p.speed != "" {
			m["port_"+k+"_speed"] = p.speed
		}
		if p.link == "up" {
			up++
		}
	}
	if len(ports) > 0 {
		m["ports_total"] = len(ports)
		m["ports_up"] = up
	}

	m["fault_count"] = len(faults)
	if len(faults) > 0 {
		m["faults"] = faults
	}
	return m, ports
}

// ── Commands ─────────────────────────────────────────────────────────────

// SendCommand runs one of:
//
//	reboot         reload (unsaved changes are not saved)
//	poe_reset      interface <port> / poe reset — power-cycles the PoE port
//	poe_on         interface <port> / poe
//	poe_off        interface <port> / no poe
//	port_enable    interface <port> / no shutdown
//	port_disable   interface <port> / shutdown
//
// Per-port commands take arg "port": a port number ("5", expanded with
// tags.port_prefix, default "0/") or a full interface name ("0/5").
// A commands: entry in config wins over the built-ins; use ";" to separate
// lines. Any "show …" command is passed through as a read-only query.
func (a *NetgearM4250Adapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	lines, answer, err := a.resolveCommand(req)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	var out strings.Builder
	err = a.withSession(ctx, func(s *netgearSession) error {
		for _, l := range lines {
			resp, err := s.run(l, answer)
			out.WriteString(resp)
			out.WriteString("\n")
			if err != nil {
				return err
			}
			if netgearIsError(resp) {
				return fmt.Errorf("switch rejected %q: %s", l, firstLine(netgearErrorLine(resp)))
			}
		}
		return nil
	})
	// reload drops the session once confirmed; that's success, not failure.
	if err != nil && req.Name == "reboot" && errors.Is(err, errNetgearClosed) {
		err = nil
	}
	raw := strings.TrimSpace(out.String())
	if err != nil {
		return &device.CommandResponse{Raw: raw, Parsed: map[string]any{"success": false}, Latency: time.Since(start)},
			fmt.Errorf("netgear_m4250 command %q: %w", req.Name, err)
	}
	slog.Info("netgear_m4250 command", "device", a.Cfg.ID, "command", req.Name, "lines", lines)
	return &device.CommandResponse{
		Raw:     raw,
		Parsed:  map[string]any{"success": true},
		Latency: time.Since(start),
	}, nil
}

// resolveCommand turns a request into CLI lines, plus how to answer any
// (y/n) question the switch asks along the way.
func (a *NetgearM4250Adapter) resolveCommand(req device.CommandRequest) ([]string, func(string) string, error) {
	if raw, ok := a.Cfg.Commands[req.Name]; ok {
		for k, v := range req.Args {
			raw = strings.ReplaceAll(raw, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		var lines []string
		for _, l := range strings.Split(raw, ";") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		return lines, nil, nil
	}

	iface := map[string]string{
		"poe_reset":    "poe reset",
		"poe_on":       "poe",
		"poe_off":      "no poe",
		"port_enable":  "no shutdown",
		"port_disable": "shutdown",
	}
	if action, ok := iface[req.Name]; ok {
		port, err := a.portArg(req)
		if err != nil {
			return nil, nil, err
		}
		return []string{"configure", "interface " + port, action, "exit", "exit"}, nil, nil
	}

	switch {
	case req.Name == "reboot":
		// Don't save unsaved changes (they may be the reason for the
		// reboot); do confirm the reset itself.
		return []string{"reload"}, func(q string) string {
			if strings.Contains(strings.ToLower(q), "save") {
				return "n"
			}
			return "y"
		}, nil
	case strings.HasPrefix(req.Name, "show "):
		return []string{req.Name}, nil, nil
	}
	return nil, nil, fmt.Errorf("netgear_m4250: unknown command %q — expected reboot, poe_reset, poe_on, poe_off, port_enable, port_disable, a show command, or a commands: override", req.Name)
}

var netgearPortRE = regexp.MustCompile(`^\d+(/\d+){1,2}$`)

func (a *NetgearM4250Adapter) portArg(req device.CommandRequest) (string, error) {
	v, ok := req.Args["port"]
	if !ok {
		return "", fmt.Errorf("netgear_m4250: command %q requires arg \"port\" (e.g. 5 or %s5)", req.Name, a.portPrefix)
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if f, ok := v.(float64); ok {
		s = strconv.Itoa(int(f))
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 {
			return "", fmt.Errorf("netgear_m4250: port %d out of range", n)
		}
		s = a.portPrefix + strconv.Itoa(n)
	}
	if !netgearPortRE.MatchString(s) {
		return "", fmt.Errorf("netgear_m4250: port %q is not a port number or slot/port name", s)
	}
	return s, nil
}

// ── Capabilities ─────────────────────────────────────────────────────────

var netgearBaseMetrics = []string{
	"model", "system_description", "serial_number", "mac_address",
	"software_version", "boot_version",
	"system_name", "system_location", "uptime_s",
	"temperature_c", "fan_count", "psu_count",
	"poe_status", "poe_budget_w", "poe_consumed_w", "poe_threshold_w", "poe_ports_delivering",
	"ports_total", "ports_up",
	"fault_count", "faults",
	"response_ms",
}

func (a *NetgearM4250Adapter) Capabilities() device.Capabilities {
	metrics := append([]string{}, netgearBaseMetrics...)
	a.portsMu.RLock()
	for _, k := range a.ports {
		metrics = append(metrics,
			"port_"+k+"_link", "port_"+k+"_admin", "port_"+k+"_speed",
			"port_"+k+"_poe_status", "port_"+k+"_poe_power_w", "port_"+k+"_poe_class")
	}
	a.portsMu.RUnlock()

	commands := []string{"reboot", "poe_reset", "poe_on", "poe_off", "port_enable", "port_disable"}
	for name := range a.Cfg.Commands {
		commands = append(commands, name)
	}
	sortStrings(commands)
	sortStrings(metrics)
	return device.Capabilities{
		Power:    device.PowerCapability{On: false, Off: false},
		Commands: commands,
		Metrics:  metrics,
	}
}

// ── CLI session ──────────────────────────────────────────────────────────

var errNetgearClosed = errors.New("session closed by switch")

type netgearSession struct {
	conn   net.Conn
	r      *bufio.Reader
	prompt string // e.g. "(M4250-26G4F-PoE+)"; config modes append "(Config)" etc.
	ctx    context.Context
}

func (a *NetgearM4250Adapter) withSession(ctx context.Context, fn func(*netgearSession) error) error {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	s, err := a.login(ctx)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	return fn(s)
}

var netgearLoginFailRE = regexp.MustCompile(`(?i)(incorrect|failed|denied|invalid)`)

func (a *NetgearM4250Adapter) login(ctx context.Context) (*netgearSession, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", a.address, err)
	}
	s := &netgearSession{conn: conn, r: bufio.NewReader(newTelnetFilter(conn)), ctx: ctx}
	fail := func(step string, err error, seen string) (*netgearSession, error) {
		conn.Close()
		return nil, fmt.Errorf("%s: %w (saw %q)", step, err, collapse(seen))
	}

	seen, err := s.readUntil(netgearLoginTimeout, func(b string) bool {
		return endsWithAny(b, []string{"user:", "username:", "login:"})
	})
	if err != nil {
		return fail("login prompt", err, seen)
	}
	if err := writeCRLF(conn, a.Cfg.Username); err != nil {
		return fail("send username", err, "")
	}
	if seen, err = s.readUntil(netgearLoginTimeout, func(b string) bool {
		return endsWithAny(b, []string{"password:"})
	}); err != nil {
		return fail("password prompt", err, seen)
	}
	if err := writeCRLF(conn, a.Cfg.Password); err != nil {
		return fail("send password", err, "")
	}

	// Either a prompt, or the login prompt again after a rejection.
	seen, err = s.readUntil(netgearLoginTimeout, func(b string) bool {
		return netgearTailPrompt(b) != "" || endsWithAny(b, []string{"user:", "username:"})
	})
	if err != nil || netgearTailPrompt(seen) == "" {
		if err == nil || netgearLoginFailRE.MatchString(seen) {
			err = errors.New("login rejected — check username and password")
		}
		return fail("login", err, seen)
	}

	// User EXEC (">") → Privileged EXEC ("#"). The enable password is
	// blank unless one has been set; tags.enable_password supplies it.
	if strings.HasSuffix(netgearTailPrompt(seen), ">") {
		if err := writeCRLF(conn, "enable"); err != nil {
			return fail("enable", err, "")
		}
		seen, err = s.readUntil(netgearLoginTimeout, func(b string) bool {
			return endsWithAny(b, []string{"password:"}) || strings.HasSuffix(netgearTailPrompt(b), "#")
		})
		if err != nil {
			return fail("enable", err, seen)
		}
		if endsWithAny(seen, []string{"password:"}) {
			if err := writeCRLF(conn, a.Cfg.Tags["enable_password"]); err != nil {
				return fail("enable password", err, "")
			}
			if seen, err = s.readUntil(netgearLoginTimeout, func(b string) bool {
				return netgearTailPrompt(b) != ""
			}); err != nil {
				return fail("enable password", err, seen)
			}
		}
		if !strings.HasSuffix(netgearTailPrompt(seen), "#") {
			return fail("enable", errors.New("not granted privileged mode — set tags.enable_password or use a privilege-15 user"), seen)
		}
	}
	p := netgearTailPrompt(seen)
	s.prompt = strings.TrimSpace(p[:len(p)-1])
	if i := strings.Index(s.prompt, ")"); i >= 0 {
		s.prompt = s.prompt[:i+1] // the hostname part, without any mode suffix
	}
	return s, nil
}

var netgearMoreRE = regexp.MustCompile(`--More--( or \(q\)uit)?`)

// run sends one CLI line and returns its output, without the echoed command
// or the trailing prompt. Pages are advanced with a space; (y/n) questions
// go to answer (or are declined when answer is nil).
func (s *netgearSession) run(cmd string, answer func(question string) string) (string, error) {
	if err := writeCRLF(s.conn, cmd); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}
	var out, line strings.Builder // out: finished lines; line: the current one
	deadline := time.Now().Add(netgearCommandTimeout)
	if d, ok := s.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	reply := func(b []byte) error {
		_ = s.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, err := s.conn.Write(b)
		return err
	}
	// check looks at the current line for a pager, a question or the
	// prompt. done reports the command finished. The switch erases a pager
	// with CRs and carries on in the same line, so pagers are counted
	// rather than cleared (netgearClean removes them), and questions and
	// prompts are looked for after the last CR.
	pagers := 0
	check := func() (done bool, err error) {
		raw := line.String()
		if n := len(netgearMoreRE.FindAllStringIndex(raw, -1)); n > pagers {
			pagers = n
			return false, reply([]byte(" "))
		}
		cur := strings.TrimSpace(raw[strings.LastIndexByte(raw, '\r')+1:])
		switch {
		case cur == "":
			return false, nil
		case strings.HasSuffix(cur, "(y/n)"):
			r := "n"
			if answer != nil {
				r = answer(cur)
			}
			out.WriteString(cur + "\n")
			line.Reset()
			pagers = 0
			return false, reply([]byte(r)) // single keypress, no Enter
		case s.prompt != "" && strings.HasPrefix(cur, s.prompt) &&
			(strings.HasSuffix(cur, "#") || strings.HasSuffix(cur, ">")):
			return true, nil
		}
		return false, nil
	}
	for {
		if time.Now().After(deadline) {
			return netgearClean(out.String()+line.String(), cmd), fmt.Errorf("timeout waiting for %q", cmd)
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(netgearQuiet))
		b, err := s.r.ReadByte()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if done, err := check(); done || err != nil {
					return netgearClean(out.String(), cmd), err
				}
				continue
			}
			text := netgearClean(out.String()+line.String(), cmd)
			if errors.Is(err, io.EOF) {
				return text, errNetgearClosed
			}
			return text, fmt.Errorf("%w: %v", errNetgearClosed, err)
		}
		if b == '\n' {
			out.WriteString(line.String() + "\n")
			line.Reset()
			pagers = 0
			continue
		}
		line.WriteByte(b)
		switch b {
		case ' ', '-', ')', '#', '>', 't': // pagers, questions and prompts end on these
			if done, err := check(); done || err != nil {
				return netgearClean(out.String(), cmd), err
			}
		}
	}
}

// readUntil reads until done(buffer) holds or the timeout elapses.
func (s *netgearSession) readUntil(timeout time.Duration, done func(string) bool) (string, error) {
	deadline := time.Now().Add(timeout)
	var seen strings.Builder
	for time.Now().Before(deadline) {
		_ = s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		b, err := s.r.ReadByte()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if done(seen.String()) {
					return seen.String(), nil
				}
				continue
			}
			return seen.String(), err
		}
		seen.WriteByte(b)
		if done(seen.String()) {
			return seen.String(), nil
		}
	}
	return seen.String(), errors.New("timeout")
}

// netgearTailPrompt returns the last line if it looks like a CLI prompt,
// "(hostname) #" or "(hostname) >", else "".
var netgearPromptRE = regexp.MustCompile(`^\(.+\)\s*(\(.+\))?\s*[#>]$`)

func netgearTailPrompt(buf string) string {
	tail := strings.TrimSpace(buf[strings.LastIndexAny(buf, "\n")+1:])
	if netgearPromptRE.MatchString(tail) {
		return tail
	}
	return ""
}

// netgearClean strips the echoed command, pager remnants and control bytes.
func netgearClean(s, cmd string) string {
	s = netgearMoreRE.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && strings.Contains(lines[0], cmd) {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var netgearErrorRE = regexp.MustCompile(`(?im)^\s*(% ?invalid|% ?incomplete|% ?ambiguous|error|an invalid|invalid input|command not found)`)

func netgearIsError(out string) bool { return netgearErrorRE.MatchString(out) }

func netgearErrorLine(out string) string {
	if loc := netgearErrorRE.FindStringIndex(out); loc != nil {
		return strings.TrimSpace(out[loc[0]:])
	}
	return out
}

// ── Parsing ──────────────────────────────────────────────────────────────

var netgearKVRE = regexp.MustCompile(`^\s*([^.]+?)\s*\.{2,}\s*(.*?)\s*$`)

// netgearKV parses "Name........... value" lines into a map keyed by the
// lower-cased name.
func netgearKV(out string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := netgearKVRE.FindStringSubmatch(line); m != nil {
			kv[strings.ToLower(m[1])] = m[2]
		}
	}
	return kv
}

var netgearUptimeRE = regexp.MustCompile(`(?i)(\d+)\s*(days?|hrs?|hours?|mins?|minutes?|secs?|seconds?)\b`)

// netgearUptime parses "2 days 3 hrs 24 mins 33 secs" into seconds.
func netgearUptime(s string) (int, bool) {
	ms := netgearUptimeRE.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return 0, false
	}
	total := 0
	for _, m := range ms {
		n, _ := strconv.Atoi(m[1])
		switch strings.ToLower(m[2][:1]) {
		case "d":
			total += n * 86400
		case "h":
			total += n * 3600
		case "m":
			total += n * 60
		case "s":
			total += n
		}
	}
	return total, true
}

var netgearNumRE = regexp.MustCompile(`[-0-9.]+`)

func netgearWatts(s string) (float64, bool) {
	f, err := strconv.ParseFloat(netgearNumRE.FindString(s), 64)
	return f, err == nil
}

type netgearEnv struct {
	temp   float64
	tempOK bool
	fans   int
	psus   int
	faults [][3]string // level, type, description
}

var netgearColsRE = regexp.MustCompile(`\s{2,}`)

// netgearEnvironment parses `show environment`: the overall temperature,
// then the Temperature Sensors / Fans / Power Modules tables.
func netgearEnvironment(out string) netgearEnv {
	var e netgearEnv
	if t, err := strconv.ParseFloat(netgearKV(out)["temp (c)"], 64); err == nil {
		e.temp, e.tempOK = t, true
	}
	section := ""
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "Temperature Sensors"):
			section = "sensor"
			continue
		case strings.HasPrefix(trim, "Fans"):
			section = "fan"
			continue
		case strings.HasPrefix(trim, "Power Modules") || strings.HasPrefix(trim, "Power Supplies"):
			section = "psu"
			continue
		}
		cols := netgearColsRE.Split(trim, -1)
		if section == "" || len(cols) < 4 || !isDigits(cols[0]) {
			continue // headers, rules and anything else
		}
		desc, state := cols[2], cols[len(cols)-1]
		switch section {
		case "sensor":
			if len(cols) >= 5 {
				state = cols[4]
				if !e.tempOK {
					if t, err := strconv.ParseFloat(cols[3], 64); err == nil {
						e.temp, e.tempOK = t, true
					}
				}
			}
			if !strings.EqualFold(state, "Normal") && !strings.EqualFold(state, "N/A") {
				e.faults = append(e.faults, [3]string{"Warning", "Temperature", desc + ": " + state})
			}
		case "fan":
			e.fans++
			if !netgearHealthy(state) {
				e.faults = append(e.faults, [3]string{"Error", "Fan", desc + ": " + state})
			}
		case "psu":
			e.psus++
			if !netgearHealthy(state) {
				e.faults = append(e.faults, [3]string{"Error", "Power supply", desc + ": " + state})
			}
		}
	}
	return e
}

// netgearHealthy: Operational is fine, and so is an empty bay.
func netgearHealthy(state string) bool {
	s := strings.ToLower(state)
	return s == "operational" || s == "not present" || s == "notpresent" || s == "n/a"
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type netgearPoEPort struct {
	intf, class, status, fault string
	powerMW                    int
}

var netgearIntfRE = regexp.MustCompile(`^\d+/\d+(/\d+)?$`)

// netgearPoEPorts parses `show poe port info all`:
//
//	0/2      Yes   32000    4        4000     74      54       Delivering Power  No Error
func netgearPoEPorts(out string) []netgearPoEPort {
	var ports []netgearPoEPort
	for _, line := range strings.Split(out, "\n") {
		cols := netgearColsRE.Split(strings.TrimSpace(line), -1)
		if len(cols) < 8 || !netgearIntfRE.MatchString(cols[0]) {
			continue
		}
		mw, _ := strconv.Atoi(cols[4])
		p := netgearPoEPort{intf: cols[0], class: cols[3], powerMW: mw, status: cols[7]}
		if len(cols) >= 9 {
			p.fault = cols[8]
		}
		ports = append(ports, p)
	}
	return ports
}

type netgearPort struct {
	intf, admin, link, speed string
}

var (
	netgearLinkRE  = regexp.MustCompile(`\s(Up|Down)\s`)
	netgearSpeedRE = regexp.MustCompile(`\s(\d+(?:\.\d+)?G?\s+(?:Full|Half))\s`)
	netgearAdminRE = regexp.MustCompile(`\s(Enable|Disable)\s`)
)

// netgearPorts parses `show port all`, keeping physical front-panel ports
// (slot 0: "0/5", or "1/0/5" on a stackable M4350). The Type column is
// usually blank, so fields are found by value rather than position.
//
//	0/1              Enable    Auto       100 Full   Up     Enable  Enable long
func netgearPorts(out string) []netgearPort {
	var ports []netgearPort
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !netgearIntfRE.MatchString(fields[0]) || !netgearPhysical(fields[0]) {
			continue
		}
		rest := " " + strings.TrimSpace(line[strings.Index(line, fields[0])+len(fields[0]):]) + " "
		lm := netgearLinkRE.FindStringSubmatch(rest)
		if lm == nil {
			continue
		}
		p := netgearPort{intf: fields[0], link: strings.ToLower(lm[1])}
		if am := netgearAdminRE.FindStringSubmatch(rest); am != nil {
			p.admin = strings.ToLower(am[1]) + "d" // enabled / disabled
		}
		// Physical status comes before the link column; the mode column
		// ("Auto" or "100 Full") comes before that, so take the last match.
		if ms := netgearSpeedRE.FindAllStringSubmatch(rest[:strings.Index(rest, lm[0])+1], -1); len(ms) > 0 && p.link == "up" {
			p.speed = ms[len(ms)-1][1]
		}
		ports = append(ports, p)
	}
	sort.SliceStable(ports, func(i, j int) bool { return netgearPortLess(ports[i].intf, ports[j].intf) })
	return ports
}

func netgearPhysical(intf string) bool {
	parts := strings.Split(intf, "/")
	return parts[len(parts)-2] == "0"
}

// netgearPortKey names a port in metric keys: "0/5" → "5", "1/0/5" → "1_0_5".
func netgearPortKey(intf string) string {
	if strings.HasPrefix(intf, "0/") && strings.Count(intf, "/") == 1 {
		return intf[2:]
	}
	return strings.ReplaceAll(intf, "/", "_")
}

func netgearPortLess(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
