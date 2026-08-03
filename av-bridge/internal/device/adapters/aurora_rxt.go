package adapters

import (
	"bufio"
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

// AuroraAdapter communicates with Aurora Multimedia RXT-x WM (wall-mount)
// control touch panels using the RXT Control Application API.
// Reference: RXT-4/8 Control Application API User Guide v1.0.3
// Minimum firmware: RXT-x 2.2.0
//
// Wire protocol: single-line text command terminated by \r\n, device replies
// with a single JSON object. Default TCP port is 6975. The protocol is
// described as "Telnet"; in practice the IAC filter shared with the Tesira
// adapter handles any negotiation bytes that arrive at connect time and
// passes plain bytes through otherwise.
//
// Background commands (status:"PROCESSING" with a request_id) are not
// chased — the adapter returns the immediate response. None of the commands
// used by Poll fall into that category.
type AuroraAdapter struct {
	device.Base
	address string

	connMu sync.Mutex
	conn   net.Conn
	dec    *json.Decoder

	// cmdMu serialises in-flight commands so a Poll doesn't interleave with
	// a SendCommand. The API is strictly one request/response per connection.
	cmdMu sync.Mutex
}

const auroraDefaultPort = 6975

func NewAuroraAdapter(cfg config.DeviceConfig) *AuroraAdapter {
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, auroraDefaultPort)
	}
	return &AuroraAdapter{
		Base:    device.NewBase(cfg),
		address: addr,
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *AuroraAdapter) Connect(ctx context.Context) error {
	log := slog.With("device", a.Cfg.ID, "address", a.address)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	log.Info("aurora tcp connected")

	reader := bufio.NewReader(newTelnetFilter(conn))

	a.connMu.Lock()
	a.conn = conn
	a.dec = json.NewDecoder(reader)
	a.connMu.Unlock()

	// Probe: `version` is a no-side-effect command on every supported firmware.
	log.Info("aurora sending probe", "cmd", "version")
	resp, err := a.exec(ctx, "version", 3*time.Second)
	if err != nil {
		a.closeConn()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora probe: %w", err)
	}
	if status, _ := resp["status"].(string); status != "SUCCESS" {
		a.closeConn()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora probe returned status %q (error %v)", status, resp["error"])
	}

	a.captureStaticInfo(ctx, resp)
	a.SetStatus(device.StatusOnline)
	log.Info("aurora connected")
	return nil
}

// captureStaticInfo populates Cfg.Tags with device inventory fields that
// don't change at runtime (model, serial, MAC, firmware, hostname). Called
// once on successful Connect with the version probe response, then issues a
// few more queries. Failures are logged at debug and skipped.
func (a *AuroraAdapter) captureStaticInfo(ctx context.Context, versionResp map[string]any) {
	setTag := func(k, v string) {
		if v == "" {
			return
		}
		if a.Cfg.Tags == nil {
			a.Cfg.Tags = map[string]string{}
		}
		a.Cfg.Tags[k] = v
	}

	if v, ok := versionResp["version"].(string); ok {
		setTag("firmware_version", v)
	}
	if v, ok := versionResp["date"].(string); ok {
		setTag("firmware_date", v)
	}

	probe := func(cmd string, fn func(map[string]any)) {
		r, err := a.exec(ctx, cmd, 2*time.Second)
		if err != nil {
			slog.Debug("aurora static info query failed", "device", a.Cfg.ID, "cmd", cmd, "error", err)
			return
		}
		if status, _ := r["status"].(string); status != "SUCCESS" {
			return
		}
		fn(r)
	}

	probe("serial_num", func(r map[string]any) {
		if v, ok := r["serial_num"].(string); ok {
			setTag("serial", v)
		}
	})
	probe("model", func(r map[string]any) {
		if v, ok := r["model"].(string); ok {
			setTag("model", v)
		}
	})
	probe("get_machinetype", func(r map[string]any) {
		if v, ok := r["machine_type"].(string); ok {
			setTag("machine_type", v)
		}
	})
	probe("get_hostname", func(r map[string]any) {
		if v, ok := r["hostname"].(string); ok {
			setTag("hostname", v)
		}
	})
	probe("api_version", func(r map[string]any) {
		if v, ok := r["api_version"].(string); ok {
			setTag("api_version", v)
		}
	})
	probe("get_ip 1", func(r map[string]any) {
		net, ok := r["network_settings"].(map[string]any)
		if !ok {
			return
		}
		if v, ok := net["MAC_address"].(string); ok {
			setTag("mac_address", v)
		}
		if v, ok := net["IP_address"].(string); ok {
			setTag("ip_address", v)
		}
	})
}

func (a *AuroraAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return a.closeConn()
}

