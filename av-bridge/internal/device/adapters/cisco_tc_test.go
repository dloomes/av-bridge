package adapters

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Shapes follow the TC7 API Reference Guide: Standby is reported as
// Active On/Off, and there's no RoomAnalytics.
const ciscoTCStatus = `<?xml version="1.0"?>
<Status product="Cisco Codec" version="TC7.3.3.c84180a" apiVersion="4">
  <Audio>
    <Microphones><Mute>Off</Mute></Microphones>
    <Volume>60</Volume>
  </Audio>
  <Network item="1">
    <Ethernet><MacAddress>00:50:60:0A:0B:0C</MacAddress></Ethernet>
    <IPv4><Address>192.168.1.19</Address></IPv4>
  </Network>
  <Standby><Active>On</Active></Standby>
  <SystemUnit>
    <Hardware><Module><SerialNumber>FTT12345678</SerialNumber></Module></Hardware>
    <ProductId>Cisco TelePresence SX20</ProductId>
    <Software><Version>TC7.3.3.c84180a</Version></Software>
    <Uptime>86400</Uptime>
  </SystemUnit>
</Status>`

func newTestCiscoTC(t *testing.T, f *fakeCodec) (*CiscoRoomOSAdapter, func()) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	a := NewCiscoTCAdapter(config.DeviceConfig{
		ID: "sx20-1", Name: "SX20", Type: "conferencing", Protocol: "cisco_tc",
		Address: srv.URL, Username: "marcus", Password: "secret",
	})
	return a, srv.Close
}

func TestCiscoTCPoll(t *testing.T) {
	f := &fakeCodec{status: ciscoTCStatus}
	a, done := newTestCiscoTC(t, f)
	defer done()
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOnline {
		t.Fatalf("status = %v (%s)", tel.Status, tel.Error)
	}
	m := tel.Metrics
	want := map[string]any{
		"product_id":       "Cisco TelePresence SX20",
		"software_version": "TC7.3.3.c84180a",
		"serial_number":    "FTT12345678",
		"ip_address":       "192.168.1.19",
		"volume":           60,
		"mic_mute":         false,
		"standby_state":    "Standby",
		"power_status":     "standby",
		"active_calls":     0,
		"call_state":       "idle",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}

	// Awake.
	f.mu.Lock()
	f.status = `<Status><Standby><Active>Off</Active></Standby></Status>`
	f.mu.Unlock()
	tel, _ = a.Poll(context.Background())
	if tel.Metrics["power_status"] != "on" || tel.Metrics["standby_state"] != "Off" {
		t.Errorf("awake: power=%v standby=%v, want on/Off", tel.Metrics["power_status"], tel.Metrics["standby_state"])
	}
}

func TestCiscoTCCommands(t *testing.T) {
	f := &fakeCodec{status: ciscoTCStatus}
	a, done := newTestCiscoTC(t, f)
	defer done()
	cases := map[string]string{
		"hangup":    `<Command><Call><DisconnectAll command="True"/></Call></Command>`,
		"reboot":    `<Command><Boot command="True"><Action>Restart</Action></Boot></Command>`,
		"power_on":  `<Command><Standby><Deactivate command="True"/></Standby></Command>`,
		"power_off": `<Command><Standby><Activate command="True"/></Standby></Command>`,
		"mute":      `<Command><Audio><Microphones><Mute command="True"/></Microphones></Audio></Command>`,
		"vol_up":    `<Command><Audio><Volume><Increase command="True"/></Volume></Audio></Command>`,
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
	for _, m := range a.Capabilities().Metrics {
		if m == "people_count" {
			t.Error("TC capabilities shouldn't list people_count")
		}
	}
	if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "nonsense"}); err == nil || !strings.Contains(err.Error(), "cisco_tc") {
		t.Errorf("unknown command error = %v, want it to name cisco_tc", err)
	}
}

// A TC codec whose web server only speaks TLS 1.0 with RSA key exchange:
// the RoomOS adapter can't reach it, the TC adapter can.
func TestCiscoTCLegacyTLS(t *testing.T) {
	f := &fakeCodec{status: ciscoTCStatus}
	srv := httptest.NewUnstartedServer(f.handler(t))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS10,
		MaxVersion:   tls.VersionTLS10,
		CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA},
	}
	srv.StartTLS()
	defer srv.Close()
	cfg := config.DeviceConfig{ID: "sx20-tls", Address: srv.URL, Username: "marcus", Password: "secret",
		Tags: map[string]string{"tls_skip_verify": "true"}}

	if err := NewCiscoRoomOSAdapter(cfg).Connect(context.Background()); err == nil {
		t.Error("RoomOS adapter (TLS 1.2+) unexpectedly reached a TLS 1.0-only codec")
	}
	if err := NewCiscoTCAdapter(cfg).Connect(context.Background()); err != nil {
		t.Fatalf("TC adapter: %v", err)
	}
}
