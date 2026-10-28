package adapters

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Replies as the DIDO Protocol Guide v0.0.9 prints them where it does
// (preset_list), and plausible shapes where it only says "returns ...".
var didoReplies = map[string]string{
	"get_fw_version":       `{"status":"SUCCESS","fw_version":"1.0.4","error":null}`,
	"get_api_version":      `{"status":"SUCCESS","api_version":"0.0.9","error":null}`,
	"get_serial_number":    `{"status":"SUCCESS","serial_number":"DIDO123","error":null}`,
	"get_mac":              `{"status":"SUCCESS","mac":"00:11:22:33:44:55","error":null}`,
	"get_ip":               `{"status":"SUCCESS","ip":"192.168.1.50","mode":"static","error":null}`,
	"get_video_route":      `{"status":"SUCCESS","Video Route":{"out1":"in2","out2":"IN1","out3":"3","out4":{"source":"in4"}},"error":null}`,
	"get_vid_resl_mode":    `{"status":"SUCCESS","mode":"B","error":null}`,
	"get_vid_resl current": `{"status":"SUCCESS","out1":"1080p60","out2":"2160p30","out3":"1080p60","out4":"1080p60","error":null}`,
	"get_audio_route":      `{"status":"SUCCESS","lineout":"hdmi","dante":"linein","out1":"trace","out2":"Dante","out3":"trace","out4":"mute","error":null}`,
	"get_volume lineout":   `{"status":"SUCCESS","volume":"85","error":null}`,
	"get_volume linein":    `{"status":"SUCCESS","volume":"40","mute":"0","error":null}`,
	"get_button_lock":      `{"status":"SUCCESS","button_lock":"disable","error":null}`,
	"preset_list":          `{"status":"SUCCESS","Presets":["window_cross","vwall_2x2"],"error":null}`,
}

func newDIDO(t *testing.T, replies map[string]string) (*fakeVPX, *AuroraDIDOAdapter) {
	t.Helper()
	f := newFakeVPX(t, replies)
	a := NewAuroraDIDOAdapter(config.DeviceConfig{ID: "dido-1", Name: "Court 3 DIDO", Type: "control",
		Protocol: "aurora_dido", Address: f.ln.Addr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if err := a.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { a.Disconnect() })
	return f, a
}

func TestAuroraDIDOPoll(t *testing.T) {
	_, a := newDIDO(t, didoReplies)
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tel.Status != device.StatusOnline {
		t.Fatalf("status %s (%s)", tel.Status, tel.Error)
	}
	want := map[string]any{
		"fw_version": "1.0.4", "api_version": "0.0.9", "serial_number": "DIDO123",
		"mac": "00:11:22:33:44:55", "ip_address": "192.168.1.50", "ip_mode": "static",
		"out1_source": "in2", "out2_source": "in1", "out3_source": "in3", "out4_source": "in4",
		"resolution_mode": "bypass", "out2_resolution": "2160p30",
		"lineout_audio": "hdmi", "out2_audio": "dante", "out4_audio": "mute",
		"lineout_volume": 85, "linein_volume": 40, "linein_mute": false,
		"buttons_locked": false,
		"presets":        "window_cross, vwall_2x2", "preset_count": 2,
	}
	for k, v := range want {
		if tel.Metrics[k] != v {
			t.Errorf("%s = %#v, want %#v", k, tel.Metrics[k], v)
		}
	}
	for k := range tel.Metrics {
		if strings.HasSuffix(k, "_reply") || k == "read_error" {
			t.Errorf("unexpected %s: %v", k, tel.Metrics[k])
		}
	}
}

// A reply the adapter can't read shows on the device page as it came.
func TestAuroraDIDOUnreadReply(t *testing.T) {
	replies := map[string]string{}
	for k, v := range didoReplies {
		replies[k] = v
	}
	replies["get_video_route"] = `{"status":"SUCCESS","routing":"1,2,3,4","error":null}`
	_, a := newDIDO(t, replies)
	tel, _ := a.Poll(context.Background())
	if got, _ := tel.Metrics["get_video_route_reply"].(string); !strings.Contains(got, `"routing":"1,2,3,4"`) {
		t.Errorf("get_video_route_reply = %q", got)
	}
	if tel.Metrics["fw_version"] != "1.0.4" || tel.Metrics["preset_count"] != 2 {
		t.Errorf("other reads lost: %v", tel.Metrics)
	}
}

// A read the firmware doesn't answer drops the connection; what was read
// before it stays, and the device is still online.
func TestAuroraDIDOHungRead(t *testing.T) {
	replies := map[string]string{}
	for k, v := range didoReplies {
		replies[k] = v
	}
	delete(replies, "get_audio_route")
	_, a := newDIDO(t, replies)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline || tel.Metrics["out1_source"] != "in2" {
		t.Fatalf("status %s, metrics %v", tel.Status, tel.Metrics)
	}
	if _, ok := tel.Metrics["read_error"]; !ok {
		t.Error("no read_error for the read that hung")
	}
}

func TestAuroraDIDOCommands(t *testing.T) {
	replies := map[string]string{"get_fw_version": didoReplies["get_fw_version"]}
	ok := `{"status":"SUCCESS","error":null}`
	for _, w := range []string{
		"set_video_route in3 out2", "set_video_route in1 out1", "set_video_route in1 out2",
		"set_video_route in1 out3", "set_video_route in1 out4", "set_volume lineout 70",
		"set_volume linein mute", "preset_recall vwall_2x2", "button_lock enable",
	} {
		replies[w] = ok
	}
	replies["preset_recall missing"] = `{"status":"FAILURE","error":"preset not found"}`
	f, a := newDIDO(t, replies)
	ctx := context.Background()
	send := func(name string, args map[string]any) error {
		_, err := a.SendCommand(ctx, device.CommandRequest{Name: name, Args: args})
		return err
	}

	for _, c := range []struct {
		name string
		args map[string]any
		wire string
	}{
		{"route_out2", map[string]any{"input": float64(3)}, "set_video_route in3 out2"},
		{"set_volume", map[string]any{"level": float64(70)}, "set_volume lineout 70"},
		{"mute_linein", nil, "set_volume linein mute"},
		{"recall_preset", map[string]any{"name": "vwall_2x2"}, "preset_recall vwall_2x2"},
		{"lock_buttons", nil, "button_lock enable"},
	} {
		if err := send(c.name, c.args); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !f.got(c.wire) {
			t.Errorf("%s didn't send %q", c.name, c.wire)
		}
	}
	if err := send("route_all", map[string]any{"input": "in1"}); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		if w := "set_video_route in1 out" + string(rune('0'+n)); !f.got(w) {
			t.Errorf("route_all didn't send %q", w)
		}
	}

	// The device's own error comes back; bad args are refused unsent.
	if err := send("recall_preset", map[string]any{"name": "missing"}); err == nil || !strings.Contains(err.Error(), "preset not found") {
		t.Errorf("failed recall: %v", err)
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"route_out1", map[string]any{"input": float64(5)}},
		{"route_out1", nil},
		{"set_volume", map[string]any{"level": float64(101)}},
		{"recall_preset", map[string]any{"name": "two words"}},
		{"power_on", nil},
	} {
		if err := send(c.name, c.args); err == nil {
			t.Errorf("%s %v: no error", c.name, c.args)
		}
	}
}

