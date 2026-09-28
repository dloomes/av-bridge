package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// PhilipsSICPAdapter controls Philips professional displays (PPDS signage
// and meeting-room ranges: Q-Line, D-Line, T-Line and siblings) over SICP,
// the Serial/Ethernet Interface Communication Protocol, on TCP port 5000.
// Written against the SICP V2.03 specification.
//
// Frame: MsgSize | Control (monitor ID) | Group | Data[0..N] | Checksum,
// where MsgSize counts every byte including itself and the checksum, and
// the checksum is the XOR of all preceding bytes. Group 0 addresses the
// display by monitor ID alone.
//
// A Get is answered by a report whose Data[0] repeats the command code; a
// Set is answered by Data[0] = 0x00 with Data[1] ACK (0x06), NACK (0x15)
// or NAV (0x18, "not available" — valid but unsupported on this model or
// in this state). One command at a time; the spec allows a retry after
// 500 ms without a reply.
//
// Session model: one TCP connection per poll or command, serialised by
// connMu. Displays keep the network up in standby only when APM is set to
// "TCP on"; otherwise power_on relies on Wake-on-LAN (tags.mac_address).
type PhilipsSICPAdapter struct {
	device.Base
	address   string
	monitorID byte

	connMu sync.Mutex

	// Identity rarely changes; read once per connection success and cached.
	identMu  sync.Mutex
	identity map[string]any
}

const (
	sicpDefaultPort = 5000
	sicpReplyWait   = 700 * time.Millisecond

	sicpAck  = 0x06
	sicpNack = 0x15
	sicpNav  = 0x18
)

// SICP command codes (spec §16 command summary).
const (
	sicpCmdPowerGet     = 0x19
	sicpCmdPowerSet     = 0x18
	sicpCmdSourceGet    = 0xAD
	sicpCmdSourceSet    = 0xAC
	sicpCmdVolumeGet    = 0x45
	sicpCmdVolumeSet    = 0x44
	sicpCmdVolumeStep   = 0x41
	sicpCmdMuteGet      = 0x46
	sicpCmdMuteSet      = 0x47
	sicpCmdMiscGet      = 0x0F
	sicpCmdTempGet      = 0x2F
	sicpCmdSerialGet    = 0x15
	sicpCmdModelGet     = 0xA1
	sicpCmdRestart      = 0x57
	sicpCmdVideoPresent = 0x59
	sicpCmdBacklightGet = 0x71
	sicpCmdBacklightSet = 0x72
)

// sicpSources maps input source codes to labels (spec §4.4.1).
var sicpSources = map[byte]string{
	0x01: "video", 0x02: "s-video", 0x03: "component", 0x05: "vga",
	0x06: "hdmi2", 0x07: "displayport2", 0x08: "usb2", 0x09: "card_dvi",
	0x0A: "displayport", 0x0B: "ops", 0x0C: "usb", 0x0D: "hdmi1",
	0x0E: "dvi", 0x0F: "hdmi3", 0x10: "browser", 0x11: "smartcms",
	0x12: "dms", 0x13: "internal_storage", 0x16: "media_player",
	0x17: "pdf_player", 0x18: "custom", 0x19: "hdmi4", 0x1A: "vga2",
	0x1B: "vga3", 0x1C: "iwb",
}

// sicpInputCommands are the input_* commands offered as buttons.
var sicpInputCommands = map[string]byte{
	"input_hdmi1": 0x0D, "input_hdmi2": 0x06, "input_hdmi3": 0x0F, "input_hdmi4": 0x19,
	"input_displayport": 0x0A, "input_dvi": 0x0E, "input_vga": 0x05, "input_ops": 0x0B,
}

var errSICPNav = errors.New("not available on this display or in its current state (NAV)")

func NewPhilipsSICPAdapter(cfg config.DeviceConfig) *PhilipsSICPAdapter {
	addr := strings.TrimSpace(cfg.Address)
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, sicpDefaultPort)
	}
	id := byte(1)
	if v := cfg.Tags["monitor_id"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 255 {
			id = byte(n)
		}
	}
	return &PhilipsSICPAdapter{Base: device.NewBase(cfg), address: addr, monitorID: id}
}

// ── Connection ───────────────────────────────────────────────────────────

