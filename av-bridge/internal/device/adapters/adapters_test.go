package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// ---- REST Adapter Tests ----

func newRESTConfig(addr string) config.DeviceConfig {
	return config.DeviceConfig{
		ID:       "test-display",
		Name:     "Test Display",
		Type:     "display",
		Protocol: "rest",
		Address:  addr,
		PollRate: 60 * time.Second,
		Commands: map[string]string{
			"poll":     "/status",
			"power_on": "/power/on",
		},
	}
}

func TestRESTAdapter_Connect_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	cfg := newRESTConfig(srv.URL)
	a := NewRESTAdapter(cfg)

	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if a.Status() != device.StatusOnline {
		t.Errorf("expected online, got %v", a.Status())
	}
}

func TestRESTAdapter_Connect_Failure(t *testing.T) {
	cfg := newRESTConfig("http://127.0.0.1:1") // nothing listening
	a := NewRESTAdapter(cfg)

	err := a.Connect(context.Background())
	if err == nil {
		t.Fatal("expected connection error")
	}
	if a.Status() != device.StatusOffline {
		t.Errorf("expected offline after failed connect, got %v", a.Status())
	}
}

func TestRESTAdapter_Poll_ReturnsMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"power": "on",
			"input": "HDMI1",
		})
	}))
	defer srv.Close()

	cfg := newRESTConfig(srv.URL)
	a := NewRESTAdapter(cfg)
	_ = a.Connect(context.Background())

	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if tel == nil {
		t.Fatal("expected telemetry, got nil")
	}
	if tel.DeviceID != "test-display" {
		t.Errorf("unexpected device ID: %q", tel.DeviceID)
	}
	if tel.Metrics["power"] != "on" {
		t.Errorf("expected power=on in metrics, got %v", tel.Metrics["power"])
	}
}

func TestRESTAdapter_SendCommand(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
	}))
	defer srv.Close()

	cfg := newRESTConfig(srv.URL)
	a := NewRESTAdapter(cfg)
	_ = a.Connect(context.Background())

	resp, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "power_on"})
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if receivedPath != "/power/on" {
		t.Errorf("expected path /power/on, got %q", receivedPath)
	}
}

func TestRESTAdapter_Poll_DegradedOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := newRESTConfig(srv.URL)
	a := NewRESTAdapter(cfg)
	_ = a.Connect(context.Background())

	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusDegraded {
		t.Errorf("expected degraded on 503, got %v", tel.Status)
	}
}

func TestRESTAdapter_Auth_BearerToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := newRESTConfig(srv.URL)
	cfg.Tags = map[string]string{"auth_token": "secret-token"}
	a := NewRESTAdapter(cfg)
	_ = a.Connect(context.Background())

	if !strings.Contains(receivedAuth, "Bearer secret-token") {
		t.Errorf("expected Bearer token in auth header, got %q", receivedAuth)
	}
}

// ---- Factory Tests ----

func TestFactory_CreatesCorrectAdapters(t *testing.T) {
	cases := []struct {
		protocol string
		wantErr  bool
	}{
		{"rest", false},
		{"websocket", false},
		{"telnet", false},
		{"serial", false},
		{"mqtt", true},
		{"", true},
	}

	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			cfg := config.DeviceConfig{
				ID:       "test",
				Name:     "Test",
				Type:     "display",
				Protocol: tc.protocol,
				Address:  "1.2.3.4:80",
			}
			_, err := New(cfg, Deps{})
			if tc.wantErr && err == nil {
				t.Errorf("expected error for protocol %q", tc.protocol)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for protocol %q: %v", tc.protocol, err)
			}
		})
	}
}

// ---- Vendor adapter factory tests ----

func TestFactory_VendorAdapters(t *testing.T) {
	cases := []struct {
		protocol string
		wantErr  bool
	}{
		{"tesira", false},
		{"sony_bravia", false},
		{"poly_videoos", false},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			cfg := config.DeviceConfig{
				ID:       "vendor-test",
				Name:     "Vendor Test",
				Type:     "display",
				Protocol: tc.protocol,
				Address:  "192.168.1.100",
				Tags:     map[string]string{"psk": "test", "tls_skip_verify": "true"},
			}
			dev, err := New(cfg, Deps{})
			if tc.wantErr && err == nil {
				t.Errorf("expected error for protocol %q", tc.protocol)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for protocol %q: %v", tc.protocol, err)
			}
			if dev != nil && dev.ID() != "vendor-test" {
				t.Errorf("unexpected device ID: %q", dev.ID())
			}
		})
	}
}

// ---- Sony Bravia adapter unit tests ----

func TestSonyBraviaAdapter_MissingPSK(t *testing.T) {
	cfg := config.DeviceConfig{
		ID:       "sony-no-psk",
		Name:     "Sony No PSK",
		Type:     "display",
		Protocol: "sony_bravia",
		Address:  "192.168.1.100",
		Tags:     map[string]string{}, // deliberately no PSK
	}
	a := NewSonyBraviaAdapter(cfg)
	err := a.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when PSK is missing")
	}
	if !strings.Contains(err.Error(), "psk") {
		t.Errorf("expected PSK-related error, got: %v", err)
	}
}