func (a *AuroraAdapter) closeConn() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.dec = nil
	if a.conn == nil {
		return nil
	}
	err := a.conn.Close()
	a.conn = nil
	return err
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll samples runtime state: built-in speaker volume/mute, LCD brightness +
// dim timeout, ambient light + proximity sensors, auto-brightness mode, and
// both relay output states. Each query is best-effort — failures degrade the
// metric set but don't fail the poll, so a partial Tesira panel with no IR
// blaster (etc.) still surfaces useful data.
func (a *AuroraAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	metrics := map[string]any{}
	start := time.Now()

	run := func(cmd string, fn func(map[string]any)) {
		r, err := a.exec(ctx, cmd, 3*time.Second)
		if err != nil {
			slog.Debug("aurora poll command failed", "device", a.Cfg.ID, "cmd", cmd, "error", err)
			return
		}
		if status, _ := r["status"].(string); status != "SUCCESS" {
			slog.Debug("aurora poll non-success", "device", a.Cfg.ID, "cmd", cmd, "status", status, "error", r["error"])
			return
		}
		fn(r)
	}

	run("get_volume builtin_speaker", func(r map[string]any) {
		if audio, ok := r["audio"].(map[string]any); ok {
			if v, ok := audio["volume"].(string); ok {
				if n, err := strconv.Atoi(v); err == nil {
					metrics["volume"] = n
				}
			}
			if v, ok := audio["mute"].(string); ok {
				metrics["mute"] = v == "on"
			}
		}
	})
	run("get_lcd", func(r map[string]any) {
		lcd, ok := r["LCD"].(map[string]any)
		if !ok {
			return
		}
		if v, ok := lcd["brightness"].(string); ok {
			if n, err := strconv.Atoi(v); err == nil {
				metrics["lcd_brightness"] = n
			}
		}
		if v, ok := lcd["timeout"].(string); ok {
			if n, err := strconv.Atoi(v); err == nil {
				metrics["lcd_timeout"] = n
			}
		}
	})
	run("get_sensor", func(r map[string]any) {
		s, ok := r["sensor"].(map[string]any)
		if !ok {
			return
		}
		if v, ok := s["lux"].(string); ok {
			if n, err := strconv.Atoi(v); err == nil {
				metrics["lux"] = n
			}
		}
		if v, ok := s["proximity"].(string); ok {
			if n, err := strconv.Atoi(v); err == nil {
				metrics["proximity"] = n
			}
		}
	})
	run("auto_brightness status", func(r map[string]any) {
		if v, ok := r["auto_brightness"].(string); ok {
			metrics["auto_brightness"] = v == "enabled"
		}
	})
	run("get_relay 1", func(r map[string]any) {
		if rel, ok := r["relay"].(map[string]any); ok {
			if v, ok := rel["status"].(string); ok {
				metrics["relay_1"] = v
			}
		}
	})
	run("get_relay 2", func(r map[string]any) {
		if rel, ok := r["relay"].(map[string]any); ok {
			if v, ok := rel["status"].(string); ok {
				metrics["relay_2"] = v
			}
		}
	})
	run("event_manager status", func(r map[string]any) {
		if v, ok := r["event_manager"].(string); ok {
			metrics["event_manager"] = v == "enabled"
		}
	})

	metrics["response_ms"] = time.Since(start).Milliseconds()
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// ── SendCommand ───────────────────────────────────────────────────────────────

// auroraVerbs is the set of first tokens we'll accept as a raw API command
// passed straight through to the device. Adding to commands: in the device
// config remains the more discoverable path.
var auroraVerbs = map[string]bool{
	"enable_rs232": true, "disable_rs232": true, "config_rs232": true, "get_rs232": true,
	"read_io": true, "set_io": true, "config_io": true, "get_io": true,
	"version": true, "serial_num": true, "system_info": true,
	"factory_default": true, "reboot": true,
	"get_ip": true, "get_static_ip": true, "set_ip": true,
	"set_time": true, "get_time": true,
	"erase_all_files": true, "api_version": true, "fw_update": true,
	"kiosk": true, "debug_console": true,
	"get_dioportcount": true, "get_serialportcount": true, "get_netcardcount": true,
	"get_irportcount": true, "get_relayportcount": true,
	"set_lcd": true, "get_lcd": true,
	"event_manager": true,
	"list_irgroups": true, "list_ircmds": true, "send_ir": true, "get_irfile_header": true,
	"set_relay": true, "get_relay": true, "relay_toggle": true,
	"get_machinetype": true, "get_machinename": true,
	"set_hostname": true, "get_hostname": true,
	"config_led": true, "get_led": true,
	"model":               true,
	"set_ftp_pass":        true,
	"get_sensor":          true,
	"auto_brightness":     true,
	"set_volume":          true,
	"get_volume":          true,
	"test_audio":          true,
	"set_default_speaker": true,
	"config_system_bar":   true,
	"get_system_bar":      true,
}

// SendCommand resolves a request to an Aurora API command and executes it.
//
// Built-in named shortcuts:
//
//	reboot, factory_default, test_audio
//	mute / unmute                  (builtin_speaker)
//	auto_brightness_on / _off
//	relay_1_on / _off / _toggle
//	relay_2_on / _off / _toggle
//
// Commands not in the built-in list are looked up in the device's commands:
// config map, then — as a last resort — passed straight through if the first
// token is a known API verb. Args (req.Args) are substituted into the command
// via {key} placeholders before sending.
func (a *AuroraAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	raw := a.resolveCommand(req)
	if raw == "" {
		return nil, fmt.Errorf("aurora: unknown command %q — add it to commands: in config or use a raw API command", req.Name)
	}
	for k, v := range req.Args {
		raw = strings.ReplaceAll(raw, "{"+k+"}", fmt.Sprintf("%v", v))
	}

	start := time.Now()
	resp, err := a.exec(ctx, raw, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("aurora command %q: %w", req.Name, err)
	}

	rawJSON, _ := json.Marshal(resp)
	parsed := map[string]any{"status": resp["status"]}
	if errObj := resp["error"]; errObj != nil {
		parsed["error"] = errObj
	}
	if status, ok := resp["status"].(string); ok && status != "SUCCESS" {
		return &device.CommandResponse{
				Raw: string(rawJSON), Parsed: parsed, Latency: time.Since(start),
			},
			fmt.Errorf("aurora API returned %q: %v", status, resp["error"])
	}
	return &device.CommandResponse{
		Raw:     string(rawJSON),
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

func (a *AuroraAdapter) resolveCommand(req device.CommandRequest) string {
	switch req.Name {
	case "reboot":
		return "reboot"
	case "factory_default":
		return "factory_default"
	case "test_audio":
		return "test_audio"
	case "mute":
		return "set_volume builtin_speaker mute"
	case "unmute":
		return "set_volume builtin_speaker unmute"
	case "auto_brightness_on":
		return "auto_brightness enable"
	case "auto_brightness_off":
		return "auto_brightness disable"
	case "relay_1_on":
		return "set_relay 1 on"
	case "relay_1_off":
		return "set_relay 1 off"
	case "relay_1_toggle":
		return "relay_toggle 1"
	case "relay_2_on":
		return "set_relay 2 on"
	case "relay_2_off":
		return "set_relay 2 off"
	case "relay_2_toggle":
		return "relay_toggle 2"
	}

	if raw, ok := a.Cfg.Commands[req.Name]; ok {
		return raw
	}

	// Raw API command passthrough — first token must be a known verb.
	parts := strings.SplitN(req.Name, " ", 2)
	if auroraVerbs[parts[0]] {
		return req.Name
	}
	return ""
}

// ── Capabilities ─────────────────────────────────────────────────────────────
//
// Static capability declaration for the Aurora RXT touch panel range.
// Aurora exposes the built-in speaker, LCD brightness, relays and the
// proximity sensor — no dedicated power state, so power_on/off stay
// false. Reboot is a command, not a power action.

var auroraCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: false, Off: false},
	Commands: []string{
		"reboot", "factory_default", "test_audio",
		"mute", "unmute",
		"auto_brightness_on", "auto_brightness_off",
		"relay_1_on", "relay_1_off", "relay_1_toggle",
		"relay_2_on", "relay_2_off", "relay_2_toggle",
	},
	Metrics: []string{
		"volume", "mute",
		"lcd_brightness", "lcd_timeout",
		"lux", "proximity",
		"auto_brightness",
		"relay_1", "relay_2",
		"event_manager",
		"response_ms",
	},
}

func (a *AuroraAdapter) Capabilities() device.Capabilities {
	return auroraCapabilities
}

// ── Transport ────────────────────────────────────────────────────────────────

// exec writes one command and reads one JSON response. Serialised via cmdMu
// so concurrent Poll and SendCommand calls don't share the wire.
func (a *AuroraAdapter) exec(ctx context.Context, cmd string, timeout time.Duration) (map[string]any, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn := a.conn
	dec := a.dec
	a.connMu.Unlock()
	if conn == nil || dec == nil {
		return nil, fmt.Errorf("aurora: not connected")
	}

	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}

	line := cmd
	if !strings.HasSuffix(line, "\r\n") {
		line += "\r\n"
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(line)); err != nil {
		return nil, fmt.Errorf("write %q: %w", cmd, err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	var resp map[string]any
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response to %q: %w", cmd, err)
	}
	return resp, nil
}
