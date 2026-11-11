package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

func vpxSettingsReply(rs232 string) string {
	return `{"status":"SUCCESS","settings":{"streams":{"rs232":` + rs232 + `}}}`
}

func vpxGatewayFor(t *testing.T, f *fakeRXTAPI, tags map[string]string) *vpxSerialGateway {
	t.Helper()
	cfg := config.DeviceConfig{ID: "proj", Protocol: "nec_projector", Address: "127.0.0.1", Tags: tags}
	gw, err := vpxSerialGatewayFromConfig(cfg)
	if err != nil || gw == nil {
		t.Fatalf("gateway: %v %v", gw, err)
	}
	gw.apiAddr = f.ln.Addr().String()
	return gw
}

func TestVPXSerialGatewayConfig(t *testing.T) {
	c := config.DeviceConfig{ID: "p", Protocol: "nec_projector", Address: "10.0.0.7",
		Tags: map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752", "serial_settings": "38400 8 1 n"}}
	gw, err := vpxSerialGatewayFromConfig(c)
	if err != nil || gw.dataAddr != "10.0.0.7:6752" || gw.apiAddr != "10.0.0.7:6970" || gw.settings != "38400-8n1" {
		t.Fatalf("got %+v %v", gw, err)
	}

	c.Tags = map[string]string{"via_vpx_serial": "true"}
	if _, err := vpxSerialGatewayFromConfig(c); err == nil || !strings.Contains(err.Error(), "soip_tcp_port is required") {
		t.Errorf("missing port: err = %v", err)
	}

	for in, want := range map[string]string{"9600-8n1": "9600-8n1", "19200 8 1 even": "19200-8e1", "": ""} {
		if got, err := vpxSerialSettings(in); err != nil || got != want {
			t.Errorf("vpxSerialSettings(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"9600", "fast 8 1 n", "9600 9 1 n", "9600 8 1 x"} {
		if _, err := vpxSerialSettings(bad); err == nil {
			t.Errorf("vpxSerialSettings(%q): want an error", bad)
		}
	}

	c.Tags = map[string]string{"via_vpx_serial": "true", "via_rxt_serial": "1", "soip_tcp_port": "1"}
	if _, _, err := gatewayFromConfig(c); err == nil {
		t.Error("both gateway tags set: want an error")
	}
}

func TestVPXSerialGatewayEnablesWhenOff(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"get": vpxSettingsReply(`{"soip_status":"off","mode":"","baudrate":"115200","data_bit":"8","stop_bit":"1","parity":"n","soip_connected":"false","soip_ip":""}`),
		"set": `{"status":"SUCCESS","error":"NULL"}`,
	})
	gw := vpxGatewayFor(t, f, map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752", "serial_settings": "38400 8 1 n"})
	if err := gw.open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := f.got(); len(got) != 2 || got[1] != "set soip_enable y passthrough 38400-8n1" {
		t.Fatalf("sent %q", got)
	}
}

func TestVPXSerialGatewayKeepsCurrentSettings(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"get": vpxSettingsReply(`{"soip_status":"off","baudrate":"9600","data_bit":"8","stop_bit":"1","parity":"none"}`),
		"set": `{"status":"SUCCESS","error":"NULL"}`,
	})
	gw := vpxGatewayFor(t, f, map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752"})
	if err := gw.open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := f.got(); len(got) != 2 || got[1] != "set soip_enable y passthrough 9600-8n1" {
		t.Fatalf("sent %q", got)
	}
}

func TestVPXSerialGatewayAlreadyOnLeavesItAlone(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"get": vpxSettingsReply(`{"soip_status":"on","mode":"telnet","baudrate":"38400","data_bit":"8","stop_bit":"1","parity":"n","soip_connected":"false"}`),
	})
	gw := vpxGatewayFor(t, f, map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752", "serial_settings": "38400-8n1"})
	if err := gw.open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := f.got(); len(got) != 1 {
		t.Fatalf("sent %q, want only get settings", got)
	}
}

func TestVPXSerialGatewayRefusesRedirection(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"get": vpxSettingsReply(`{"soip_status":"on","mode":"redirection","baudrate":"9600","data_bit":"8","stop_bit":"1","parity":"n","soip_connected":"true","soip_ip":"10.0.0.9"}`),
	})
	gw := vpxGatewayFor(t, f, map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752"})
	if err := gw.open(context.Background()); err == nil || !strings.Contains(err.Error(), "routed to another VPX") {
		t.Fatalf("err = %v", err)
	}
	if got := f.got(); len(got) != 1 {
		t.Fatalf("sent %q, want nothing changed", got)
	}
}

func TestVPXSerialGatewayRefusesAnotherClient(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"get": vpxSettingsReply(`{"soip_status":"on","mode":"telnet","baudrate":"9600","data_bit":"8","stop_bit":"1","parity":"n","soip_connected":"true","soip_ip":"10.0.0.9"}`),
	})
	gw := vpxGatewayFor(t, f, map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752"})
	if err := gw.open(context.Background()); err == nil || !strings.Contains(err.Error(), "in use by 10.0.0.9") {
		t.Fatalf("err = %v", err)
	}
}

func TestFactoryWrapsVPXGatewayDevice(t *testing.T) {
	cfg := config.DeviceConfig{ID: "p", Protocol: "nec_projector", Address: "10.0.0.7",
		Tags: map[string]string{"via_vpx_serial": "true", "soip_tcp_port": "6752"}}
	dev, err := New(cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	gd, ok := dev.(*gatewayDevice)
	if !ok || gd.Info().Address != "10.0.0.7" || !strings.HasPrefix(gd.Device.Info().Address, "10.0.0.7:6752") {
		t.Fatalf("got %T %+v", dev, dev.Info())
	}
}
