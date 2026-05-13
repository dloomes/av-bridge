package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud/lens"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// PolyVideoOSAdapter communicates with Poly G7500 video codecs and Studio X
// series devices using the Poly VideoOS REST API (HTTPS + JSON).
//
// Endpoints used:
//   - POST   /rest/session            — login (cookie + X-XSRF-Token)
//   - GET    /rest/system                  — softwareVersion / serialNumber
//   - GET    /rest/system/status            — subsystem health (network, sip, mics, ...)
//   - GET    /rest/system/mode/device       — Device Mode (Teams/Zoom appliance) flag
//   - GET    /rest/conferences        — active conferences (calls)
//   - GET    /rest/audio/muted        — microphone mute state (bare boolean body)
//   - GET    /rest/audio/volume       — system volume (bare integer body)
//   - POST   /rest/audio/muted        — set mute (body is bare boolean)
//   - POST   /rest/audio/volume       — set volume (body is bare integer)
//   - POST   /rest/conferences        — dial
//   - DELETE /rest/conferences/{id}   — hangup
//   - POST   /rest/system/reboot      — reboot
//
// Authentication uses a session-based flow:
//  1. POST /rest/session with admin credentials → receives a session cookie
//     and an X-XSRF-Token header value (required from PolyOS 4.6.2+)
//  2. All subsequent requests carry both the cookie jar and the XSRF token
//  3. On 401 response, the adapter transparently re-authenticates and retries
//
// The G7500 presents a self-signed TLS certificate by default. Set
// tags.tls_skip_verify: "true" in config to accept it.
type PolyVideoOSAdapter struct {
	device.Base

	client    *http.Client
	baseURL   string
	xsrfToken string
	sessionMu sync.Mutex
	jar       http.CookieJar

	lens *lens.Client // optional; nil when Lens is disabled

	infoMu          sync.RWMutex
	softwareVersion string
	serialNumber    string
	ipAddress       string

	// Lens-sourced fields, kept separate so we can publish them under their
	// own bucket on telemetry. Zero values when Lens is disabled or the
	// device isn't in the Lens inventory.
	lensMu              sync.RWMutex
	lensMacAddress      string
	lensInternalIP      string
	lensExternalIP      string
	lensHardwareModel   string
	lensHardwareProduct string
	lensHardwareFamily  string
	lensManufacturer    string
	lensSoftware        string
	lensSoftwareBuild   string
	lensRoom            string
	lensSite            string
	lensConnected       bool
	lensConnectedSet    bool
	lensLastDetected    string
}

// errSessionExpired indicates the device returned 401 Unauthorized.
// The adapter's doWithReauth helper handles this transparently.
var errSessionExpired = errors.New("session expired")

func NewPolyVideoOSAdapter(cfg config.DeviceConfig, lensClient *lens.Client) *PolyVideoOSAdapter {
	base := cfg.Address
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")

	skipVerify := cfg.Tags["tls_skip_verify"] == "true"

	jar, _ := cookiejar.New(nil)

	return &PolyVideoOSAdapter{
		Base:    device.NewBase(cfg),
		baseURL: base,
		jar:     jar,
		lens:    lensClient,
		client: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: skipVerify, //nolint:gosec // intentional for self-signed certs
				},
			},
		},
	}
}

// ── Connection / session management ──────────────────────────────────────────

func (a *PolyVideoOSAdapter) Connect(ctx context.Context) error {
	if err := a.login(ctx); err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("poly g7500 connect %s: %w", a.Cfg.ID, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("poly g7500 connected", "device", a.Cfg.ID, "address", a.Cfg.Address)
	return nil
}

func (a *PolyVideoOSAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = a.do(ctx, http.MethodDelete, "/rest/session", nil)
	return nil
}

func (a *PolyVideoOSAdapter) login(ctx context.Context) error {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	username := a.Cfg.Username
	if username == "" {
		username = "admin"
	}

	creds := map[string]string{"user": username, "password": a.Cfg.Password}
	b, _ := json.Marshal(creds)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/rest/session", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d) — check username and password", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("login returned HTTP %d", resp.StatusCode)
	}

	// Older PolyOS firmware returns the XSRF token via the X-XSRF-Token response
	// header. Newer firmware (Studio X with PolyOS 4.x+) sets it as an XSRF-TOKEN
	// cookie instead, which we must echo back in the X-XSRF-Token request header
	// on every subsequent call — otherwise the device responds with 403 to all
	// /rest/* endpoints except /rest/session itself.
	token := resp.Header.Get("X-XSRF-Token")
	if token == "" {
		for _, c := range resp.Cookies() {
			if strings.EqualFold(c.Name, "XSRF-TOKEN") || strings.EqualFold(c.Name, "XSRF_TOKEN") {
				token = c.Value
				break
			}
		}
	}
	if token != "" {
		a.xsrfToken = token
		slog.Debug("poly xsrf token acquired", "device", a.Cfg.ID)
	} else {
		slog.Warn("poly login succeeded but no XSRF token found in response", "device", a.Cfg.ID)
	}

	slog.Debug("poly session established", "device", a.Cfg.ID)
	return nil
}

