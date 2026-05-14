package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// SonyAPIError is returned by call() when the Sony JSON-RPC response carries
// a non-empty "error" array. Sony uses small numeric codes — notably 3
// ("Illegal Argument"), 7 ("Illegal State"), 12 ("No Such Method") — which we
// surface so callers can branch on them.
type SonyAPIError struct {
	Code    int
	Message string
}

func (e *SonyAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("sony API error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("sony API error %d", e.Code)
}

// Sony error codes we branch on
const (
	sonyErrIllegalArgument = 3
	sonyErrIllegalState    = 7
)

// SonyBraviaAdapter communicates with Sony BRAVIA Professional Displays
// using Sony's REST API (JSON-RPC over HTTP).
// Reference: https://pro-bravia.sony.net/remote-display-control/rest-api/reference/
//
// Authentication uses a Pre-Shared Key (PSK) passed in the X-Auth-PSK
// header on every request — no login flow, no session management.
// A small number of services (system/getInterfaceInformation,
// system/getPowerStatus) are auth-level "none" and reachable without a PSK,
// which we exploit during Connect for a faster reachability probe.
//
// Sony's API is structured around named services accessible at:
//   http://<ip>/sony/<service>
// Services used here: system, avContent, audio.
//
// Config tags used:
//   - psk: Pre-Shared Key configured on the display (required for control)
//
// Enable on the display:
//   Settings → Network → Home Network → IP Control → Simple IP Control: On
//   Settings → Network → Home Network → Pre-Shared Key → set your PSK
type SonyBraviaAdapter struct {
	device.Base
	client  *http.Client
	baseURL string
	psk     string
}

// sonyRequest is the JSON-RPC envelope used by all Sony Bravia REST calls.
type sonyRequest struct {
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
	Version string `json:"version"`
}

// sonyResponse is the standard envelope returned by Sony Bravia REST calls.
type sonyResponse struct {
	Result []any  `json:"result"`
	Error  []any  `json:"error"`
	ID     int    `json:"id"`
}

func NewSonyBraviaAdapter(cfg config.DeviceConfig) *SonyBraviaAdapter {
	base := cfg.Address
	if !strings.HasPrefix(base, "http") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")

	return &SonyBraviaAdapter{
		Base:    device.NewBase(cfg),
		baseURL: base,
		psk:     cfg.Tags["psk"],
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

// Connect verifies the display is reachable, captures inventory information
// (model, serial, firmware, MAC, IP), and marks the device online.
//
// We first call getInterfaceInformation (auth-level "none") for a fast
// reachability probe that doesn't depend on the PSK being correct. Then we
// call getSystemInformation and getNetworkSettings to enrich tags.
func (a *SonyBraviaAdapter) Connect(ctx context.Context) error {
	if a.psk == "" {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("sony bravia %s: psk tag is required (set tags.psk in config)", a.Cfg.ID)
	}

	if iface, err := a.call(ctx, "system", "getInterfaceInformation", nil); err == nil && len(iface) > 0 {
		if info, ok := iface[0].(map[string]any); ok {
			a.setTags(map[string]string{
				"product":           stringVal(info, "productName"),
				"interface_version": stringVal(info, "interfaceVersion"),
				"server_name":       stringVal(info, "serverName"),
			})
		}
	}

	result, err := a.call(ctx, "system", "getSystemInformation", nil)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("sony bravia connect %s: %w", a.Cfg.ID, err)
	}
	if len(result) > 0 {
		if info, ok := result[0].(map[string]any); ok {
			a.setTags(map[string]string{
				"model":            stringVal(info, "model"),
				"serial":           stringVal(info, "serial"),
				"mac_address":      stringVal(info, "macAddr"),
				"firmware_version": stringVal(info, "fwVersion"),
				"generation":       stringVal(info, "generation"),
				"device_name":      stringVal(info, "name"),
				"language":         stringVal(info, "language"),
			})
		}
	}

	if net, err := a.call(ctx, "system", "getNetworkSettings", nil); err == nil {
		for _, raw := range net {
			iface, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ip := stringVal(iface, "ipAddrV4")
			if ip == "" {
				continue
			}
			tags := map[string]string{"ip_address": ip}
			if hw := stringVal(iface, "hwAddr"); hw != "" {
				tags["mac_address"] = hw
			}
			a.setTags(tags)
			break
		}
	}

	a.SetStatus(device.StatusOnline)
	return nil
}

func (a *SonyBraviaAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll queries power status, audio state, current input, and power-saving mode.
// Audio and input calls are skipped when the display is in standby so a powered-
// off display doesn't drag the poll out with errors that the docs explicitly
// allow ("Illegal State" while in standby).
func (a *SonyBraviaAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	metrics := map[string]any{}
	start := time.Now()

	powerResult, err := a.call(ctx, "system", "getPowerStatus", nil)
	if err != nil {
		a.SetStatus(device.StatusDegraded)
		t.Status = device.StatusDegraded
		t.Error = err.Error()
		return t, nil
	}

	powerStatus := ""
	if len(powerResult) > 0 {
		if ps, ok := powerResult[0].(map[string]any); ok {
			powerStatus = stringVal(ps, "status")
			metrics["power_status"] = powerStatus
		}
	}

	if psm, err := a.call(ctx, "system", "getPowerSavingMode", nil); err == nil && len(psm) > 0 {
		if m, ok := psm[0].(map[string]any); ok {
			metrics["power_saving_mode"] = stringVal(m, "mode")
		}
	}

	if powerStatus == "active" {
		if vol, err := a.call(ctx, "audio", "getVolumeInformation", nil); err == nil && len(vol) > 0 {
			for _, raw := range vol {
				v, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				target := stringVal(v, "target")
				if target == "" {
					target = "default"
				}
				metrics["volume_"+target] = intVal(v, "volume")
				metrics["max_volume_"+target] = intVal(v, "maxVolume")
				if muted, ok := v["mute"].(bool); ok {
					metrics["mute_"+target] = muted
				}
			}
		}

		if input, err := a.call(ctx, "avContent", "getPlayingContentInfo", nil); err == nil && len(input) > 0 {
			if ci, ok := input[0].(map[string]any); ok {
				metrics["current_input"] = stringVal(ci, "uri")
				metrics["input_title"] = stringVal(ci, "title")
				metrics["input_source"] = stringVal(ci, "source")
			}
		}
	}

	metrics["response_ms"] = time.Since(start).Milliseconds()
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// ── SendCommand ───────────────────────────────────────────────────────────────

// SendCommand supports the following named commands (configure in config.yaml):
//
//	power_on     — setPowerStatus active
//	power_off    — setPowerStatus standby
//	input_hdmi1  — setPlayContent extInput:hdmi?port=1
//	input_hdmi2  — setPlayContent extInput:hdmi?port=2
//	input_hdmi3  — setPlayContent extInput:hdmi?port=3
//	volume_up    — setAudioVolume (relative +1)
//	volume_down  — setAudioVolume (relative -1)
//	mute         — setAudioMute true
//	unmute       — setAudioMute false
//
// You can also pass a raw JSON-RPC method as the command name in the format
// "<service>/<method>" (e.g. "system/getPowerStatus") for direct API access.
func (a *SonyBraviaAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	start := time.Now()

	service, method, params, err := a.ResolveCommand(req)
	if err != nil {
		return nil, err
	}

	// Power-on fast path: BRAVIA displays in cold standby don't respond to
	// REST setPowerStatus, but they do wake on a WoL magic packet. Fire one
	// before the REST call. Best-effort — we still try REST regardless.
	if req.Name == "power_on" {
		if mac := a.Cfg.Tags["mac_address"]; mac != "" {
			ip := a.Cfg.Tags["ip_address"]
			if ip == "" {
				// Fall back to the address from config (strip http(s):// and port if present)
				ip = hostFromBaseURL(a.baseURL)
			}
			if werr := sendWakeOnLAN(mac, ip); werr != nil {
				slog.Warn("WoL packet failed", "device", a.Cfg.ID, "mac", mac, "error", werr)
			} else {
				slog.Info("WoL packets sent", "device", a.Cfg.ID, "mac", mac, "unicast_target", ip)
			}
		} else {
			slog.Warn("WoL skipped: no mac_address tag captured yet",
				"device", a.Cfg.ID, "hint", "set tags.mac_address in config, or wait for first successful connect")
		}
	}

	result, err := a.call(ctx, service, method, params)
	if err != nil && method == "setPowerStatus" {
		var apiErr *SonyAPIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case sonyErrIllegalArgument:
				// Param shape rejected — Sony's spec says string, some firmwares
				// say boolean. We default to boolean (what real-world Pro/consumer
				// displays accept); fall back to the spec string form if the
				// firmware insists.
				if alt := altPowerParams(params); alt != nil {
					slog.Info("setPowerStatus rejected, retrying with alternate param form",
						"device", a.Cfg.ID, "first_params", params, "retry_params", alt)
					r, rerr := a.call(ctx, service, method, alt)
					if rerr == nil {
						slog.Info("setPowerStatus alternate form accepted", "device", a.Cfg.ID)
						result, err = r, nil
					} else {
						slog.Warn("setPowerStatus alternate form also failed",
							"device", a.Cfg.ID, "first_error", apiErr.Error(), "retry_error", rerr.Error())
						err = fmt.Errorf("both forms rejected: primary=%w, fallback=%v", err, rerr)
					}
				}
			case sonyErrIllegalState:
				// "Illegal State" on setPowerStatus means the display is already
				// in the requested state (or transitioning). Idempotent calls
				// shouldn't surface as errors.
				slog.Info("setPowerStatus already in target state, treating as no-op",
					"device", a.Cfg.ID, "params", params)
				result, err = []any{}, nil
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sony bravia command %q: %w", req.Name, err)
	}

	raw, _ := json.Marshal(result)
	parsed := map[string]any{"result": result}

	return &device.CommandResponse{
		Raw:     string(raw),
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

// resolveCommand maps friendly command names to Sony API service/method/params.
func (a *SonyBraviaAdapter) ResolveCommand(req device.CommandRequest) (service, method string, params []any, err error) {
	// Allow raw service/method override
	if strings.Contains(req.Name, "/") {
		parts := strings.SplitN(req.Name, "/", 2)
		return parts[0], parts[1], nil, nil
	}

	// Named command lookup
	switch req.Name {
	case "power_on":
		// Most Sony BRAVIA firmwares (consumer-line and the Pro Display series
		// we've tested) want a boolean here, even though Sony's published spec
		// shows a string. We send boolean first and fall back to the spec form
		// in SendCommand if the firmware rejects it with code 3.
		return "system", "setPowerStatus", []any{map[string]any{"status": true}}, nil
	case "power_off":
		return "system", "setPowerStatus", []any{map[string]any{"status": false}}, nil
	case "input_hdmi1":
		return "avContent", "setPlayContent", []any{map[string]any{"uri": "extInput:hdmi?port=1"}}, nil
	case "input_hdmi2":
		return "avContent", "setPlayContent", []any{map[string]any{"uri": "extInput:hdmi?port=2"}}, nil
	case "input_hdmi3":
		return "avContent", "setPlayContent", []any{map[string]any{"uri": "extInput:hdmi?port=3"}}, nil
	case "input_hdmi4":
		return "avContent", "setPlayContent", []any{map[string]any{"uri": "extInput:hdmi?port=4"}}, nil
	case "mute":
		return "audio", "setAudioMute", []any{map[string]any{"status": true}}, nil
	case "unmute":
		return "audio", "setAudioMute", []any{map[string]any{"status": false}}, nil
	case "volume_up":
		return "audio", "setAudioVolume", []any{map[string]any{"target": "speaker", "volume": "+1"}}, nil
	case "volume_down":
		return "audio", "setAudioVolume", []any{map[string]any{"target": "speaker", "volume": "-1"}}, nil
	default:
		// Check config commands map for custom overrides
		if raw, ok := a.Cfg.Commands[req.Name]; ok {
			// Expect format "service/method"
			parts := strings.SplitN(raw, "/", 2)
			if len(parts) == 2 {
				return parts[0], parts[1], nil, nil
			}
		}
		return "", "", nil, fmt.Errorf("unknown command %q — use service/method format or add to commands in config", req.Name)
	}
}

// ── Sony JSON-RPC transport ───────────────────────────────────────────────────

// call sends a JSON-RPC request to the given Sony service and returns the result array.
func (a *SonyBraviaAdapter) call(ctx context.Context, service, method string, params []any) ([]any, error) {
	if params == nil {
		params = []any{}
	}

	reqBody := sonyRequest{
		Method:  method,
		Params:  params,
		ID:      1,
		Version: "1.0",
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := fmt.Sprintf("%s/sony/%s", a.baseURL, service)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Auth-PSK", a.psk)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http post %s/%s: %w", service, method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed — check PSK (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s/%s", resp.StatusCode, service, method)
	}

	body, _ := io.ReadAll(resp.Body)
	var sonyResp sonyResponse
	if err := json.Unmarshal(body, &sonyResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(sonyResp.Error) > 0 {
		apiErr := &SonyAPIError{}
		if code, ok := sonyResp.Error[0].(float64); ok {
			apiErr.Code = int(code)
		}
		if len(sonyResp.Error) > 1 {
			if msg, ok := sonyResp.Error[1].(string); ok {
				apiErr.Message = msg
			}
		}
		return nil, apiErr
	}

	return sonyResp.Result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// setTags writes a batch of tag values into the adapter's device config,
// ignoring empty values so we don't overwrite something useful with "".
func (a *SonyBraviaAdapter) setTags(kv map[string]string) {
	if a.Cfg.Tags == nil {
		a.Cfg.Tags = map[string]string{}
	}
	for k, v := range kv {
		if v == "" {
			continue
		}
		a.Cfg.Tags[k] = v
	}
}

// hostFromBaseURL strips the scheme and any :port from "http(s)://host[:port]"
// so we can use the host as a unicast WoL target. Returns empty string on
// anything that doesn't parse cleanly.
func hostFromBaseURL(base string) string {
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// sendWakeOnLAN sends a Wake-on-LAN magic packet to wake a Sony BRAVIA in
// cold standby. We send to multiple destinations to maximise the chance of
// delivery on different network topologies:
//   - 255.255.255.255:9   (limited broadcast — works on flat LANs)
//   - 255.255.255.255:7   (legacy "echo" port some firmwares listen on)
//   - <display_ip>:9      (unicast — works on managed networks that drop broadcast)
//
// Best-effort: a partial failure (one destination unreachable) still returns
// success if at least one packet went out.
func sendWakeOnLAN(mac, targetIP string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("parse mac %q: %w", mac, err)
	}
	if len(hw) != 6 {
		return fmt.Errorf("mac %q is not 6 bytes (got %d)", mac, len(hw))
	}

	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, hw...)
	}

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	defer conn.Close()

	targets := []*net.UDPAddr{
		{IP: net.IPv4bcast, Port: 9},
		{IP: net.IPv4bcast, Port: 7},
	}
	if targetIP != "" {
		if ip := net.ParseIP(targetIP); ip != nil {
			targets = append(targets,
				&net.UDPAddr{IP: ip, Port: 9},
				&net.UDPAddr{IP: ip, Port: 7},
			)
		}
	}

	var lastErr error
	sent := 0
	for _, dst := range targets {
		if _, werr := conn.WriteTo(packet, dst); werr != nil {
			lastErr = werr
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("no magic packets sent (last error: %w)", lastErr)
	}
	return nil
}

// altPowerParams flips the status param between the documented string form
// ("active"/"standby") and the boolean form (true/false) that some firmwares
// require. Returns nil if the input doesn't match either expected shape.
func altPowerParams(params []any) []any {
	if len(params) != 1 {
		return nil
	}
	m, ok := params[0].(map[string]any)
	if !ok {
		return nil
	}
	switch s := m["status"].(type) {
	case string:
		switch s {
		case "active":
			return []any{map[string]any{"status": true}}
		case "standby":
			return []any{map[string]any{"status": false}}
		}
	case bool:
		if s {
			return []any{map[string]any{"status": "active"}}
		}
		return []any{map[string]any{"status": "standby"}}
	}
	return nil
}

// intVal extracts a numeric field from a JSON map. JSON numbers decode as
// float64 in Go, so we coerce. Returns 0 if missing or wrong type.
func intVal(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func stringVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
