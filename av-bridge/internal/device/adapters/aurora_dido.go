package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// AuroraDIDOAdapter — Aurora Multimedia DIDO 4x4 HDMI matrix / video wall
// processor. Reference: DIDO Protocol Guide v0.0.9 (Aurora Multimedia).
//
// Wire protocol: the guide's "TCP commands" (§1.3) — a single-line text
// command to TCP port 6970, answered with a JSON object, as on the VPX.
// Commands end \r\n and the shared IAC filter strips any Telnet
// negotiation, as for the VPX.
//
// The guide prints example replies for only a few commands (get_zoom,
// preset_list, the *_config reads); the rest just say "returns ...". So
// replies are read tolerantly: a field is found by any of several likely
// key names, at the top level or one object down. A reply the adapter
// can't read is put on the device page as <command>_reply, so the real
// shape can be seen and the parsing tightened.
type AuroraDIDOAdapter struct {
	device.Base
	address string

	connMu sync.Mutex
	conn   net.Conn
	dec    *json.Decoder
	raw    *tailBuffer

	// cmdMu serialises requests: the device answers one at a time on the
	// connection, and Poll and SendCommand share it.
	cmdMu sync.Mutex
}

const auroraDIDODefaultPort = 6970

func NewAuroraDIDOAdapter(cfg config.DeviceConfig) *AuroraDIDOAdapter {
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, auroraDIDODefaultPort)
	}
	return &AuroraDIDOAdapter{
		Base:    device.NewBase(cfg),
		address: addr,
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *AuroraDIDOAdapter) Connect(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora_dido connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}

	a.connMu.Lock()
	if a.conn != nil {
		_ = a.conn.Close()
	}
	a.conn = conn
	a.raw = &tailBuffer{max: 8 << 10}
	a.dec = json.NewDecoder(bufio.NewReader(io.TeeReader(newTelnetFilter(conn), a.raw)))
	a.connMu.Unlock()

	// Probe: no side effects, small reply, proves we're getting JSON.
	resp, err := a.request(ctx, "get_fw_version")
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora_dido probe %s: %w", a.Cfg.ID, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("aurora_dido connected", "device", a.Cfg.ID, "address", a.address,
		"fw_version", didoString(resp, "fw_version", "firmware_version", "FW_version", "version"))
	return nil
}

func (a *AuroraDIDOAdapter) connected() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	return a.conn != nil
}

func (a *AuroraDIDOAdapter) ensureConn(ctx context.Context) error {
	if a.connected() {
		return nil
	}
	return a.Connect(ctx)
}

func (a *AuroraDIDOAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	var err error
	if a.conn != nil {
		err = a.conn.Close()
		a.conn = nil
		a.dec = nil
	}
	a.SetStatus(device.StatusOffline)
	return err
}

// Heartbeat implements device.Heartbeater: keeps the connection warm
// between polls with the cheapest read.
func (a *AuroraDIDOAdapter) Heartbeat(ctx context.Context) error {
	_, err := a.request(ctx, "get_fw_version")
	return err
}

// ── Poll ──────────────────────────────────────────────────────────────────────

