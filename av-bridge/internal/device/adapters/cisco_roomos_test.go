package adapters

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Shapes follow the RoomOS 11 API Reference Guide (D15502): list items carry
// item="n", command replies are <Command><XxxResult status="…"/></Command>.
const ciscoStatusInCall = `<?xml version="1.0"?>
<Status product="Cisco Codec" version="ce11.24.1.5" apiVersion="4">
  <Audio>
    <Microphones><Mute>On</Mute></Microphones>
    <Volume>70</Volume>
  </Audio>
  <Call item="27" maxOccurrence="n">
    <CallType>Video</CallType>
    <Direction>Outgoing</Direction>
    <RemoteNumber>room@example.com</RemoteNumber>
    <Status>Connected</Status>
  </Call>
  <Diagnostics>
    <Message item="1" maxOccurrence="n">
      <Description>Camera not detected</Description>
      <Level>Error</Level>
      <Type>CameraDetected</Type>
    </Message>
    <Message item="2" maxOccurrence="n">
      <Description>Informational only</Description>
      <Level>Info</Level>
      <Type>Something</Type>
    </Message>
  </Diagnostics>
  <Network item="1" maxOccurrence="1">
    <Ethernet><MacAddress>00:50:60:02:FD:C7</MacAddress></Ethernet>
    <IPv4><Address>192.0.2.149</Address></IPv4>
  </Network>
  <RoomAnalytics><PeopleCount><Current>2</Current></PeopleCount></RoomAnalytics>
  <Standby><State>Off</State></Standby>
  <SystemUnit>
    <Hardware><Module><SerialNumber>FOC99999999</SerialNumber></Module></Hardware>
    <ProductId>Cisco Room Kit EQ</ProductId>
    <Software><Version>ce11.24.1.5</Version></Software>
    <Uptime>597095</Uptime>
  </SystemUnit>
</Status>`

const ciscoStatusIdle = `<?xml version="1.0"?>
<Status product="Cisco Codec" version="ce11.24.1.5" apiVersion="4">
  <Audio><Microphones><Mute>Off</Mute></Microphones><Volume>50</Volume></Audio>
  <RoomAnalytics><PeopleCount><Current>-1</Current></PeopleCount></RoomAnalytics>
  <Standby><State>Halfwake</State></Standby>
  <SystemUnit>
    <Hardware><MainBoard><SerialNumber>FOC00000001</SerialNumber></MainBoard></Hardware>
    <ProductId>Cisco Room Bar</ProductId>
  </SystemUnit>
</Status>`

// fakeCodec emulates the xAPI HTTP endpoints.
type fakeCodec struct {
	mu       sync.Mutex
	status   string
	lastPut  string
	putReply string
}

func (f *fakeCodec) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "marcus" || pass != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="xapi"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/getxml":
			switch r.URL.Query().Get("location") {
			case "/Status":
				_, _ = io.WriteString(w, f.status)
			case "/Status/SystemUnit/ProductId":
				_, _ = io.WriteString(w, `<Status><SystemUnit><ProductId>Cisco Room Kit EQ</ProductId></SystemUnit></Status>`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/putxml":
			if ct := r.Header.Get("Content-Type"); ct != "text/xml" {
				t.Errorf("putxml Content-Type = %q, want text/xml", ct)
			}
			b, _ := io.ReadAll(r.Body)
			f.lastPut = string(b)
			reply := f.putReply
			if reply == "" {
				reply = `<?xml version="1.0"?><Command><Result status="OK"/></Command>`
			}
			_, _ = io.WriteString(w, reply)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newTestCisco(t *testing.T, f *fakeCodec, user, pass string) (*CiscoRoomOSAdapter, func()) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	a := NewCiscoRoomOSAdapter(config.DeviceConfig{
		ID: "roomkit-1", Name: "Room Kit", Type: "conferencing", Protocol: "cisco_roomos",
		Address: srv.URL, Username: user, Password: pass,
	})
	return a, srv.Close
}

func TestCiscoConnect(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusInCall}
	a, done := newTestCisco(t, f, "marcus", "secret")
	defer done()
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if a.Status() != device.StatusOnline {
		t.Errorf("status = %v, want online", a.Status())
	}

	bad, done2 := newTestCisco(t, f, "marcus", "wrong")
	defer done2()
	err := bad.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Admin role") {
		t.Fatalf("bad-credential Connect error = %v, want an auth error naming the Admin role", err)
	}
}

func TestCiscoPollInCall(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusInCall}
	a, done := newTestCisco(t, f, "marcus", "secret")
	defer done()
	tel, err := a.Poll(context.Background())
	if err != nil || tel.Status != device.StatusOnline {
		t.Fatalf("Poll: err=%v status=%v error=%q", err, tel.Status, tel.Error)
	}
	m := tel.Metrics
	want := map[string]any{
		"product_id":       "Cisco Room Kit EQ",
		"software_version": "ce11.24.1.5",
		"serial_number":    "FOC99999999",
		"uptime_s":         597095,
		"ip_address":       "192.0.2.149",
		"mac_address":      "00:50:60:02:FD:C7",
		"volume":           70,
		"mic_mute":         true,
		"standby_state":    "Off",
		"power_status":     "on",
		"active_calls":     1,
		"call_state":       "connected",
		"remote_number":    "room@example.com",
		"fault_count":      1, // the Info message is not a fault
		"people_count":     2,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}
	faults, _ := m["faults"].([]any)
	if len(faults) != 1 || faults[0].(map[string]any)["type"] != "CameraDetected" {
		t.Errorf("faults = %#v, want the one Error message", m["faults"])
	}
	if _, ok := m["response_ms"]; !ok {
		t.Error("response_ms missing")
	}
}

