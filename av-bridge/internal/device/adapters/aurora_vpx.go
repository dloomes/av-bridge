package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// AuroraVPXAdapter — Aurora Multimedia VPX Series AV-over-IP encoder/decoder.
// Reference: VPX Series Protocol Guide v1.2.7 (Aurora Multimedia Corp.).
//
// Wire protocol: single-line text command terminated by \r\n, device replies
// with a single-line JSON object. Default TCP port is 6970. The API is
// described as "Telnet"; we reuse the shared IAC filter to strip any
// negotiation bytes that arrive at connect time.
//
// The device operates as either an encoder or a decoder. We auto-detect at
// connect time via `get settings` (hdmi.tx == "y" means encoder). Some
// commands are mode-specific and are rejected here rather than sent to
// the device — the device would reject with INVALID_PARAMETER anyway, but
// pre-check is cheaper and gives a clearer error to the operator.
//
// Background commands (status == "PROCESSING") are not chased. Poll uses
// only immediate commands; SendCommand's join/leave/start/stop return the
// PROCESSING status straight through so callers can see it.
type AuroraVPXAdapter struct {
	device.Base
	address string

	connMu sync.Mutex
	conn   net.Conn
	dec    *json.Decoder

	// cmdMu serialises in-flight commands so Poll doesn't interleave with
	// SendCommand. Aurora VPX serves strictly one request/response per
	// connection.
	cmdMu sync.Mutex

	// Populated on Connect and refreshed on Poll. modeMu guards both.
	// mode is "encoder", "decoder", or "" (unknown — mode-specific commands
	// refuse to dispatch until we've seen a `get settings` reply).
	modeMu    sync.RWMutex
	mode      string
	fwVersion string
}

const auroraVPXDefaultPort = 6970

func NewAuroraVPXAdapter(cfg config.DeviceConfig) *AuroraVPXAdapter {
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, auroraVPXDefaultPort)
	}
	return &AuroraVPXAdapter{
		Base:    device.NewBase(cfg),
		address: addr,
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *AuroraVPXAdapter) Connect(ctx context.Context) error {
	log := slog.With("device", a.Cfg.ID, "address", a.address)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora_vpx connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}

	a.connMu.Lock()
	// Close any prior conn — a reconnect must not leak the previous socket.
	if a.conn != nil {
		_ = a.conn.Close()
	}
	a.conn = conn
	a.dec = json.NewDecoder(bufio.NewReader(newTelnetFilter(conn)))
	a.connMu.Unlock()

	// Probe with `version` — no side effects, minimal response, confirms the
	// telnet handshake completed and we're getting JSON back.
	resp, err := a.request(ctx, "version")
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("aurora_vpx probe %s: %w", a.Cfg.ID, err)
	}
	if fw, ok := resp["fw_version"].(string); ok {
		a.modeMu.Lock()
		a.fwVersion = fw
		a.modeMu.Unlock()
	}

	// Detect mode. Failure here isn't fatal — Poll retries — but the log
	// helps operators spot config drift quickly.
	if err := a.refreshMode(ctx); err != nil {
		log.Warn("aurora_vpx mode detect failed", "error", err)
	}

	a.SetStatus(device.StatusOnline)
	log.Info("aurora_vpx connected", "fw_version", a.getFWVersion(), "mode", a.getMode())
	return nil
}