// Connect proves the display answers SICP by reading its power state.
func (a *PhilipsSICPAdapter) Connect(ctx context.Context) error {
	var state string
	err := a.withConn(ctx, func(s *sicpSession) error {
		var err error
		state, err = s.powerState()
		return err
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("philips_sicp connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("philips_sicp connected", "device", a.Cfg.ID, "address", a.address,
		"monitor_id", a.monitorID, "power", state)
	return nil
}

func (a *PhilipsSICPAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

// Poll reads power state first. In standby most displays answer only a
// few commands (others return NAV), so picture and audio readings are
// taken only while the display is on. Every reading is best-effort: a NAV
// or an odd reply leaves that metric out rather than failing the poll.
func (a *PhilipsSICPAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()
	m := map[string]any{}

	err := a.withConn(ctx, func(s *sicpSession) error {
		state, err := s.powerState()
		if err != nil {
			return err
		}
		m["power_status"] = state

		if d, err := s.get(sicpCmdMiscGet, 0x02); err == nil && len(d) >= 2 {
			m["operating_hours"] = int(d[0])<<8 | int(d[1])
		}
		a.identMu.Lock()
		cached := a.identity
		a.identMu.Unlock()
		if cached == nil {
			cached = s.identity()
			if len(cached) > 0 {
				a.identMu.Lock()
				a.identity = cached
				a.identMu.Unlock()
			}
		}
		for k, v := range cached {
			m[k] = v
		}

		if state != "on" {
			return nil
		}
		if d, err := s.get(sicpCmdSourceGet); err == nil && len(d) >= 1 {
			m["current_input"] = sicpSourceLabel(d[0])
		}
		if d, err := s.get(sicpCmdVolumeGet); err == nil && len(d) >= 1 {
			m["volume"] = int(d[0])
			if len(d) >= 2 {
				m["volume_audio_out"] = int(d[1])
			}
		}
		if d, err := s.get(sicpCmdMuteGet); err == nil && len(d) >= 1 {
			m["mute"] = d[0] == 0x01
		}
		if d, err := s.get(sicpCmdTempGet); err == nil && len(d) >= 1 {
			m["temperature_c"] = int(d[0])
			if len(d) >= 2 && d[1] > 0 && d[1] <= 100 {
				m["temperature_2_c"] = int(d[1])
			}
		}
		if d, err := s.get(sicpCmdVideoPresent); err == nil && len(d) >= 1 {
			m["signal_present"] = d[0] == 0x01
		}
		if d, err := s.get(sicpCmdBacklightGet); err == nil && len(d) >= 1 {
			if d[0] == 0x01 {
				m["backlight"] = "off"
			} else {
				m["backlight"] = "on"
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
	m["response_ms"] = time.Since(start).Milliseconds()
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = m
	return t, nil
}

func sicpSourceLabel(code byte) string {
	if s, ok := sicpSources[code]; ok {
		return s
	}
	return fmt.Sprintf("source_0x%02x", code)
}

// ── Commands ─────────────────────────────────────────────────────────────

// SendCommand runs one of:
//
//	power_on / power_off          0x18 (Wake-on-LAN first, when a MAC is known)
//	input_hdmi1..4, input_displayport, input_dvi, input_vga, input_ops   0xAC
//	set_volume {level 0–100}      0x44
//	volume_up / volume_down       0x41
//	mute / unmute                 0x47
//	backlight_on / backlight_off  0x72 (picture off, audio untouched)
//	reboot                        0x57
//
// A commands: entry in config wins: space-separated hex bytes for
// Data[0..N], e.g. "AC 0D 09 01 00", with {placeholders} substituted.
func (a *PhilipsSICPAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	frames, err := a.resolveCommand(req)
	if err != nil {
		return nil, err
	}
	if req.Name == "power_on" {
		a.wake()
	}

	start := time.Now()
	var used []byte
	err = a.withConn(ctx, func(s *sicpSession) error {
		// Some commands have an older short form; try each until one isn't NAV.
		var err error
		for _, f := range frames {
			used = f
			if err = s.set(f...); !errors.Is(err, errSICPNav) {
				return err
			}
		}
		return err
	})
	raw := fmt.Sprintf("% X", used)
	if err != nil {
		return &device.CommandResponse{Raw: raw, Parsed: map[string]any{"success": false}, Latency: time.Since(start)},
			fmt.Errorf("philips_sicp command %q: %w", req.Name, err)
	}
	slog.Info("philips_sicp command", "device", a.Cfg.ID, "command", req.Name, "data", raw)
	return &device.CommandResponse{Raw: raw, Parsed: map[string]any{"success": true}, Latency: time.Since(start)}, nil
}

// resolveCommand returns the Data[] payload(s) to try, in order.
func (a *PhilipsSICPAdapter) resolveCommand(req device.CommandRequest) ([][]byte, error) {
	if tmpl, ok := a.Cfg.Commands[req.Name]; ok {
		for k, v := range req.Args {
			tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		data, err := sicpParseHex(tmpl)
		if err != nil {
			return nil, fmt.Errorf("philips_sicp: command %q: %w", req.Name, err)
		}
		return [][]byte{data}, nil
	}

	if src, ok := sicpInputCommands[req.Name]; ok {
		// Source, playlist 0x09 (ignored for video inputs), OSD source label, mute style — as the spec's examples.
		return [][]byte{{sicpCmdSourceSet, src, 0x09, 0x01, 0x00}, {sicpCmdSourceSet, src}}, nil
	}
	switch req.Name {
	case "power_on":
		return [][]byte{{sicpCmdPowerSet, 0x02}}, nil
	case "power_off":
		return [][]byte{{sicpCmdPowerSet, 0x01}}, nil
	case "mute":
		return [][]byte{{sicpCmdMuteSet, 0x01}}, nil
	case "unmute":
		return [][]byte{{sicpCmdMuteSet, 0x00}}, nil
	case "volume_up", "volume_down":
		dir := byte(0x00)
		if req.Name == "volume_up" {
			dir = 0x01
		}
		// Speaker step with audio out unchanged (0x02); older platforms take speaker only.
		return [][]byte{{sicpCmdVolumeStep, dir, 0x02}, {sicpCmdVolumeStep, dir}}, nil
	case "set_volume":
		level, err := sicpLevelArg(req.Args["level"])
		if err != nil {
			return nil, err
		}
		return [][]byte{{sicpCmdVolumeSet, level, level}, {sicpCmdVolumeSet, level}}, nil
	case "backlight_on":
		return [][]byte{{sicpCmdBacklightSet, 0x00}}, nil
	case "backlight_off":
		return [][]byte{{sicpCmdBacklightSet, 0x01}}, nil
	case "reboot":
		return [][]byte{{sicpCmdRestart, 0x00}}, nil
	}
	return nil, fmt.Errorf("philips_sicp: unknown command %q", req.Name)
}

func sicpLevelArg(v any) (byte, error) {
	var n int
	switch t := v.(type) {
	case nil:
		return 0, errors.New(`philips_sicp: set_volume requires arg "level" (0–100)`)
	case float64:
		n = int(t)
	case int:
		n = t
	case string:
		var err error
		if n, err = strconv.Atoi(strings.TrimSpace(t)); err != nil {
			return 0, fmt.Errorf("philips_sicp: level %q is not a number", t)
		}
	default:
		return 0, fmt.Errorf("philips_sicp: level arg type %T not supported", v)
	}
	if n < 0 || n > 100 {
		return 0, fmt.Errorf("philips_sicp: level %d out of range 0–100", n)
	}
	return byte(n), nil
}

func sicpParseHex(s string) ([]byte, error) {
	var out []byte
	for _, f := range strings.Fields(strings.NewReplacer(",", " ", "0x", "", "0X", "").Replace(s)) {
		b, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("%q is not a hex byte", f)
		}
		out = append(out, byte(b))
	}
	if len(out) == 0 || len(out) > 36 {
		return nil, fmt.Errorf("need 1–36 data bytes, got %d", len(out))
	}
	return out, nil
}

// wake sends a Wake-on-LAN packet when the MAC is known, so power_on also
// works from a standby in which the display's network is off.
func (a *PhilipsSICPAdapter) wake() {
	mac := a.Cfg.Tags["mac_address"]
	if mac == "" {
		return
	}
	host, _, err := net.SplitHostPort(a.address)
	if err != nil {
		host = a.address
	}
	if err := sendWakeOnLAN(mac, host); err != nil {
		slog.Warn("philips_sicp WoL failed", "device", a.Cfg.ID, "error", err)
	}
}

// ── Capabilities ─────────────────────────────────────────────────────────

var philipsSICPCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: true, Off: true},
	Commands: []string{
		"power_on", "power_off",
		"input_hdmi1", "input_hdmi2", "input_hdmi3", "input_hdmi4",
		"input_displayport", "input_dvi", "input_vga", "input_ops",
		"set_volume", "volume_up", "volume_down", "mute", "unmute",
		"backlight_on", "backlight_off",
		"reboot",
	},
	Metrics: []string{
		"power_status", "current_input", "signal_present", "backlight",
		"volume", "volume_audio_out", "mute",
		"temperature_c", "temperature_2_c", "operating_hours",
		"model", "firmware_version", "serial_number",
		"response_ms",
	},
}

func (a *PhilipsSICPAdapter) Capabilities() device.Capabilities {
	c := philipsSICPCapabilities
	if len(a.Cfg.Commands) > 0 {
		c.Commands = append([]string{}, c.Commands...)
		for name := range a.Cfg.Commands {
			c.Commands = append(c.Commands, name)
		}
	}
	return c
}

// ── SICP session ─────────────────────────────────────────────────────────

type sicpSession struct {
	conn net.Conn
	id   byte
}

func (a *PhilipsSICPAdapter) withConn(ctx context.Context, fn func(*sicpSession) error) error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.address, err)
	}
	defer conn.Close()
	return fn(&sicpSession{conn: conn, id: a.monitorID})
}

// sicpFrame builds MsgSize | Control | Group(0) | data... | checksum.
func sicpFrame(id byte, data ...byte) []byte {
	f := make([]byte, 0, len(data)+4)
	f = append(f, byte(len(data)+4), id, 0x00)
	f = append(f, data...)
	var x byte
	for _, b := range f {
		x ^= b
	}
	return append(f, x)
}

// exchange sends data and returns the Data[] of the first reply that is
// either the report for data[0] or a communication-control report. It
// retries once when nothing arrives, as the spec allows.
func (s *sicpSession) exchange(data ...byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := s.conn.Write(sicpFrame(s.id, data...)); err != nil {
			return nil, fmt.Errorf("write: %w", err)
		}
		deadline := time.Now().Add(sicpReplyWait)
		for {
			reply, err := s.readFrame(deadline)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					lastErr = errors.New("no reply from display")
					break // retry
				}
				return nil, err
			}
			if len(reply) == 0 {
				continue
			}
			if reply[0] == data[0] || reply[0] == 0x00 {
				return reply, nil
			}
			// A stray report for something else; keep reading.
		}
	}
	return nil, lastErr
}