// Reboot with no reply is still a reboot, and the next poll reconnects.
func TestAuroraDIDOReboot(t *testing.T) {
	f, a := newDIDO(t, didoReplies)
	resp, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "reboot"})
	if err != nil || resp.Parsed["status"] != "REBOOTING" {
		t.Fatalf("reboot: %v %v", resp, err)
	}
	if !f.got("reboot") {
		t.Fatal("reboot not sent")
	}
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOnline {
		t.Fatalf("after reboot: %s (%s)", tel.Status, tel.Error)
	}
}

// A DIDO with no free connections says so in plain text and closes. The
// error says what that means, and Connect holds off for a while rather
// than dialling again straight away.
func TestAuroraDIDOConnectionsFull(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var dials atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			// Read the command first: closing with it unread resets the
			// connection on Windows, losing the message.
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			_, _ = c.Read(make([]byte, 256))
			_, _ = c.Write([]byte("connections maxed out, cannot open more\r\n"))
			_ = c.Close()
		}
	}()
	old := didoFullWait
	didoFullWait = 300 * time.Millisecond
	defer func() { didoFullWait = old }()

	a := NewAuroraDIDOAdapter(config.DeviceConfig{ID: "dido-1", Protocol: "aurora_dido", Address: ln.Addr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = a.Connect(ctx)
	if !errors.Is(err, errDIDOFull) || !strings.Contains(err.Error(), "no free control connections") {
		t.Fatalf("first connect: %v", err)
	}
	// Held off: no second dial.
	if err := a.Connect(ctx); !errors.Is(err, errDIDOFull) || dials.Load() != 1 {
		t.Fatalf("second connect: %v, dials %d", err, dials.Load())
	}
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "no free control connections") || dials.Load() != 1 {
		t.Fatalf("poll while full: %s %q, dials %d", tel.Status, tel.Error, dials.Load())
	}
	// After the wait it tries again.
	time.Sleep(350 * time.Millisecond)
	_ = a.Connect(ctx)
	if dials.Load() != 2 {
		t.Fatalf("dials after the wait: %d, want 2", dials.Load())
	}
}