func (a *AuroraVPXAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn != nil {
		err := a.conn.Close()
		a.conn = nil
		a.dec = nil
		a.SetStatus(device.StatusOffline)
		return err
	}
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ──────────────────────────────────────────────────────────────────────

func (a *AuroraVPXAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	started := time.Now()
	t := a.BaseTelemetry()
	t.Metrics = map[string]any{}

	// `get status` is the primary health signal. If it fails we're offline
	// — no point issuing the rest.
	status, err := a.request(ctx, "get status")
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		_ = a.Disconnect() // force reconnect next tick
		return t, nil
	}

	state, _ := status["result"].(string)
	devStatus := mapVPXStatus(state)
	a.SetStatus(devStatus)
	t.Status = devStatus
	t.Metrics["state"] = state

	// The remaining reads are best-effort — a firmware that doesn't
	// support one shouldn't blank the whole telemetry payload.
	if settings, err := a.request(ctx, "get settings"); err == nil {
		a.applySettings(settings, t)
	}
	if hp, err := a.request(ctx, "get hotplug_status"); err == nil {
		if r, ok := hp["result"].(map[string]any); ok {
			if v, ok := r["IN1"].(string); ok {
				t.Metrics["hdmi_hpd_in1"] = v == "1"
			}
			if v, ok := r["IN2"].(string); ok {
				t.Metrics["hdmi_hpd_in2"] = v == "1"
			}
		}
	}
	if ls, err := a.request(ctx, "get linkspeed"); err == nil {
		if r, ok := ls["result"].(map[string]any); ok {
			if v, ok := r["rj45"].(string); ok {
				t.Metrics["link_speed_rj45"] = v
			}
			if v, ok := r["sfp"].(string); ok {
				t.Metrics["link_speed_sfp"] = v
			}
		}
	}
	if enc, err := a.request(ctx, "get video_encr"); err == nil {
		if v, ok := enc["result"].(string); ok {
			t.Metrics["video_hdcp"] = v
		}
	}

	if fw := a.getFWVersion(); fw != "" {
		t.Metrics["fw_version"] = fw
	}
	if mode := a.getMode(); mode != "" {
		t.Metrics["mode"] = mode
	}
	t.Metrics["response_ms"] = time.Since(started).Milliseconds()
	return t, nil
}

// applySettings pulls the fields we care about out of the `get settings`
// response and folds them into telemetry metrics. The full response is
// large (mac + hostname + full stream + audio + video geometry); we
// surface only what a fleet-monitoring view actually needs.
func (a *AuroraVPXAdapter) applySettings(resp map[string]any, t *device.Telemetry) {
	settings, ok := resp["settings"].(map[string]any)
	if !ok {
		return
	}
	if v, ok := settings["mac"].(string); ok {
		t.Metrics["mac"] = v
	}
	if v, ok := settings["device_id"].(string); ok {
		t.Metrics["hostname"] = v
	}
	if v, ok := settings["FW_version"].(string); ok {
		t.Metrics["fw_version"] = v
		a.modeMu.Lock()
		a.fwVersion = v
		a.modeMu.Unlock()
	}
	if v, ok := settings["display_mode"].(string); ok {
		t.Metrics["display_mode"] = v
	}
	if hdmi, ok := settings["hdmi"].(map[string]any); ok {
		// hdmi.tx doubles as the encoder/decoder discriminator — refresh
		// the cached mode on every poll so a runtime `set mode` flip is
		// picked up within a cycle.
		if tx, ok := hdmi["tx"].(string); ok {
			mode := "decoder"
			if tx == "y" {
				mode = "encoder"
			}
			a.modeMu.Lock()
			a.mode = mode
			a.modeMu.Unlock()
			t.Metrics["mode"] = mode
		}
		if v, ok := hdmi["hdcp"].(string); ok {
			t.Metrics["hdmi_hdcp"] = v
		}
		if v, ok := hdmi["local_display_source"].(string); ok {
			t.Metrics["local_display_source"] = v
		}
		if v, ok := hdmi["stream_source"].(string); ok && v != "" {
			t.Metrics["stream_source"] = v
		}
	}
	if ip, ok := settings["ip"].(map[string]any); ok {
		if v, ok := ip["address"].(string); ok {
			t.Metrics["ip_address"] = v
		}
		if v, ok := ip["mode"].(string); ok {
			t.Metrics["ip_mode"] = v
		}
	}
	if streams, ok := settings["streams"].(map[string]any); ok {
		if video, ok := streams["video"].(map[string]any); ok {
			if v, ok := video["status"].(string); ok {
				t.Metrics["stream_status"] = v
			}
			if v, ok := video["h_size"].(string); ok {
				t.Metrics["video_h_size"] = v
			}
			if v, ok := video["v_size"].(string); ok {
				t.Metrics["video_v_size"] = v
			}
			if v, ok := video["fps"].(string); ok {
				t.Metrics["video_fps"] = v
			}
			if v, ok := video["remote_hostname"].(string); ok && v != "" {
				t.Metrics["remote_hostname"] = v
			}
		}
	}
}