func TestCiscoPollIdleAndFallbacks(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusIdle}
	a, done := newTestCisco(t, f, "marcus", "secret")
	defer done()
	tel, _ := a.Poll(context.Background())
	m := tel.Metrics
	if m["active_calls"] != 0 || m["call_state"] != "idle" {
		t.Errorf("calls: active=%v state=%v, want 0/idle", m["active_calls"], m["call_state"])
	}
	if m["power_status"] != "standby" || m["standby_state"] != "Halfwake" {
		t.Errorf("power: %v/%v, want standby/Halfwake", m["power_status"], m["standby_state"])
	}
	if m["serial_number"] != "FOC00000001" {
		t.Errorf("serial fallback to MainBoard = %v", m["serial_number"])
	}
	if _, ok := m["people_count"]; ok {
		t.Error("people_count -1 (unavailable) should be omitted")
	}
	if m["fault_count"] != 0 {
		t.Errorf("fault_count = %v, want 0", m["fault_count"])
	}
}

func TestCiscoPollDeviceDown(t *testing.T) {
	a := NewCiscoRoomOSAdapter(config.DeviceConfig{ID: "gone", Address: "http://127.0.0.1:1", Username: "u", Password: "p"})
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll should report offline telemetry, not error: %v", err)
	}
	if tel.Status != device.StatusOffline || tel.Error == "" {
		t.Errorf("status=%v error=%q, want offline with an error", tel.Status, tel.Error)
	}
}

func TestCiscoCommands(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusIdle}
	a, done := newTestCisco(t, f, "marcus", "secret")
	defer done()
	cases := map[string]string{
		"hangup":    `<Command><Call><Disconnect command="True"/></Call></Command>`,
		"mute":      `<Command><Audio><Microphones><Mute command="True"/></Microphones></Audio></Command>`,
		"unmute":    `<Command><Audio><Microphones><Unmute command="True"/></Microphones></Audio></Command>`,
		"vol_up":    `<Command><Audio><Volume><Increase command="True"/></Volume></Audio></Command>`,
		"vol_dn":    `<Command><Audio><Volume><Decrease command="True"/></Volume></Audio></Command>`,
		"power_on":  `<Command><Standby><Deactivate command="True"/></Standby></Command>`,
		"power_off": `<Command><Standby><Activate command="True"/></Standby></Command>`,
		"reboot":    `<Command><SystemUnit><Boot command="True"/></SystemUnit></Command>`,
	}
	for name, body := range cases {
		if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: name}); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.lastPut != body {
			t.Errorf("%s body = %s\nwant %s", name, f.lastPut, body)
		}
	}
	// Every declared command is handled.
	for _, c := range ciscoCapabilities.Commands {
		if _, ok := cases[c]; !ok && c != "dial" {
			t.Errorf("capability %q has no test case", c)
		}
	}
}

func TestCiscoDial(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusIdle,
		putReply: `<?xml version="1.0"?><Command><DialResult status="OK"><CallId>3</CallId><ConferenceId>2</ConferenceId></DialResult></Command>`}
	a, done := newTestCisco(t, f, "marcus", "secret")
	defer done()

	resp, err := a.SendCommand(context.Background(), device.CommandRequest{
		Name: "dial", Args: map[string]any{"address": "a&b<c>@example.com"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if want := `<Command><Dial command="True"><Number>a&amp;b&lt;c&gt;@example.com</Number></Dial></Command>`; f.lastPut != want {
		t.Errorf("dial body not escaped:\n got %s\nwant %s", f.lastPut, want)
	}
	if resp.Parsed["call_id"] != "3" {
		t.Errorf("call_id = %v, want 3", resp.Parsed["call_id"])
	}

	if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "dial"}); err == nil {
		t.Error("dial without an address should fail before reaching the device")
	}

	f.putReply = `<?xml version="1.0"?><Command><DialResult status="Error"><Reason>Invalid number</Reason></DialResult></Command>`
	_, err = a.SendCommand(context.Background(), device.CommandRequest{Name: "dial", Args: map[string]any{"address": "x"}})
	if err == nil || !strings.Contains(err.Error(), "Invalid number") {
		t.Errorf("error reply: err = %v, want the device's reason", err)
	}

	if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "nonsense"}); err == nil {
		t.Error("unknown command should fail")
	}
}

func TestCiscoTLSSkipVerify(t *testing.T) {
	f := &fakeCodec{status: ciscoStatusIdle}
	srv := httptest.NewTLSServer(f.handler(t)) // self-signed, like a RoomOS device
	defer srv.Close()
	cfg := config.DeviceConfig{ID: "tls", Address: srv.URL, Username: "marcus", Password: "secret"}

	if err := NewCiscoRoomOSAdapter(cfg).Connect(context.Background()); err == nil {
		t.Error("self-signed certificate should be rejected by default")
	}
	cfg.Tags = map[string]string{"tls_skip_verify": "true"}
	if err := NewCiscoRoomOSAdapter(cfg).Connect(context.Background()); err != nil {
		t.Errorf("tls_skip_verify=true should accept it: %v", err)
	}
}

func TestCiscoAddressDefaultsToHTTPS(t *testing.T) {
	a := NewCiscoRoomOSAdapter(config.DeviceConfig{Address: "192.0.2.10/"})
	if a.baseURL != "https://192.0.2.10" {
		t.Errorf("baseURL = %q, want https://192.0.2.10", a.baseURL)
	}
}
