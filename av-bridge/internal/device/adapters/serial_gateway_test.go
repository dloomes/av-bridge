package adapters

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

// fakeRXTAPI answers RXT Control API lines with canned JSON, keyed by the
// command's first word, and records every line it gets.
type fakeRXTAPI struct {
	ln      net.Listener
	replies map[string]string

	mu    sync.Mutex
	lines []string
}

func newFakeRXTAPI(t *testing.T, replies map[string]string) *fakeRXTAPI {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRXTAPI{ln: ln, replies: replies}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := strings.TrimSpace(sc.Text())
					f.mu.Lock()
					f.lines = append(f.lines, line)
					f.mu.Unlock()
					reply, ok := f.replies[strings.Fields(line)[0]]
					if !ok {
						reply = `{"status":"ERROR","error":"INVALID_COMMAND"}`
					}
					c.Write([]byte(reply + "\r\n"))
				}
			}()
		}
	}()
	return f
}

func (f *fakeRXTAPI) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func gatewayFor(t *testing.T, f *fakeRXTAPI, tags map[string]string) *serialGateway {
	t.Helper()
	cfg := config.DeviceConfig{ID: "proj", Protocol: "nec_projector", Address: "127.0.0.1", Tags: tags}
	gw, err := serialGatewayFromConfig(cfg)
	if err != nil || gw == nil {
		t.Fatalf("gateway: %v %v", gw, err)
	}
	gw.apiAddr = f.ln.Addr().String() // the fake listens on a random port
	return gw
}

func TestSerialGatewayConfig(t *testing.T) {
	base := config.DeviceConfig{ID: "p", Protocol: "nec_projector", Address: "10.0.0.5"}

	if gw, err := serialGatewayFromConfig(base); gw != nil || err != nil {
		t.Fatalf("no tag: got %v %v, want nil nil", gw, err)
	}

	c := base
	c.Tags = map[string]string{"via_rxt_serial": "2"}
	gw, err := serialGatewayFromConfig(c)
	if err != nil || gw.dataAddr != "10.0.0.5:6901" || gw.apiAddr != "10.0.0.5:6975" || gw.port != 2 {
		t.Fatalf("port 2 default: %+v %v", gw, err)
	}

	c.Tags = map[string]string{"via_rxt_serial": "1", "soip_tcp_port": "7001", "serial_settings": " 9600  8 1 n none "}
	gw, _ = serialGatewayFromConfig(c)
	if gw.dataAddr != "10.0.0.5:7001" || gw.settings != "9600 8 1 n none" {
		t.Fatalf("explicit port/settings: %+v", gw)
	}

	for _, bad := range []map[string]string{
		{"via_rxt_serial": "3"},
		{"via_rxt_serial": "1", "soip_tcp_port": "x"},
	} {
		c.Tags = bad
		if _, err := serialGatewayFromConfig(c); err == nil {
			t.Errorf("tags %v: want an error", bad)
		}
	}

	c.Tags = map[string]string{"via_rxt_serial": "1"}
	c.Protocol = "sony_bravia"
	if _, err := serialGatewayFromConfig(c); err == nil {
		t.Error("sony_bravia over a serial gateway should be refused")
	}
}

func TestSerialGatewayOpens(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{
		"config_rs232": `{"status":"SUCCESS","error":null}`,
		"enable_rs232": `{"status":"SUCCESS","error":null}`,
	})
	gw := gatewayFor(t, f, map[string]string{"via_rxt_serial": "1", "serial_settings": "38400 8 1 n none"})
	if err := gw.open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	got := f.got()
	want := []string{"config_rs232 1 38400 8 1 n none", "enable_rs232 1 soip 6900"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestSerialGatewayLeavesSettingsAlone(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{"enable_rs232": `{"status":"SUCCESS","soip_port":"6900","error":null}`})
	gw := gatewayFor(t, f, map[string]string{"via_rxt_serial": "1"})
	if err := gw.open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := f.got(); len(got) != 1 || got[0] != "enable_rs232 1 soip 6900" {
		t.Fatalf("sent %q, want only enable_rs232", got)
	}
}

func TestSerialGatewayAlreadySharedElsewhere(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{"enable_rs232": `{"status":"SUCCESS","soip_port":"6910","error":null}`})
	gw := gatewayFor(t, f, map[string]string{"via_rxt_serial": "1"})
	err := gw.open(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already shared on TCP 6910") {
		t.Fatalf("err = %v, want the existing TCP port named", err)
	}
}

func TestSerialGatewayPortInUse(t *testing.T) {
	f := newFakeRXTAPI(t, map[string]string{"enable_rs232": `{"status":"ERROR","error":"INVALID_STATE"}`})
	gw := gatewayFor(t, f, map[string]string{"via_rxt_serial": "1"})
	err := gw.open(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("err = %v, want the in-use hint", err)
	}
}

func TestFactoryWrapsGatewayDevice(t *testing.T) {
	cfg := config.DeviceConfig{ID: "p", Protocol: "nec_projector", Address: "10.0.0.5", Tags: map[string]string{"via_rxt_serial": "1"}}
	dev, err := New(cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	gd, ok := dev.(*gatewayDevice)
	if !ok {
		t.Fatalf("got %T, want *gatewayDevice", dev)
	}
	if gd.Info().Address != "10.0.0.5" {
		t.Errorf("Info().Address = %q, want the RXT address as configured", gd.Info().Address)
	}
	if inner := gd.Device.Info().Address; !strings.HasPrefix(inner, "10.0.0.5:6900") {
		t.Errorf("adapter address = %q, want the SOIP port", inner)
	}
}

func TestAddRS232Metrics(t *testing.T) {
	m := map[string]any{}
	addRS232Metrics(m, "rs232_1_", map[string]any{"rs232": map[string]any{
		"baud_rate": float64(115200), "data_bit": float64(8), "stop_bit": float64(1),
		"parity": "n", "handshaking": "none", "signal_level": "RS232",
	}})
	if m["rs232_1_baud"] != 115200 || m["rs232_1_format"] != "8N1" || m["rs232_1_handshaking"] != "none" || m["rs232_1_signal_level"] != "RS232" {
		t.Fatalf("metrics = %v", m)
	}
}