// mapVPXStatus turns the device's state-machine value into our tri-state.
// See §4.7.4.1 of the protocol guide.
func mapVPXStatus(state string) device.Status {
	switch state {
	case "s_srv_on":
		return device.StatusOnline
	case "s_init", "s_search", "s_attaching", "s_start_srv_lp", "s_start_srv_hp":
		// Transient. Treat as degraded so alerts don't fire on a fresh
		// device that's still coming up.
		return device.StatusDegraded
	case "s_stop", "s_error", "s_idle", "":
		return device.StatusOffline
	default:
		// Unknown state — better to log it than to guess.
		return device.StatusDegraded
	}
}

// ── Commands ──────────────────────────────────────────────────────────────────

// Named commands the adapter exposes to SendCommand. Kept intentionally
// small — the device supports 100+ commands, but most are one-shot config
// mutations that belong in a dedicated setup UI, not a fleet-monitoring
// operator's palette.
const (
	vpxCmdReboot        = "reboot"
	vpxCmdIdentify      = "identify"
	vpxCmdStopIdentify  = "stop_identify"
	vpxCmdMute          = "mute"
	vpxCmdUnmute        = "unmute"
	vpxCmdJoin          = "join"
	vpxCmdLeave         = "leave"
	vpxCmdStartStream   = "start_stream"
	vpxCmdStopStream    = "stop_stream"
)

func (a *AuroraVPXAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	started := time.Now()

	wire, err := a.translateCommand(cmd)
	if err != nil {
		return nil, err
	}

	// reboot is fire-and-forget by design. The device sometimes writes a
	// {"status":"PROCESSING"} ack before dropping the telnet session, and
	// sometimes just closes the socket. Treat any read failure after a
	// successful write as a normal reboot; the next Poll will reconnect.
	if cmd.Name == vpxCmdReboot {
		return a.sendReboot(ctx, wire, started)
	}

	resp, err := a.request(ctx, wire)
	if err != nil {
		return nil, err
	}

	raw, _ := json.Marshal(resp)
	return &device.CommandResponse{
		Raw:     string(raw),
		Parsed:  resp,
		Latency: time.Since(started),
	}, nil
}

// sendReboot writes the reboot command, then briefly waits for the optional
// PROCESSING ack the firmware may emit before it cuts the socket. Any read
// error — EOF, deadline, connection reset — is normal here, so we synthesize
// a {"status":"REBOOTING"} response rather than surfacing a failure to the
// caller. The connection is force-closed so the Poll goroutine reconnects
// cleanly once the device is back up.
func (a *AuroraVPXAdapter) sendReboot(ctx context.Context, wire string, started time.Time) (*device.CommandResponse, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn, dec := a.conn, a.dec
	a.connMu.Unlock()

	if conn == nil || dec == nil {
		return nil, errors.New("aurora_vpx: not connected")
	}

	// Short read window — if the device is going to ack, it does so almost
	// immediately. We don't want to hold the caller for the full poll
	// interval waiting for a reply that will never come.
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write([]byte(wire + "\r\n")); err != nil {
		// Write failure is a real error — the device isn't reachable yet.
		return nil, fmt.Errorf("aurora_vpx write %q: %w", wire, err)
	}

	resp := map[string]any{}
	decErr := dec.Decode(&resp)

	// Whatever happened, the connection is going away. Drop it so the next
	// Poll opens a fresh one instead of blocking on a dead socket.
	a.connMu.Lock()
	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
		a.dec = nil
	}
	a.connMu.Unlock()

	if decErr != nil {
		resp = map[string]any{
			"status":  "REBOOTING",
			"command": wire,
			"note":    "device closed connection without ack — this is normal for reboot",
		}
	}

	raw, _ := json.Marshal(resp)
	return &device.CommandResponse{
		Raw:     string(raw),
		Parsed:  resp,
		Latency: time.Since(started),
	}, nil
}