func TestSonyBraviaAdapter_CommandResolution(t *testing.T) {
	cfg := config.DeviceConfig{
		ID:       "sony-cmd-test",
		Name:     "Sony Cmd Test",
		Type:     "display",
		Protocol: "sony_bravia",
		Address:  "192.168.1.100",
		Tags:     map[string]string{"psk": "test"},
	}
	a := NewSonyBraviaAdapter(cfg)

	cases := []struct {
		cmd         string
		wantService string
		wantMethod  string
		wantErr     bool
	}{
		{"power_on", "system", "setPowerStatus", false},
		{"power_off", "system", "setPowerStatus", false},
		{"input_hdmi1", "avContent", "setPlayContent", false},
		{"input_hdmi2", "avContent", "setPlayContent", false},
		{"mute", "audio", "setAudioMute", false},
		{"unmute", "audio", "setAudioMute", false},
		{"system/getPowerStatus", "system", "getPowerStatus", false},
		{"avContent/getPlayingContentInfo", "avContent", "getPlayingContentInfo", false},
		{"unknown_command", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			req := device.CommandRequest{Name: tc.cmd}
			svc, method, _, err := a.ResolveCommand(req)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for command %q", tc.cmd)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for command %q: %v", tc.cmd, err)
			}
			if svc != tc.wantService {
				t.Errorf("service: got %q, want %q", svc, tc.wantService)
			}
			if method != tc.wantMethod {
				t.Errorf("method: got %q, want %q", method, tc.wantMethod)
			}
		})
	}
}

func TestSonyBraviaAdapter_BaseURLNormalisation(t *testing.T) {
	cases := []struct {
		address string
		wantURL string
	}{
		{"192.168.1.100", "http://192.168.1.100"},
		{"http://192.168.1.100", "http://192.168.1.100"},
		{"https://192.168.1.100", "https://192.168.1.100"},
		{"192.168.1.100/", "http://192.168.1.100"},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			cfg := config.DeviceConfig{
				ID: "test", Address: tc.address,
				Tags: map[string]string{"psk": "x"},
			}
			a := NewSonyBraviaAdapter(cfg)
			if a.baseURL != tc.wantURL {
				t.Errorf("baseURL: got %q, want %q", a.baseURL, tc.wantURL)
			}
		})
	}
}

// ---- Poly G7500 adapter unit tests ----

func TestPolyVideoOSAdapter_BaseURLNormalisation(t *testing.T) {
	cases := []struct {
		address string
		wantURL string
	}{
		{"192.168.1.100", "https://192.168.1.100"},
		{"https://192.168.1.100", "https://192.168.1.100"},
		{"192.168.1.100/", "https://192.168.1.100"},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			cfg := config.DeviceConfig{
				ID: "test", Address: tc.address,
				Tags: map[string]string{},
			}
			a := NewPolyVideoOSAdapter(cfg, nil)
			if a.baseURL != tc.wantURL {
				t.Errorf("baseURL: got %q, want %q", a.baseURL, tc.wantURL)
			}
		})
	}
}

func TestPolyVideoOSAdapter_UnknownCommand(t *testing.T) {
	cfg := config.DeviceConfig{
		ID: "poly-test", Address: "192.168.1.100",
		Username: "admin", Password: "pass",
		Tags: map[string]string{"tls_skip_verify": "true"},
	}
	a := NewPolyVideoOSAdapter(cfg, nil)
	_, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "invalid_command"})
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("expected 'unknown command' error, got: %v", err)
	}
}

func TestPolyVideoOSAdapter_DialRequiresAddress(t *testing.T) {
	cfg := config.DeviceConfig{
		ID: "poly-test", Address: "192.168.1.100",
		Username: "admin", Password: "pass",
		Tags: map[string]string{"tls_skip_verify": "true"},
	}
	a := NewPolyVideoOSAdapter(cfg, nil)
	_, err := a.SendCommand(context.Background(), device.CommandRequest{
		Name: "dial",
		Args: map[string]any{}, // missing address
	})
	if err == nil {
		t.Fatal("expected error when dial address is missing")
	}
}

// ---- Tesira adapter unit tests ----

func TestTesiraAdapter_SubscriptionNotificationParsing(t *testing.T) {
	cfg := config.DeviceConfig{
		ID: "tesira-test", Address: "192.168.1.100:23",
		Tags: map[string]string{
			"mute_instance_tag": "MicMuteLevel1",
			"gain_instance_tag": "ProgramLevel1",
		},
	}
	a := NewTesiraAdapter(cfg)

	cases := []struct {
		line       string
		wantLabel  string
		wantValue  string
	}{
		{"! mute_state true", "mute_state", "true"},
		{"! gain_level -10.0", "gain_level", "-10.0"},
		{"! call_state \"Init\"", "call_state", "Init"},
	}

	for _, tc := range cases {
		t.Run(tc.wantLabel, func(t *testing.T) {
			a.HandleSubscriptionNotification(tc.line)
			// Verify the notification was emitted as an event
			select {
			case evt := <-a.Events():
				if evt.EventType != "tesira_subscription:"+tc.wantLabel {
					t.Errorf("event type: got %q, want %q", evt.EventType, "tesira_subscription:"+tc.wantLabel)
				}
				if v, ok := evt.Payload["value"]; !ok || v != tc.wantValue {
					t.Errorf("event value: got %q, want %q", v, tc.wantValue)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("no event emitted after subscription notification")
			}
		})
	}
}
