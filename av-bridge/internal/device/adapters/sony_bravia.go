package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// SonyBraviaAdapter communicates with Sony Bravia Professional Displays
// using Sony's REST API (JSON-RPC over HTTP).
//
// Authentication uses a Pre-Shared Key (PSK) passed in the X-Auth-PSK
// header on every request — no login flow, no session management.
//
// Sony's API is structured around named services accessible at:
//   http://<ip>/sony/<service>
// Key services: system, avContent, audio, appControl, videoScreen.
//
// Config tags used:
//   - psk: Pre-Shared Key configured on the display (required)
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

// Connect verifies the display is reachable by calling getSystemInformation.
// This also captures the model name, firmware version, and MAC address for
// telemetry enrichment.
func (a *SonyBraviaAdapter) Connect(ctx context.Context) error {
	if a.psk == "" {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("sony bravia %s: psk tag is required (set tags.psk in config)", a.Cfg.ID)
	}

	result, err := a.call(ctx, "system", "getSystemInformation", nil)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("sony bravia connect %s: %w", a.Cfg.ID, err)
	}

	// Cache model info in metrics on first connect
	if info, ok := result[0].(map[string]any); ok {
		a.stateLock(func() {
			a.Cfg.Tags["model"] = stringVal(info, "model")
			a.Cfg.Tags["mac_address"] = stringVal(info, "macAddr")
			a.Cfg.Tags["firmware"] = stringVal(info, "generation")
		})
	}

	a.SetStatus(device.StatusOnline)
	return nil
}

func (a *SonyBraviaAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll queries power status and current input from the display.
func (a *SonyBraviaAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	metrics := map[string]any{}
	start := time.Now()

	// Power status — /sony/system → getPowerStatus
	powerResult, err := a.call(ctx, "system", "getPowerStatus", nil)
	if err != nil {
		a.SetStatus(device.StatusDegraded)
		t.Status = device.StatusDegraded
		t.Error = err.Error()
		return t, nil
	}
	if len(powerResult) > 0 {
		if ps, ok := powerResult[0].(map[string]any); ok {
			metrics["power_status"] = stringVal(ps, "status")
		}
	}

	// Current input — /sony/avContent → getPlayingContentInfo
	inputResult, err := a.call(ctx, "avContent", "getPlayingContentInfo", nil)
	if err == nil && len(inputResult) > 0 {
		if ci, ok := inputResult[0].(map[string]any); ok {
			metrics["current_input"] = stringVal(ci, "uri")
			metrics["input_title"] = stringVal(ci, "title")
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

	result, err := a.call(ctx, service, method, params)
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
		return "system", "setPowerStatus", []any{map[string]any{"status": "active"}}, nil
	case "power_off":
		return "system", "setPowerStatus", []any{map[string]any{"status": "standby"}}, nil
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
		return nil, fmt.Errorf("sony API error: %v", sonyResp.Error)
	}

	return sonyResp.Result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (a *SonyBraviaAdapter) stateLock(fn func()) {
	// Tags are set on initial connect — use a simple approach since they're
	// written once and read many times
	fn()
}

func stringVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