// translateCommand maps our stable named-command surface to the exact wire
// strings the VPX firmware expects. Kept as a switch (not a map) so mode
// gating + arg extraction sit right next to the mapping.
func (a *AuroraVPXAdapter) translateCommand(cmd device.CommandRequest) (string, error) {
	mode := a.getMode()
	switch cmd.Name {
	case vpxCmdReboot:
		return "reboot", nil
	case vpxCmdIdentify:
		return "set led blink", nil
	case vpxCmdStopIdentify:
		return "set led normal", nil

	case vpxCmdMute:
		if err := requireDecoder(mode, cmd.Name); err != nil {
			return "", err
		}
		return "mute av on", nil
	case vpxCmdUnmute:
		if err := requireDecoder(mode, cmd.Name); err != nil {
			return "", err
		}
		return "mute av off", nil

	case vpxCmdJoin:
		if err := requireDecoder(mode, cmd.Name); err != nil {
			return "", err
		}
		// Accept either `encoder` (hostname) or `encoder_ip`. Prefer
		// hostname when both are present; the device accepts both but
		// hostname survives DHCP-lease churn.
		enc, _ := cmd.Args["encoder"].(string)
		if enc == "" {
			enc, _ = cmd.Args["encoder_ip"].(string)
		}
		if enc == "" {
			return "", fmt.Errorf("%s requires args.encoder or args.encoder_ip", cmd.Name)
		}
		// `display` optional — auto-swap the decoder's output to STREAM.
		suffix := ""
		if wantDisplay, _ := cmd.Args["display"].(bool); wantDisplay {
			suffix = " display"
		}
		return "join ALL " + enc + suffix, nil
	case vpxCmdLeave:
		if err := requireDecoder(mode, cmd.Name); err != nil {
			return "", err
		}
		return "leave ALL", nil

	case vpxCmdStartStream:
		if err := requireEncoder(mode, cmd.Name); err != nil {
			return "", err
		}
		return "start HDMI", nil
	case vpxCmdStopStream:
		if err := requireEncoder(mode, cmd.Name); err != nil {
			return "", err
		}
		return "stop HDMI", nil
	}
	return "", fmt.Errorf("aurora_vpx: unsupported command %q", cmd.Name)
}

func requireEncoder(mode, name string) error {
	if mode == "encoder" {
		return nil
	}
	if mode == "" {
		return fmt.Errorf("aurora_vpx: mode not detected yet; retry after next poll (%s requires encoder)", name)
	}
	return fmt.Errorf("aurora_vpx: %s requires encoder mode, device is %s", name, mode)
}

func requireDecoder(mode, name string) error {
	if mode == "decoder" {
		return nil
	}
	if mode == "" {
		return fmt.Errorf("aurora_vpx: mode not detected yet; retry after next poll (%s requires decoder)", name)
	}
	return fmt.Errorf("aurora_vpx: %s requires decoder mode, device is %s", name, mode)
}

// ── Request/response ──────────────────────────────────────────────────────────

