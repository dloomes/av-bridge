package adapters

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// fakeVPX is a minimal VPX telnet endpoint. replies maps a command to the
// raw reply line; a missing command gets no reply at all (a hung device).
type fakeVPX struct {
	ln       net.Listener
	mu       sync.Mutex
	replies  map[string]string
	received []string
	conns    int
}

func newFakeVPX(t *testing.T, replies map[string]string) *fakeVPX {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVPX{ln: ln, replies: replies}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeVPX) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		f.mu.Lock()
		f.received = append(f.received, cmd)
		reply, ok := f.replies[cmd]
		f.mu.Unlock()
		if ok {
			c.Write([]byte(reply + "\r\n"))
		}
	}
}

func (f *fakeVPX) got(cmd string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.received {
		if c == cmd {
			return true
		}
	}
	return false
}

const (
	vpxVersion = `{"status":"SUCCESS","fw_version":"3.2.14","error":"NULL"}`
	vpxStatus  = `{"status":"SUCCESS","result":"s_srv_on","error":"NULL"}`
	vpxOK      = `{"status":"SUCCESS","result":"ok","error":"NULL"}`
)

func newTestVPX(f *fakeVPX) *AuroraVPXAdapter {
	return NewAuroraVPXAdapter(config.DeviceConfig{ID: "vpx", Address: f.ln.Addr().String(), PollRate: time.Minute})
}

// The failure behind "mode not detected yet": get settings comes back as
// something that isn't valid JSON. Mode is recovered from the raw reply,
// commands still work, and the device page gets the reason.
func TestVPXMalformedSettings(t *testing.T) {
	f := newFakeVPX(t, map[string]string{
		"version":      vpxVersion,
		"get status":   vpxStatus,
		"get settings": `{"status":"SUCCESS","settings":{"hdmi":{"tx":"n"},"error":"NULL,"command":"x"}}`,
		"mute av on":   vpxOK,
		// The other per-poll reads answer normally.
		"get hotplug_status": vpxOK,
		"get linkspeed":      vpxOK,
		"get video_encr":     vpxOK,
	})
	a := newTestVPX(f)
	ctx := context.Background()
	if err := a.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if a.getMode() != "decoder" {
		t.Errorf("mode = %q, want decoder recovered from the raw reply", a.getMode())
	}
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: vpxCmdMute}); err != nil {
		t.Fatalf("mute after a bad settings reply: %v", err)
	}
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline {
		t.Errorf("poll status = %s, want online", tel.Status)
	}
	if msg, _ := tel.Metrics["settings_error"].(string); !strings.Contains(msg, "device sent") {
		t.Errorf("settings_error should quote the device's reply, got %q", msg)
	}
	// And the next command still works after that poll dropped the conn.
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: vpxCmdMute}); err != nil {
		t.Fatalf("mute after poll: %v", err)
	}
}

// Mode unknown (get settings never answered) must not block commands:
// the device decides.
func TestVPXUnknownModeSendsCommand(t *testing.T) {
	f := newFakeVPX(t, map[string]string{
		"version":    vpxVersion,
		"get status": vpxStatus,
		"mute av on": vpxOK,
	})
	a := newTestVPX(f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if a.getMode() != "" {
		t.Fatalf("mode should be unknown, got %q", a.getMode())
	}
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: vpxCmdMute}); err != nil {
		t.Fatalf("mute with unknown mode: %v", err)
	}
	if !f.got("mute av on") {
		t.Fatal("mute should have reached the device")
	}
	// A known, wrong mode is still refused locally.
	a.modeMu.Lock()
	a.mode = "encoder"
	a.modeMu.Unlock()
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: vpxCmdMute}); err == nil {
		t.Fatal("mute on a known encoder should be refused")
	}
}

// A request that times out must not poison the connection for the next
// poll (json.Decoder errors are sticky).
func TestVPXRecoversAfterTimeout(t *testing.T) {
	settings := `{"status":"SUCCESS","settings":{"mac":"001102","hdmi":{"tx":"y"}}}`
	f := newFakeVPX(t, map[string]string{
		"version":      vpxVersion,
		"get status":   vpxStatus,
		"get settings": settings,
		// get hotplug_status / linkspeed / video_encr never answer
	})
	a := newTestVPX(f)
	ctx := context.Background()
	if err := a.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	a.Poll(ctx) // times out on hotplug_status, drops the connection
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline {
		t.Fatalf("second poll: %s (%s), want online after reconnecting", tel.Status, tel.Error)
	}
	if a.getMode() != "encoder" {
		t.Errorf("mode = %q, want encoder", a.getMode())
	}
}