// ── Device info (static identity) ────────────────────────────────────────────

// fetchDeviceInfo refreshes cached software version, serial number, IP, and
// MAC address. The Studio X / G7500 PolyOS exposes most of these on /rest/system
// itself (softwareVersion, serialNumber, ipv4Address). MAC address fields vary
// across firmware — we probe a few known endpoints and a flexible parser picks
// up whichever returns 200.
//
// Best-effort: any individual endpoint failure is logged at debug level and
// leaves prior cached values untouched.
func (a *PolyVideoOSAdapter) fetchDeviceInfo(ctx context.Context) {
	if body, err := a.getWithReauth(ctx, "/rest/system"); err == nil {
		// Log raw body once (when nothing's cached yet) so we can see all
		// fields exposed on this firmware and extend the parser if needed.
		a.infoMu.RLock()
		empty := a.softwareVersion == "" && a.serialNumber == "" && a.ipAddress == ""
		a.infoMu.RUnlock()
		if empty {
			slog.Info("poly /rest/system raw body", "device", a.Cfg.ID, "body", string(body))
		}

		var sys struct {
			SoftwareVersion string `json:"softwareVersion"`
			Build           string `json:"build"`
			SerialNumber    string `json:"serialNumber"`
		}
		if jErr := json.Unmarshal(body, &sys); jErr == nil {
			a.infoMu.Lock()
			if sys.SoftwareVersion != "" {
				a.softwareVersion = sys.SoftwareVersion
			} else if sys.Build != "" {
				a.softwareVersion = sys.Build
			}
			if sys.SerialNumber != "" {
				a.serialNumber = sys.SerialNumber
			}
			a.infoMu.Unlock()
		}
	} else {
		slog.Debug("poly device info fetch failed", "device", a.Cfg.ID, "endpoint", "/rest/system", "error", err)
	}

	// IP address — derived from the configured device address. Studio X70 /
	// VideoOS 4.5 does not expose IP via the public REST API, but we already
	// know the address since we have to connect to it. Hostnames pass through
	// as-is.
	a.infoMu.RLock()
	needIP := a.ipAddress == ""
	a.infoMu.RUnlock()
	if needIP {
		if host := hostFromAddress(a.Cfg.Address); host != "" {
			a.infoMu.Lock()
			a.ipAddress = host
			a.infoMu.Unlock()
		}
	}

	// Lens enrichment — opt-in per device via tags.lens_managed: "true".
	// Requires the serial we just pulled from /rest/system. Best-effort: any
	// failure leaves cached values untouched and is logged at info level so
	// misconfiguration surfaces.
	if a.lens != nil && a.Cfg.Tags["lens_managed"] == "true" {
		a.infoMu.RLock()
		serial := a.serialNumber
		a.infoMu.RUnlock()
		if serial != "" {
			ldev, err := a.lens.LookupBySerial(ctx, serial)
			if err != nil {
				slog.Info("poly lens lookup failed",
					"device", a.Cfg.ID, "serial", serial, "error", err)
			} else if ldev == nil {
				slog.Info("poly lens lookup returned no match",
					"device", a.Cfg.ID, "serial", serial)
			} else {
				a.lensMu.Lock()
				a.lensMacAddress = ldev.MacAddress
				a.lensInternalIP = ldev.InternalIP
				a.lensExternalIP = ldev.ExternalIP
				a.lensHardwareModel = ldev.HardwareModel
				a.lensHardwareProduct = ldev.HardwareProduct
				a.lensHardwareFamily = ldev.HardwareFamily
				a.lensManufacturer = ldev.Manufacturer
				a.lensSoftware = ldev.SoftwareVersion
				a.lensSoftwareBuild = ldev.SoftwareBuild
				if ldev.Room != nil {
					a.lensRoom = ldev.Room.Name
				}
				if ldev.Site != nil {
					a.lensSite = ldev.Site.Name
				}
				a.lensConnected = ldev.Connected
				a.lensConnectedSet = true
				if !ldev.LastDetected.IsZero() {
					a.lensLastDetected = ldev.LastDetected.UTC().Format(time.RFC3339)
				}
				a.lensMu.Unlock()
			}
		}
	}
}