// request sends a single command and returns the parsed JSON response.
// Serialised via cmdMu so Poll and SendCommand cannot interleave over
// the shared connection.
func (a *AuroraVPXAdapter) request(ctx context.Context, cmd string) (map[string]any, error) {
	a.cmdMu.Lock()
	defer a.cmdMu.Unlock()

	a.connMu.Lock()
	conn, dec := a.conn, a.dec
	a.connMu.Unlock()

	if conn == nil || dec == nil {
		return nil, errors.New("aurora_vpx: not connected")
	}

	// Per-request deadline — ctx cancellation and a hard cap so a hung
	// device can't stall the poll goroutine indefinitely.
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		return nil, fmt.Errorf("aurora_vpx write %q: %w", cmd, err)
	}

	resp := map[string]any{}
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("aurora_vpx decode reply to %q: %w", cmd, err)
	}

	// The device reports ERROR with a human-readable string in `error`.
	// Callers can inspect the map themselves, but surface the failure so
	// SendCommand's caller sees it as an error rather than silent success.
	if status, _ := resp["status"].(string); status == "ERROR" {
		errStr, _ := resp["error"].(string)
		if errStr == "" || errStr == "NULL" {
			errStr = "unspecified"
		}
		return resp, fmt.Errorf("aurora_vpx: %s (cmd %q)", errStr, cmd)
	}
	return resp, nil
}

// refreshMode issues `get settings` and caches the encoder/decoder role.
// Called at Connect and again on every Poll (via applySettings), so a
// device that gets flipped from encoder to decoder in the field is picked
// up within one poll cycle.
func (a *AuroraVPXAdapter) refreshMode(ctx context.Context) error {
	resp, err := a.request(ctx, "get settings")
	if err != nil {
		return err
	}
	settings, ok := resp["settings"].(map[string]any)
	if !ok {
		return errors.New("aurora_vpx: settings payload missing")
	}
	hdmi, ok := settings["hdmi"].(map[string]any)
	if !ok {
		return errors.New("aurora_vpx: settings.hdmi missing")
	}
	tx, _ := hdmi["tx"].(string)
	mode := "decoder"
	if tx == "y" {
		mode = "encoder"
	}
	a.modeMu.Lock()
	a.mode = mode
	if fw, ok := settings["FW_version"].(string); ok {
		a.fwVersion = fw
	}
	a.modeMu.Unlock()
	return nil
}

func (a *AuroraVPXAdapter) getMode() string {
	a.modeMu.RLock()
	defer a.modeMu.RUnlock()
	return a.mode
}

func (a *AuroraVPXAdapter) getFWVersion() string {
	a.modeMu.RLock()
	defer a.modeMu.RUnlock()
	return a.fwVersion
}

// ── Capabilities ─────────────────────────────────────────────────────────────

// The command surface + metric names are static across the fleet. Mode-
// specific commands (mute/join/leave for decoders; start/stop for
// encoders) are advertised as available on both modes here — gating
// happens at dispatch time inside translateCommand — because a routine
// builder targeting device_type=vpx doesn't know per-device mode in
// advance and shouldn't be prevented from authoring the step.
var auroraVPXCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: false, Off: false},
	Commands: []string{
		vpxCmdReboot,
		vpxCmdIdentify,
		vpxCmdStopIdentify,
		vpxCmdMute,
		vpxCmdUnmute,
		vpxCmdJoin,
		vpxCmdLeave,
		vpxCmdStartStream,
		vpxCmdStopStream,
	},
	Metrics: []string{
		"state",
		"mode",
		"fw_version",
		"mac",
		"hostname",
		"ip_address",
		"ip_mode",
		"display_mode",
		"hdmi_hdcp",
		"hdmi_hpd_in1",
		"hdmi_hpd_in2",
		"local_display_source",
		"stream_source",
		"stream_status",
		"video_h_size",
		"video_v_size",
		"video_fps",
		"video_hdcp",
		"link_speed_rj45",
		"link_speed_sfp",
		"remote_hostname",
		"response_ms",
	},
}

func (a *AuroraVPXAdapter) Capabilities() device.Capabilities {
	return auroraVPXCapabilities
}