// readFrame reads one frame and returns its Data[] (after Group, before checksum).
func (s *sicpSession) readFrame(deadline time.Time) ([]byte, error) {
	_ = s.conn.SetReadDeadline(deadline)
	head := make([]byte, 1)
	if _, err := io.ReadFull(s.conn, head); err != nil {
		return nil, err
	}
	size := int(head[0])
	if size < 4 || size > 40 {
		return nil, fmt.Errorf("bad frame size %d", size)
	}
	rest := make([]byte, size-1)
	if _, err := io.ReadFull(s.conn, rest); err != nil {
		return nil, err
	}
	x := head[0]
	for _, b := range rest[:len(rest)-1] {
		x ^= b
	}
	if x != rest[len(rest)-1] {
		return nil, fmt.Errorf("bad checksum in reply % X", append(head, rest...))
	}
	// rest = Control, Group, Data..., Checksum. Displays without a group
	// byte would reply with size 4 + data; Group is always sent in this
	// spec version, so Data starts at index 2.
	return rest[2 : len(rest)-1], nil
}

// get sends a Get and returns the report's data after the command code.
func (s *sicpSession) get(data ...byte) ([]byte, error) {
	reply, err := s.exchange(data...)
	if err != nil {
		return nil, err
	}
	if reply[0] == 0x00 {
		return nil, sicpControlError(reply)
	}
	return reply[1:], nil
}