func (a *AuroraDIDOAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	started := time.Now()
	t := a.BaseTelemetry()
	t.Metrics = map[string]any{}

	if err := a.ensureConn(ctx); err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		return t, nil
	}

	// Firmware version is the health check: no answer, offline.
	fw, err := a.request(ctx, "get_fw_version")
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		_ = a.Disconnect()
		return t, nil
	}
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	a.put(t, "fw_version", "get_fw_version", fw, didoString(fw, "fw_version", "firmware_version", "FW_version", "version"))

	// The rest are best-effort: one a firmware doesn't support mustn't
	// blank the others. A failed read drops the connection (see
	// request), so stop at the first one and keep what's in.
	reads := []func() error{
		func() error {
			r, err := a.request(ctx, "get_api_version")
			if err == nil {
				a.put(t, "api_version", "get_api_version", r, didoString(r, "api_version", "version", "API_version"))
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_serial_number")
			if err == nil {
				a.put(t, "serial_number", "get_serial_number", r, didoString(r, "serial_number", "serial", "serialnumber", "SNUM"))
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_mac")
			if err == nil {
				a.put(t, "mac", "get_mac", r, didoString(r, "mac", "mac_id", "mac_address", "MAC"))
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_ip")
			if err == nil {
				a.put(t, "ip_address", "get_ip", r, didoString(r, "ip", "ip_address", "address", "IP"))
				if v := didoString(r, "ip_mode", "mode", "network_mode"); v != "" {
					t.Metrics["ip_mode"] = v
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_video_route")
			if err == nil {
				found := false
				for n := 1; n <= 4; n++ {
					if v := didoInput(didoString(r, didoOutKeys(n)...)); v != "" {
						t.Metrics[fmt.Sprintf("out%d_source", n)] = v
						found = true
					}
				}
				if !found {
					a.unread(t, "get_video_route", r)
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_vid_resl_mode")
			if err == nil {
				a.put(t, "resolution_mode", "get_vid_resl_mode", r, didoReslMode(didoString(r, "mode", "resolution_mode", "vid_resl_mode", "resl_mode")))
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_vid_resl current")
			if err == nil {
				found := false
				for n := 1; n <= 4; n++ {
					if v := didoString(r, didoOutKeys(n)...); v != "" {
						t.Metrics[fmt.Sprintf("out%d_resolution", n)] = v
						found = true
					}
				}
				if !found {
					a.unread(t, "get_vid_resl current", r)
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_audio_route")
			if err == nil {
				found := false
				for _, port := range []string{"lineout", "dante", "out1", "out2", "out3", "out4"} {
					if v := didoString(r, didoAudioKeys(port)...); v != "" {
						t.Metrics[port+"_audio"] = strings.ToLower(v)
						found = true
					}
				}
				if !found {
					a.unread(t, "get_audio_route", r)
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_volume lineout")
			if err == nil {
				a.putNumber(t, "lineout_volume", "get_volume lineout", r, didoString(r, "volume", "lineout", "lineout_volume", "level", "value"))
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_volume linein")
			if err == nil {
				v := didoString(r, "volume", "linein", "linein_volume", "level", "value")
				switch strings.ToLower(v) {
				case "mute", "muted":
					t.Metrics["linein_mute"] = true
				default:
					a.putNumber(t, "linein_volume", "get_volume linein", r, v)
				}
				if m := didoString(r, "mute", "linein_mute", "muted"); m != "" {
					t.Metrics["linein_mute"] = didoTruthy(m)
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "get_button_lock")
			if err == nil {
				v := didoString(r, "button_lock", "buttonlock", "lock", "locked", "value")
				if v == "" {
					a.unread(t, "get_button_lock", r)
				} else {
					t.Metrics["buttons_locked"] = didoTruthy(v)
				}
			}
			return err
		},
		func() error {
			r, err := a.request(ctx, "preset_list")
			if err == nil {
				if names := didoStrings(r, "Presets", "presets"); names != nil {
					t.Metrics["presets"] = strings.Join(names, ", ")
					t.Metrics["preset_count"] = len(names)
				}
			}
			return err
		},
	}
	for _, read := range reads {
		if err := read(); err != nil {
			if !a.connected() {
				t.Metrics["read_error"] = err.Error()
				break
			}
		}
	}

	t.Metrics["response_ms"] = time.Since(started).Milliseconds()
	return t, nil
}

// put stores v as metric name, or the raw reply when v couldn't be found.
func (a *AuroraDIDOAdapter) put(t *device.Telemetry, name, cmd string, resp map[string]any, v string) {
	if v == "" {
		a.unread(t, cmd, resp)
		return
	}
	t.Metrics[name] = v
}

// putNumber stores a whole-number metric (a volume), as a number when it
// reads as one.
func (a *AuroraDIDOAdapter) putNumber(t *device.Telemetry, name, cmd string, resp map[string]any, v string) {
	if v == "" {
		a.unread(t, cmd, resp)
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		t.Metrics[name] = n
		return
	}
	t.Metrics[name] = v
}

// unread puts a reply the adapter couldn't read on the device page, keyed
// by its command, so the shape can be seen.
func (a *AuroraDIDOAdapter) unread(t *device.Telemetry, cmd string, resp map[string]any) {
	b, _ := json.Marshal(resp)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	t.Metrics[strings.ReplaceAll(cmd, " ", "_")+"_reply"] = s
}

// ── Reading replies ──────────────────────────────────────────────────────────

// didoString finds the first of keys (case-insensitive) in resp's top level
// or one object down, and returns it as text. The reply envelope's own
// "status" and "error" are never read as values.
func didoString(resp map[string]any, keys ...string) string {
	if v, ok := didoFind(resp, keys); ok {
		return didoText(v)
	}
	for _, k := range didoSortedKeys(resp) {
		if m, ok := resp[k].(map[string]any); ok {
			if v, ok := didoFind(m, keys); ok {
				return didoText(v)
			}
		}
	}
	return ""
}

func didoFind(m map[string]any, keys []string) (any, bool) {
	for _, want := range keys {
		for k, v := range m {
			if strings.EqualFold(k, want) && !strings.EqualFold(k, "status") && !strings.EqualFold(k, "error") {
				return v, true
			}
		}
	}
	return nil, false
}

func didoSortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func didoText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case map[string]any:
		// {"out1":{"source":"in2"}} and the like: one value inside.
		for _, k := range []string{"source", "input", "src", "value", "resolution", "route"} {
			if s, ok := didoFind(x, []string{k}); ok {
				return didoText(s)
			}
		}
	}
	return ""
}

// didoStrings reads a list of names (preset_list's "Presets").
func didoStrings(resp map[string]any, keys ...string) []string {
	v, ok := didoFind(resp, keys)
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, e := range list {
		if s := didoText(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func didoOutKeys(n int) []string {
	return []string{
		fmt.Sprintf("out%d", n), fmt.Sprintf("output%d", n), fmt.Sprintf("tx%d", n),
		fmt.Sprintf("out%d_source", n), fmt.Sprintf("out%d_input", n),
	}
}

func didoAudioKeys(port string) []string {
	return []string{port, port + "_source", port + "_audio", port + "_route"}
}

// didoInput normalises a video source to in1..in4: replies may say "in2",
// "IN2" or just "2".
func didoInput(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 4 {
		return fmt.Sprintf("in%d", n)
	}
	return v
}

// didoReslMode spells the resolution mode out: the serial API's N/B/M
// letters as the TCP API's words.
func didoReslMode(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "N":
		return "native"
	case "B":
		return "bypass"
	case "M":
		return "manual"
	}
	return strings.ToLower(v)
}

func didoTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enable", "enabled", "lock", "locked", "mute", "muted":
		return true
	}
	return false
}

// ── Commands ──────────────────────────────────────────────────────────────────

const (
	didoCmdReboot       = "reboot"
	didoCmdRouteOut1    = "route_out1"
	didoCmdRouteOut2    = "route_out2"
	didoCmdRouteOut3    = "route_out3"
	didoCmdRouteOut4    = "route_out4"
	didoCmdRouteAll     = "route_all"
	didoCmdSetVolume    = "set_volume"
	didoCmdMuteLineIn   = "mute_linein"
	didoCmdUnmuteLineIn = "unmute_linein"
	didoCmdRecallPreset = "recall_preset"
	didoCmdLockButtons  = "lock_buttons"
	didoCmdUnlock       = "unlock_buttons"
)

func (a *AuroraDIDOAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	started := time.Now()
	wires, err := didoTranslate(cmd)
	if err != nil {
		return nil, err
	}
	if err := a.ensureConn(ctx); err != nil {
		return nil, err
	}
	if cmd.Name == didoCmdReboot {
		return a.sendReboot(ctx, started)
	}

	var last map[string]any
	for _, w := range wires {
		resp, err := a.request(ctx, w)
		if err != nil {
			return nil, err
		}
		last = resp
	}
	raw, _ := json.Marshal(last)
	return &device.CommandResponse{Raw: string(raw), Parsed: last, Latency: time.Since(started)}, nil
}

// didoTranslate maps a named command to the TCP command(s) it sends.
// route_all sends one route per output.
func didoTranslate(cmd device.CommandRequest) ([]string, error) {
	switch cmd.Name {
	case didoCmdReboot:
		return []string{"reboot"}, nil
	case didoCmdRouteOut1, didoCmdRouteOut2, didoCmdRouteOut3, didoCmdRouteOut4:
		in, err := didoInputArg(cmd)
		if err != nil {
			return nil, err
		}
		return []string{"set_video_route " + in + " " + strings.TrimPrefix(cmd.Name, "route_")}, nil
	case didoCmdRouteAll:
		in, err := didoInputArg(cmd)
		if err != nil {
			return nil, err
		}
		return []string{
			"set_video_route " + in + " out1", "set_video_route " + in + " out2",
			"set_video_route " + in + " out3", "set_video_route " + in + " out4",
		}, nil
	case didoCmdSetVolume:
		// Line out: the volume a room hears. Line in has its own mute.
		lvl, ok := didoIntArg(cmd.Args, "level")
		if !ok || lvl < 0 || lvl > 100 {
			return nil, fmt.Errorf("%s requires args.level, 0-100", cmd.Name)
		}
		return []string{fmt.Sprintf("set_volume lineout %d", lvl)}, nil
	case didoCmdMuteLineIn:
		return []string{"set_volume linein mute"}, nil
	case didoCmdUnmuteLineIn:
		return []string{"set_volume linein unmute"}, nil
	case didoCmdRecallPreset:
		name, _ := cmd.Args["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("%s requires args.name, a preset name without spaces", cmd.Name)
		}
		return []string{"preset_recall " + name}, nil
	case didoCmdLockButtons:
		return []string{"button_lock enable"}, nil
	case didoCmdUnlock:
		return []string{"button_lock disable"}, nil
	}
	return nil, fmt.Errorf("aurora_dido: unsupported command %q", cmd.Name)
}

// didoInputArg reads args.input as in1..in4 (1-4 or "in2" both work).
func didoInputArg(cmd device.CommandRequest) (string, error) {
	var s string
	if n, ok := didoIntArg(cmd.Args, "input"); ok {
		s = strconv.Itoa(n)
	} else {
		s, _ = cmd.Args["input"].(string)
	}
	in := didoInput(s)
	switch in {
	case "in1", "in2", "in3", "in4":
		return in, nil
	}
	return "", fmt.Errorf("%s requires args.input, 1-4", cmd.Name)
}

// didoIntArg reads a whole number from args, sent as a number or text.
func didoIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		if v == float64(int(v)) {
			return int(v), true
		}
	case int:
		return v, true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, true
		}
	}
	return 0, false
}

// sendReboot writes reboot and takes whatever comes back. The device
// may ack or may just drop the connection; both are a reboot.
func (a *AuroraDIDOAdapter) sendReboot(ctx context.Context, started time.Time) (*device.CommandResponse, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn, dec := a.conn, a.dec
	a.connMu.Unlock()
	if conn == nil || dec == nil {
		return nil, errors.New("aurora_dido: not connected")
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte("reboot\r\n")); err != nil {
		a.dropConn(conn)
		return nil, fmt.Errorf("aurora_dido write reboot: %w", err)
	}
	resp := map[string]any{}
	decErr := dec.Decode(&resp)
	a.dropConn(conn) // going away either way; the next poll reconnects
	if decErr != nil {
		resp = map[string]any{"status": "REBOOTING", "note": "device closed the connection without a reply, as it may on reboot"}
	}
	raw, _ := json.Marshal(resp)
	return &device.CommandResponse{Raw: string(raw), Parsed: resp, Latency: time.Since(started)}, nil
}

// ── Request/response ──────────────────────────────────────────────────────────

// request sends one command and returns its JSON reply. A reply whose
// status isn't SUCCESS is an error, with the device's own message.
func (a *AuroraDIDOAdapter) request(ctx context.Context, cmd string) (map[string]any, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn, dec := a.conn, a.dec
	a.connMu.Unlock()
	if conn == nil || dec == nil {
		return nil, errors.New("aurora_dido: not connected")
	}

	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		a.dropConn(conn)
		return nil, fmt.Errorf("aurora_dido write %q: %w", cmd, err)
	}
	resp := map[string]any{}
	if err := dec.Decode(&resp); err != nil {
		// A failed decoder stays failed, and a late reply would answer
		// the next request: this connection is done.
		snippet := a.rawTail(conn, 160)
		a.dropConn(conn)
		if snippet != "" {
			return nil, fmt.Errorf("aurora_dido decode reply to %q: %w (device sent: %q)", cmd, err, snippet)
		}
		return nil, fmt.Errorf("aurora_dido decode reply to %q: %w", cmd, err)
	}

	switch status, _ := resp["status"].(string); strings.ToUpper(status) {
	case "", "SUCCESS", "OK", "PROCESSING":
		return resp, nil
	default:
		msg := didoText(resp["error"])
		if msg == "" || strings.EqualFold(msg, "null") {
			msg = status
		}
		return resp, fmt.Errorf("aurora_dido: %s (cmd %q)", msg, cmd)
	}
}

func (a *AuroraDIDOAdapter) dropConn(conn net.Conn) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn == conn {
		_ = a.conn.Close()
		a.conn = nil
		a.dec = nil
	}
}

func (a *AuroraDIDOAdapter) rawTail(conn net.Conn, n int) string {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn != conn || a.raw == nil {
		return ""
	}
	b := a.raw.bytes()
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

// ── Capabilities ─────────────────────────────────────────────────────────────

var auroraDIDOCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: false, Off: false},
	Commands: []string{
		didoCmdRouteOut1, didoCmdRouteOut2, didoCmdRouteOut3, didoCmdRouteOut4, didoCmdRouteAll,
		didoCmdSetVolume, didoCmdMuteLineIn, didoCmdUnmuteLineIn,
		didoCmdRecallPreset,
		didoCmdLockButtons, didoCmdUnlock,
		didoCmdReboot,
	},
	Metrics: []string{
		"fw_version", "api_version", "serial_number", "mac", "ip_address", "ip_mode",
		"out1_source", "out2_source", "out3_source", "out4_source",
		"resolution_mode",
		"out1_resolution", "out2_resolution", "out3_resolution", "out4_resolution",
		"lineout_audio", "dante_audio", "out1_audio", "out2_audio", "out3_audio", "out4_audio",
		"lineout_volume", "linein_volume", "linein_mute",
		"buttons_locked",
		"presets", "preset_count",
		"response_ms",
	},
}

func (a *AuroraDIDOAdapter) Capabilities() device.Capabilities {
	return auroraDIDOCapabilities
}