// hostFromAddress extracts the host portion (no port, no scheme) from a Poly
// device's configured address. Accepts "10.0.0.1", "10.0.0.1:443",
// "https://10.0.0.1", "https://host:443", etc.
func hostFromAddress(addr string) string {
	s := strings.TrimSpace(addr)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "http") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}


// ── Poll ─────────────────────────────────────────────────────────────────────

// Poll queries multiple subsystems in parallel-tolerant style: any single
// endpoint failure is logged and the remaining metrics are still collected.
// The device is considered Online if at least one endpoint succeeds.
func (a *PolyVideoOSAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	metrics := map[string]any{}
	start := time.Now()

	a.infoMu.RLock()
	needsInfo := a.softwareVersion == "" || a.serialNumber == "" || a.ipAddress == ""
	a.infoMu.RUnlock()
	if !needsInfo && a.lens != nil && a.Cfg.Tags["lens_managed"] == "true" {
		a.lensMu.RLock()
		if a.lensLastDetected == "" {
			needsInfo = true // Lens enrichment hasn't run yet
		}
		a.lensMu.RUnlock()
	}
	if needsInfo {
		a.fetchDeviceInfo(ctx)
	}

	successes := 0
	failures := []string{}

	// System status — array of {langtag, name, stateList}
	if body, err := a.getWithReauth(ctx, "/rest/system/status"); err == nil {
		successes++
		var statuses []map[string]any
		if jErr := json.Unmarshal(body, &statuses); jErr == nil {
			for _, s := range statuses {
				name, _ := s["name"].(string)
				states, _ := s["stateList"].([]any)
				if name == "" || len(states) == 0 {
					continue
				}
				short := strings.TrimPrefix(name, "system.status.")
				short = strings.ReplaceAll(short, ".", "_")
				if first, ok := states[0].(string); ok {
					metrics["status_"+short] = first
				}
			}
		}
	} else {
		failures = append(failures, "system/status")
		slog.Debug("poly poll endpoint failed", "device", a.Cfg.ID, "endpoint", "/rest/system/status", "error", err)
	}

	// Device Mode — boolean: true when running as a Teams/Zoom partner-app
	// appliance, false when running native PolyOS. Response shape on Studio X
	// / G7500 is {"result": true|false}.
	if body, err := a.getWithReauth(ctx, "/rest/system/mode/device"); err == nil {
		successes++
		var mode struct {
			Result bool `json:"result"`
		}
		if jErr := json.Unmarshal(body, &mode); jErr == nil {
			metrics["device_mode_active"] = mode.Result
		}
	}

	// Active conferences (calls)
	if body, err := a.getWithReauth(ctx, "/rest/conferences"); err == nil {
		successes++
		var confs []map[string]any
		if jErr := json.Unmarshal(body, &confs); jErr == nil {
			metrics["active_calls"] = len(confs)
			if len(confs) > 0 {
				metrics["call_state"] = "active"
				a.Emit(&device.Event{
					DeviceID:   a.Cfg.ID,
					DeviceName: a.Cfg.Name,
					DeviceType: a.Cfg.Type,
					EventType:  "poly_call_active",
					Payload:    map[string]any{"active_calls": len(confs)},
					Timestamp:  time.Now().UTC(),
				})
			} else {
				metrics["call_state"] = "idle"
			}
		}
	} else {
		failures = append(failures, "conferences")
		slog.Debug("poly poll endpoint failed", "device", a.Cfg.ID, "endpoint", "/rest/conferences", "error", err)
	}

	// Microphone mute (bare boolean body)
	if body, err := a.getWithReauth(ctx, "/rest/audio/muted"); err == nil {
		successes++
		var muted bool
		if jErr := json.Unmarshal(body, &muted); jErr == nil {
			metrics["mic_mute"] = muted
		}
	} else {
		failures = append(failures, "audio/muted")
	}

	// Volume (bare integer body)
	if body, err := a.getWithReauth(ctx, "/rest/audio/volume"); err == nil {
		successes++
		var vol int
		if jErr := json.Unmarshal(body, &vol); jErr == nil {
			metrics["volume"] = vol
		}
	} else {
		failures = append(failures, "audio/volume")
	}

	metrics["response_ms"] = time.Since(start).Milliseconds()

	a.infoMu.RLock()
	if a.softwareVersion != "" {
		metrics["software_version"] = a.softwareVersion
	}
	if a.serialNumber != "" {
		metrics["serial_number"] = a.serialNumber
	}
	if a.ipAddress != "" {
		metrics["ip_address"] = a.ipAddress
	}
	a.infoMu.RUnlock()

	// Lens-sourced metrics — kept in their own map so the portal can render
	// them under a separate heading.
	lensMetrics := map[string]any{}
	a.lensMu.RLock()
	if a.lensMacAddress != "" {
		lensMetrics["mac_address"] = a.lensMacAddress
	}
	if a.lensInternalIP != "" {
		lensMetrics["internal_ip"] = a.lensInternalIP
	}
	if a.lensExternalIP != "" {
		lensMetrics["external_ip"] = a.lensExternalIP
	}
	if a.lensHardwareModel != "" {
		lensMetrics["hardware_model"] = a.lensHardwareModel
	}
	if a.lensHardwareProduct != "" {
		lensMetrics["hardware_product"] = a.lensHardwareProduct
	}
	if a.lensHardwareFamily != "" {
		lensMetrics["hardware_family"] = a.lensHardwareFamily
	}
	if a.lensManufacturer != "" {
		lensMetrics["manufacturer"] = a.lensManufacturer
	}
	if a.lensSoftware != "" {
		lensMetrics["software_version"] = a.lensSoftware
	}
	if a.lensSoftwareBuild != "" {
		lensMetrics["software_build"] = a.lensSoftwareBuild
	}
	if a.lensRoom != "" {
		lensMetrics["room"] = a.lensRoom
	}
	if a.lensSite != "" {
		lensMetrics["site"] = a.lensSite
	}
	if a.lensConnectedSet {
		lensMetrics["connected"] = a.lensConnected
	}
	if a.lensLastDetected != "" {
		lensMetrics["last_detected"] = a.lensLastDetected
	}
	a.lensMu.RUnlock()
	if len(lensMetrics) > 0 {
		t.LensMetrics = lensMetrics
	}

	if successes == 0 {
		a.SetStatus(device.StatusDegraded)
		t.Status = device.StatusDegraded
		t.Error = fmt.Sprintf("all endpoints failed: %s", strings.Join(failures, ", "))
		t.Metrics = metrics
		return t, nil
	}

	if len(failures) > 0 {
		metrics["unavailable_endpoints"] = strings.Join(failures, ",")
	}

	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// ── SendCommand ──────────────────────────────────────────────────────────────

// SendCommand supports the following named commands:
//
//	mute        — POST /rest/audio/muted body: true
//	unmute      — POST /rest/audio/muted body: false
//	vol_up      — read /rest/audio/volume, post +1
//	vol_dn      — read /rest/audio/volume, post -1
//	dial        — POST /rest/conferences body: {address, rate, dialType}
//	hangup      — DELETE /rest/conferences/{id}; without conf_id, hangs up all
//	reboot      — POST /rest/system/reboot body: {"action":"reboot"}
//
// Args:
//   - address:  SIP/H.323 URI for dial command (required)
//   - rate:     bitrate for dial (optional, default 1024)
//   - dialType: "AUTO", "SIP", "H323", etc. (optional, default "AUTO")
//   - conf_id:  specific conference ID for hangup (optional, default: hang up all)
func (a *PolyVideoOSAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	start := time.Now()

	var (
		respBody []byte
		err      error
	)

	switch req.Name {
	case "mute":
		respBody, err = a.postWithReauth(ctx, "/rest/audio/muted", true)

	case "unmute":
		respBody, err = a.postWithReauth(ctx, "/rest/audio/muted", false)

	case "vol_up", "vol_dn":
		var cur []byte
		cur, err = a.getWithReauth(ctx, "/rest/audio/volume")
		if err == nil {
			var vol int
			if jErr := json.Unmarshal(cur, &vol); jErr != nil {
				err = fmt.Errorf("parse volume: %w", jErr)
				break
			}
			if req.Name == "vol_up" {
				vol++
			} else {
				vol--
			}
			if vol < 0 {
				vol = 0
			}
			if vol > 50 {
				vol = 50
			}
			respBody, err = a.postWithReauth(ctx, "/rest/audio/volume", vol)
		}

	case "dial":
		addr, _ := req.Args["address"].(string)
		if addr == "" {
			return nil, fmt.Errorf("dial command requires 'address' arg (e.g. sip:room@domain.com)")
		}
		rate := 1024
		if r, ok := req.Args["rate"].(float64); ok {
			rate = int(r)
		}
		dialType, _ := req.Args["dialType"].(string)
		if dialType == "" {
			dialType = "AUTO"
		}
		payload := map[string]any{"address": addr, "rate": rate, "dialType": dialType}
		respBody, err = a.postWithReauth(ctx, "/rest/conferences", payload)

	case "hangup":
		confID, _ := req.Args["conf_id"].(string)
		if confID != "" {
			_, err = a.doWithReauth(ctx, http.MethodDelete, "/rest/conferences/"+confID, nil)
			break
		}
		// No conf_id — hang up every active conference
		listBody, getErr := a.getWithReauth(ctx, "/rest/conferences")
		if getErr != nil {
			err = getErr
			break
		}
		var confs []map[string]any
		if jErr := json.Unmarshal(listBody, &confs); jErr != nil {
			err = fmt.Errorf("parse conferences: %w", jErr)
			break
		}
		for _, c := range confs {
			id, _ := c["id"].(string)
			if id != "" {
				_, _ = a.doWithReauth(ctx, http.MethodDelete, "/rest/conferences/"+id, nil)
			}
		}

	case "reboot":
		respBody, err = a.postWithReauth(ctx, "/rest/system/reboot",
			map[string]string{"action": "reboot"})

	default:
		return nil, fmt.Errorf("unknown command %q", req.Name)
	}

	if err != nil {
		return nil, fmt.Errorf("poly command %q: %w", req.Name, err)
	}

	parsed := map[string]any{}
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &parsed)
	}

	return &device.CommandResponse{
		Raw:     string(respBody),
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

// ── HTTP helpers ─────────────────────────────────────────────────────────────

func (a *PolyVideoOSAdapter) get(ctx context.Context, path string) ([]byte, error) {
	return a.do(ctx, http.MethodGet, path, nil)
}

func (a *PolyVideoOSAdapter) post(ctx context.Context, path string, body any) ([]byte, error) {
	return a.do(ctx, http.MethodPost, path, body)
}

// getWithReauth / postWithReauth / doWithReauth retry once after re-authenticating
// when the device returns 401. All Poll/SendCommand call sites should use these
// rather than the bare do/get/post helpers.
func (a *PolyVideoOSAdapter) getWithReauth(ctx context.Context, path string) ([]byte, error) {
	return a.doWithReauth(ctx, http.MethodGet, path, nil)
}

func (a *PolyVideoOSAdapter) postWithReauth(ctx context.Context, path string, body any) ([]byte, error) {
	return a.doWithReauth(ctx, http.MethodPost, path, body)
}

func (a *PolyVideoOSAdapter) doWithReauth(ctx context.Context, method, path string, body any) ([]byte, error) {
	resp, err := a.do(ctx, method, path, body)
	if err == nil || !errors.Is(err, errSessionExpired) {
		return resp, err
	}
	if loginErr := a.login(ctx); loginErr != nil {
		return nil, fmt.Errorf("re-auth failed: %w", loginErr)
	}
	return a.do(ctx, method, path, body)
}

func (a *PolyVideoOSAdapter) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	a.sessionMu.Lock()
	if a.xsrfToken != "" {
		req.Header.Set("X-XSRF-Token", a.xsrfToken)
	}
	a.sessionMu.Unlock()

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errSessionExpired
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s %s: %s", resp.StatusCode, method, path, string(respBody))
	}

	if token := resp.Header.Get("X-XSRF-Token"); token != "" {
		a.sessionMu.Lock()
		a.xsrfToken = token
		a.sessionMu.Unlock()
	}

	return respBody, nil
}