// set sends a Set and waits for ACK.
func (s *sicpSession) set(data ...byte) error {
	reply, err := s.exchange(data...)
	if err != nil {
		return err
	}
	if reply[0] != 0x00 {
		return nil // a report instead of an ACK still means it was accepted
	}
	return sicpControlError(reply)
}

func sicpControlError(reply []byte) error {
	if len(reply) < 2 {
		return errors.New("short control report")
	}
	switch reply[1] {
	case sicpAck:
		return nil
	case sicpNav:
		return errSICPNav
	case sicpNack:
		return errors.New("display rejected the command (NACK)")
	}
	return fmt.Errorf("unexpected control report 0x%02X", reply[1])
}

func (s *sicpSession) powerState() (string, error) {
	d, err := s.get(sicpCmdPowerGet)
	if err != nil {
		return "", fmt.Errorf("power state: %w", err)
	}
	if len(d) < 1 {
		return "", errors.New("power state: empty report")
	}
	switch d[0] {
	case 0x02:
		return "on", nil
	case 0x01:
		return "standby", nil
	}
	return fmt.Sprintf("unknown_0x%02x", d[0]), nil
}

// identity reads model, firmware and serial; any that aren't supported
// are left out.
func (s *sicpSession) identity() map[string]any {
	out := map[string]any{}
	str := func(d []byte) string { return strings.TrimSpace(strings.Trim(string(d), "\x00")) }
	if d, err := s.get(sicpCmdModelGet, 0x00); err == nil && str(d) != "" {
		out["model"] = str(d)
	}
	if d, err := s.get(sicpCmdModelGet, 0x01); err == nil && str(d) != "" {
		out["firmware_version"] = str(d)
	}
	if d, err := s.get(sicpCmdSerialGet); err == nil && str(d) != "" {
		out["serial_number"] = str(d)
	}
	return out
}
