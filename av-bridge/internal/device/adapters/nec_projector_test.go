package adapters

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// fakeNEC is an NEC projector speaking the BDT140013 protocol on a local port.
type fakeNEC struct {
	ln net.Listener

	mu       sync.Mutex
	state    string // on, standby, warming, cooling
	silent   bool   // accept the connection but never reply (mid power change)
	t1, t2   byte   // selection signal types
	picMute  byte
	sndMute  byte
	volume   int
	errBytes []byte // [009] DATA01-12
	no078    bool   // only 305-3 for input / mute (asterisked models)
	cmds     [][]byte
}

func newFakeNEC(t *testing.T) *fakeNEC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNEC{ln: ln, state: "on", t1: 0x01, t2: 0x21, volume: 10, errBytes: make([]byte, 12)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeNEC) adapter(tags map[string]string) *NECProjectorAdapter {
	return NewNECProjectorAdapter(config.DeviceConfig{
		ID: "proj1", Protocol: "nec_projector", Type: "projector",
		Address: f.ln.Addr().String(), Tags: tags,
	})
}

func (f *fakeNEC) serve(c net.Conn) {
	defer c.Close()
	for {
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		rest := make([]byte, int(hdr[4])+1)
		if _, err := io.ReadFull(c, rest); err != nil {
			return
		}
		frame := append(hdr, rest...)
		if necChecksum(frame[:len(frame)-1]) != frame[len(frame)-1] {
			continue
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, frame)
		silent := f.silent
		f.mu.Unlock()
		if silent {
			continue
		}
		c.Write(f.reply(frame[:len(frame)-1]))
	}
}

// necTestReply builds a successful reply: CMD1|20h CMD2 ID1 ID2 LEN DATA CKS.
func necTestReply(cmd []byte, data ...byte) []byte {
	b := append([]byte{cmd[0] | 0x20, cmd[1], 0x01, 0x2A, byte(len(data))}, data...)
	return append(b, necChecksum(b))
}

// necTestError builds a failed reply: CMD1|A0h CMD2 ID1 ID2 02h ERR1 ERR2 CKS.
func necTestError(cmd []byte, e1, e2 byte) []byte {
	b := []byte{cmd[0] | 0xA0, cmd[1], 0x01, 0x2A, 0x02, e1, e2}
	return append(b, necChecksum(b))
}

func le32(v int32) []byte {
	u := uint32(v)
	return []byte{byte(u), byte(u >> 8), byte(u >> 16), byte(u >> 24)}
}

func le16(v int) []byte { u := uint16(int16(v)); return []byte{byte(u), byte(u >> 8)} }

func padString(s string, n int) []byte {
	b := make([]byte, n)
	copy(b, s)
	return b
}

func (f *fakeNEC) reply(cmd []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	on := f.state == "on"
	key := string(cmd[:2])
	sub := byte(0)
	if len(cmd) > 5 {
		sub = cmd[5]
	}
	switch {
	case key == "\x00\x85" && sub == 0x01: // 078-2
		d := make([]byte, 16)
		switch f.state {
		case "on":
			d[2], d[5] = 0x01, 0x04
		case "standby":
			d[2], d[5] = 0x00, 0x10
		case "cooling":
			d[2], d[3], d[4], d[5] = 0x00, 0x01, 0x01, 0x05
		case "warming":
			d[2], d[4], d[5] = 0x01, 0x01, 0x04
		}
		return necTestReply(cmd, d...)
	case key == "\x00\x88": // 009
		return necTestReply(cmd, f.errBytes...)
	case key == "\x03\x96": // 037-4: lamp 1 usage 18000 s (the manual's example) / life 87 %
		if cmd[6] == 0x01 {
			return necTestReply(cmd, append([]byte{0x00, 0x01}, 0x50, 0x46, 0x00, 0x00)...)
		}
		return necTestReply(cmd, append([]byte{0x00, 0x04}, le32(87)...)...)
	case key == "\x03\x95": // 037-3: filter 7200 s, alarm undefined (-1)
		return necTestReply(cmd, append(le32(7200), le32(-1)...)...)
	case key == "\x00\x85" && sub == 0x04: // 078-5
		return necTestReply(cmd, padString("NP-PA804UL", 32)...)
	case key == "\x00\xBF" && sub == 0x01: // 305-2
		return necTestReply(cmd, append([]byte{0x01, 0x06}, padString("2X00012AB", 16)...)...)
	case key == "\x03\xB0" && sub == 0x9A: // 097-155
		return necTestReply(cmd, 0x9A, 0x00, 0x00, 0x10, 0x2F, 0xAA, 0xBB, 0xCC)
	case key == "\x02\x00": // 015 POWER ON
		if f.state == "cooling" {
			return necTestError(cmd, 0x02, 0x0D)
		}
		f.state = "on"
		return necTestReply(cmd)
	case key == "\x02\x01": // 016 POWER OFF
		f.state = "standby"
		return necTestReply(cmd)
	}
	if !on {
		return necTestError(cmd, 0x02, 0x0D) // refused in standby
	}
	switch {
	case key == "\x00\x85" && sub == 0x02: // 078-3
		if f.no078 {
			return necTestError(cmd, 0x00, 0x01)
		}
		d := make([]byte, 16)
		d[1], d[2], d[3], d[8] = 0x00, f.t1, f.t2, 0x00
		return necTestReply(cmd, d...)
	case key == "\x00\x85" && sub == 0x03: // 078-4
		if f.no078 {
			return necTestError(cmd, 0x00, 0x01)
		}
		d := make([]byte, 16)
		d[0], d[1], d[2] = f.picMute, f.sndMute, 0x00
		return necTestReply(cmd, d...)
	case key == "\x00\xBF" && sub == 0x02: // 305-3
		d := make([]byte, 16)
		d[0], d[1], d[2], d[3], d[4], d[6], d[7], d[8] = 0x02, 0x04, 0x01, f.t1, f.t2, f.picMute, f.sndMute, 0x01
		return necTestReply(cmd, d...)
	case key == "\x03\x05": // 060-1 volume: range 0–31
		d := []byte{0x02}
		d = append(d, le16(31)...)
		d = append(d, le16(0)...)
		d = append(d, le16(15)...)
		d = append(d, le16(f.volume)...)
		d = append(d, make([]byte, 7)...)
		return necTestReply(cmd, d...)
	case key == "\x02\x03": // 018
		if cmd[6] == 0x99 {
			return necTestError(cmd, 0x01, 0x01)
		}
		return necTestReply(cmd, 0x00)
	case key == "\x03\x10": // 030-2
		if cmd[7] == 0x00 {
			f.volume = int(cmd[8])
		} else {
			f.volume += int(int16(uint16(cmd[8]) | uint16(cmd[9])<<8))
		}
		return necTestReply(cmd, 0x00, 0x00)
	case key == "\x02\x10":
		f.picMute = 1
		return necTestReply(cmd)
	case key == "\x02\x11":
		f.picMute = 0
		return necTestReply(cmd)
	case key == "\x02\x12":
		f.sndMute = 1
		return necTestReply(cmd)
	case key == "\x02\x13":
		f.sndMute = 0
		return necTestReply(cmd)
	case key == "\x02\x16", key == "\x02\x17":
		return necTestReply(cmd)
	}
	return necTestError(cmd, 0x00, 0x00)
}

func (f *fakeNEC) lastCmd() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cmds) == 0 {
		return nil
	}
	return f.cmds[len(f.cmds)-1]
}

func (f *fakeNEC) set(fn func(*fakeNEC)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func necCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNECChecksumManualExample(t *testing.T) {
	// §2.2: 20h 81h 01h 60h 01h 00h → 103h → 03h.
	if got := necChecksum([]byte{0x20, 0x81, 0x01, 0x60, 0x01, 0x00}); got != 0x03 {
		t.Errorf("checksum = %02Xh, want 03h", got)
	}
}

func TestNECCommandFramesMatchManual(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  []byte
		want []byte
	}{
		{"009", necCmdErrorStatus, []byte{0x00, 0x88, 0x00, 0x00, 0x00, 0x88}},
		{"015", necCmdPowerOn, []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}},
		{"016", necCmdPowerOff, []byte{0x02, 0x01, 0x00, 0x00, 0x00, 0x03}},
		{"018 video example", necInputCommand(0x06), []byte{0x02, 0x03, 0x00, 0x00, 0x02, 0x01, 0x06, 0x0E}},
		{"020", necCmdPictureMuteOn, []byte{0x02, 0x10, 0x00, 0x00, 0x00, 0x12}},
		{"021", necCmdPictureMuteOf, []byte{0x02, 0x11, 0x00, 0x00, 0x00, 0x13}},
		{"022", necCmdSoundMuteOn, []byte{0x02, 0x12, 0x00, 0x00, 0x00, 0x14}},
		{"023", necCmdSoundMuteOff, []byte{0x02, 0x13, 0x00, 0x00, 0x00, 0x15}},
		{"030-2 volume 10", necVolumeCommand(false, 10), []byte{0x03, 0x10, 0x00, 0x00, 0x05, 0x05, 0x00, 0x00, 0x0A, 0x00, 0x27}},
		{"037-3", necCmdFilterUsage, []byte{0x03, 0x95, 0x00, 0x00, 0x00, 0x98}},
		{"037-4", necCmdLampHours, []byte{0x03, 0x96, 0x00, 0x00, 0x02, 0x00, 0x01, 0x9C}},
		{"051", necCmdShutterClose, []byte{0x02, 0x16, 0x00, 0x00, 0x00, 0x18}},
		{"052", necCmdShutterOpen, []byte{0x02, 0x17, 0x00, 0x00, 0x00, 0x19}},
		{"078-2", necCmdRunningStatus, []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x01, 0x87}},
		{"078-3", necCmdInputStatus, []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x02, 0x88}},
		{"078-4", necCmdMuteStatus, []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x03, 0x89}},
		{"078-5", necCmdModelName, []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x04, 0x8A}},
		{"097-155", necCmdMACAddress, []byte{0x03, 0xB0, 0x00, 0x00, 0x02, 0x9A, 0x00, 0x4F}},
		{"305-2", necCmdSerialNumber, []byte{0x00, 0xBF, 0x00, 0x00, 0x02, 0x01, 0x06, 0xC8}},
		{"305-3", necCmdBasicInfo, []byte{0x00, 0xBF, 0x00, 0x00, 0x01, 0x02, 0xC2}},
	} {
		if got := necFrame(0, c.cmd); !bytes.Equal(got, c.want) {
			t.Errorf("%s: % X, want % X", c.name, got, c.want)
		}
	}
	// Relative volume -1 is FFFFh.
	if got := necVolumeCommand(true, -1); !bytes.Equal(got[7:], []byte{0x01, 0xFF, 0xFF}) {
		t.Errorf("volume -1 = % X", got)
	}
}

func TestNECParsers(t *testing.T) {
	// 078-2: on / cooling / warming / network standby.
	for _, c := range []struct {
		d    []byte
		want string
	}{
		{[]byte{0, 0, 0x01, 0x00, 0x00, 0x04}, "on"},
		{[]byte{0, 0, 0x00, 0x01, 0x01, 0x05}, "cooling"},
		{[]byte{0, 0, 0x01, 0x00, 0x01, 0x04}, "warming"},
		{[]byte{0, 0, 0x00, 0x00, 0x00, 0x10}, "standby"},
	} {
		rs, ok := necParseRunningStatus(append(c.d, make([]byte, 10)...))
		if !ok || rs.power != c.want {
			t.Errorf("078-2 % X: %q, want %q", c.d, rs.power, c.want)
		}
	}

	// 078-3: HDMI2 (type 1 = 02h, type 2 = 21h), no signal.
	in, ok := necParseInputStatus([]byte{0x00, 0x01, 0x02, 0x21, 0x00, 0x00, 0, 0, 0x01, 0, 0, 0, 0, 0, 0, 0})
	m := map[string]any{}
	in.apply(m)
	if !ok || m["current_input"] != "hdmi2" || m["signal_present"] != false || m["display_content"] != "no_signal" {
		t.Errorf("078-3: %v", m)
	}
	for _, c := range []struct {
		t1, t2 byte
		want   string
	}{{1, 0x21, "hdmi1"}, {1, 0x22, "displayport"}, {1, 0x27, "hdbaset"}, {1, 0x01, "computer"}, {2, 0x01, "computer2"}, {1, 0x99, "signal_01_99"}} {
		if got := necInputLabel(c.t1, c.t2); got != c.want {
			t.Errorf("input %02X/%02X = %q, want %q", c.t1, c.t2, got, c.want)
		}
	}

	// 078-4: picture mute on, sound off, onscreen not supported.
	mu, _ := necParseMuteStatus([]byte{0x01, 0x00, 0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	m = map[string]any{}
	mu.apply(m)
	if m["picture_mute"] != true || m["sound_mute"] != false {
		t.Errorf("078-4: %v", m)
	}
	if _, ok := m["onscreen_mute"]; ok {
		t.Error("onscreen_mute FFh (not supported) should be left out")
	}

	// 009: cover (DATA01 bit0), fan (DATA01 bit3), temperature sensor
	// (DATA03 bit2), interlock (DATA09 bit1); lamp off ignored in standby.
	d := make([]byte, 12)
	d[0], d[2], d[8] = 0x01|0x08|0x40, 0x04, 0x02
	faults := necDecodeErrors(d, false)
	if len(faults) != 4 {
		t.Fatalf("009 standby faults = %v", faults)
	}
	var descr []string
	for _, f := range faults {
		descr = append(descr, f.(map[string]any)["description"].(string))
	}
	joined := strings.Join(descr, "|")
	for _, want := range []string{"Cover error", "Fan error", "Temperature error (temperature sensor)", "Interlock switch is open"} {
		if !strings.Contains(joined, want) {
			t.Errorf("009 faults %q missing %q", joined, want)
		}
	}
	if n := len(necDecodeErrors(d, true)); n != 5 {
		t.Errorf("009 powered on: %d faults, want 5 (lamp off counts)", n)
	}
	if n := len(necDecodeErrors(make([]byte, 12), true)); n != 0 {
		t.Errorf("no error bits: %d faults", n)
	}

	// 037-4: the manual's example, 18000 s.
	if v, ok := necLampValue([]byte{0x00, 0x01, 0x50, 0x46, 0x00, 0x00}, 0x01); !ok || v != 18000 {
		t.Errorf("037-4 = %d %v, want 18000", v, ok)
	}
	if v, ok := necLampValue(append([]byte{0x00, 0x04}, le32(-3)...), 0x04); !ok || v != -3 {
		t.Errorf("037-4 negative life = %d", v)
	}

	// 305-3: on, video displayed, HDMI1, picture mute on.
	bi, ok := necParseBasicInfo([]byte{0x02, 0x04, 0x00, 0x01, 0x21, 0xFF, 0x01, 0x00, 0x00, 0x00, 0, 0, 0, 0, 0, 0})
	m = map[string]any{}
	bi.input.apply(m)
	bi.mute.apply(m)
	if !ok || bi.operation != "power_on" || m["current_input"] != "hdmi1" || m["signal_present"] != true || m["picture_mute"] != true {
		t.Errorf("305-3: %v %+v", m, bi)
	}
	if _, ok := necParseBasicInfo([]byte{0x00, 0x04}); ok {
		t.Error("305-3 without the 02h echo should not parse")
	}
}

func TestNECPollOn(t *testing.T) {
	f := newFakeNEC(t)
	f.set(func(f *fakeNEC) { f.errBytes[0] = 0x08; f.picMute = 1 })
	a := f.adapter(nil)
	if err := a.Connect(necCtx(t)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tel, _ := a.Poll(necCtx(t))
	if tel.Status != device.StatusOnline {
		t.Fatalf("status %v: %s", tel.Status, tel.Error)
	}
	want := map[string]any{
		"power_status": "on", "operation_status": "power_on",
		"current_input": "hdmi1", "signal_present": true, "display_content": "video",
		"picture_mute": true, "sound_mute": false, "onscreen_mute": false, "volume": 10,
		"light_source_hours": 5, "light_source_life_pct": 87, "filter_hours": 2,
		"fault_count": 1,
		"model":       "NP-PA804UL", "serial_number": "2X00012AB", "mac_address": "00:10:2F:AA:BB:CC",
	}
	for k, v := range want {
		if tel.Metrics[k] != v {
			t.Errorf("%s = %#v, want %#v", k, tel.Metrics[k], v)
		}
	}
	if _, ok := tel.Metrics["firmware_version"]; ok {
		t.Error("the protocol has no firmware request; firmware_version should be absent")
	}
}

func TestNECPollStandby(t *testing.T) {
	f := newFakeNEC(t)
	f.set(func(f *fakeNEC) { f.state = "standby"; f.errBytes[0] = 0x40 })
	tel, _ := f.adapter(nil).Poll(necCtx(t))
	if tel.Status != device.StatusOnline || tel.Metrics["power_status"] != "standby" {
		t.Fatalf("status %v power %v (%s)", tel.Status, tel.Metrics["power_status"], tel.Error)
	}
	if tel.Metrics["operation_status"] != "network_standby" || tel.Metrics["fault_count"] != 0 {
		t.Errorf("standby metrics %v", tel.Metrics)
	}
	for _, k := range []string{"current_input", "picture_mute", "volume"} {
		if _, ok := tel.Metrics[k]; ok {
			t.Errorf("%s should be absent in standby", k)
		}
	}
	if tel.Metrics["light_source_hours"] != 5 || tel.Metrics["model"] != "NP-PA804UL" {
		t.Errorf("hours and identity should still be read in standby: %v", tel.Metrics)
	}
}

func TestNECPollFallsBackTo305_3(t *testing.T) {
	f := newFakeNEC(t)
	f.set(func(f *fakeNEC) { f.no078 = true; f.t1, f.t2 = 0x01, 0x22; f.sndMute = 1 })
	tel, _ := f.adapter(nil).Poll(necCtx(t))
	if tel.Metrics["current_input"] != "displayport" || tel.Metrics["sound_mute"] != true || tel.Metrics["signal_present"] != false {
		t.Errorf("305-3 fallback: %v", tel.Metrics)
	}
}

func TestNECPollOfflineWhenRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening: connection refused
	a := NewNECProjectorAdapter(config.DeviceConfig{ID: "x", Address: addr})
	tel, _ := a.Poll(necCtx(t))
	if tel.Status != device.StatusOffline || tel.Error == "" || a.Status() != device.StatusOffline {
		t.Errorf("status %v error %q", tel.Status, tel.Error)
	}
	if err := a.Connect(necCtx(t)); err == nil {
		t.Error("connect should fail when refused")
	}
}

func TestNECCommands(t *testing.T) {
	f := newFakeNEC(t)
	a := f.adapter(nil)
	for _, c := range []struct {
		req  device.CommandRequest
		want []byte
	}{
		{device.CommandRequest{Name: "input_hdmi2"}, []byte{0x02, 0x03, 0x00, 0x00, 0x02, 0x01, 0xA2, 0xAA}},
		{device.CommandRequest{Name: "input_displayport"}, necFrame(0, necInputCommand(0xA6))},
		{device.CommandRequest{Name: "input_hdbaset"}, necFrame(0, necInputCommand(0xBF))},
		{device.CommandRequest{Name: "input_select", Args: map[string]any{"code": "0x01"}}, necFrame(0, necInputCommand(0x01))},
		{device.CommandRequest{Name: "input_select", Args: map[string]any{"code": "A1h"}}, necFrame(0, necInputCommand(0xA1))},
		{device.CommandRequest{Name: "picture_mute"}, []byte{0x02, 0x10, 0x00, 0x00, 0x00, 0x12}},
		{device.CommandRequest{Name: "picture_unmute"}, []byte{0x02, 0x11, 0x00, 0x00, 0x00, 0x13}},
		{device.CommandRequest{Name: "sound_mute"}, []byte{0x02, 0x12, 0x00, 0x00, 0x00, 0x14}},
		{device.CommandRequest{Name: "sound_unmute"}, []byte{0x02, 0x13, 0x00, 0x00, 0x00, 0x15}},
		{device.CommandRequest{Name: "set_volume", Args: map[string]any{"level": float64(20)}}, necFrame(0, necVolumeCommand(false, 20))},
		{device.CommandRequest{Name: "volume_up"}, necFrame(0, necVolumeCommand(true, 1))},
		{device.CommandRequest{Name: "shutter_close"}, []byte{0x02, 0x16, 0x00, 0x00, 0x00, 0x18}},
		{device.CommandRequest{Name: "shutter_open"}, []byte{0x02, 0x17, 0x00, 0x00, 0x00, 0x19}},
		{device.CommandRequest{Name: "power_off"}, []byte{0x02, 0x01, 0x00, 0x00, 0x00, 0x03}},
		{device.CommandRequest{Name: "power_on"}, []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}},
	} {
		resp, err := a.SendCommand(necCtx(t), c.req)
		if err != nil {
			t.Errorf("%s: %v", c.req.Name, err)
			continue
		}
		if got := f.lastCmd(); !bytes.Equal(got, c.want) {
			t.Errorf("%s sent % X, want % X", c.req.Name, got, c.want)
		}
		if resp.Parsed["success"] != true {
			t.Errorf("%s: success = %v", c.req.Name, resp.Parsed["success"])
		}
	}
	if f.volume != 21 || f.picMute != 0 || f.sndMute != 0 {
		t.Errorf("projector state volume=%d pic=%d snd=%d", f.volume, f.picMute, f.sndMute)
	}
}

func TestNECErrorsAndBadArgs(t *testing.T) {
	f := newFakeNEC(t)
	a := f.adapter(nil)

	// An error reply maps to the §2.4 message.
	_, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "input_select", Args: map[string]any{"code": "99"}})
	if err == nil || !strings.Contains(err.Error(), "That input isn't available on this projector") {
		t.Errorf("invalid input error = %v", err)
	}
	// Out of the projector's own volume range (0–31 from 060-1).
	if _, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "set_volume", Args: map[string]any{"level": 40}}); err == nil || !strings.Contains(err.Error(), "0–31") {
		t.Errorf("volume range error = %v", err)
	}
	for _, req := range []device.CommandRequest{
		{Name: "set_volume"},
		{Name: "set_volume", Args: map[string]any{"level": "loud"}},
		{Name: "input_select"},
		{Name: "input_select", Args: map[string]any{"code": "zz"}},
		{Name: "input_hdmi9"},
	} {
		if _, err := a.SendCommand(necCtx(t), req); err == nil {
			t.Errorf("%s %v: expected an error", req.Name, req.Args)
		}
	}

	// In standby, picture mute is refused with 02h 0Dh.
	f.set(func(f *fakeNEC) { f.state = "standby" })
	if _, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "picture_mute"}); err == nil || !strings.Contains(err.Error(), "standby") {
		t.Errorf("standby refusal = %v", err)
	}
	if e := (&NECError{0x02, 0x0D}).Error(); e != "Projector is in standby, so it can't accept this command" {
		t.Errorf("02h 0Dh = %q", e)
	}
	if e := (&NECError{0x07, 0x07}).Error(); !strings.Contains(e, "07h 07h") {
		t.Errorf("unknown code = %q", e)
	}
}

func TestNECPowerTransitions(t *testing.T) {
	f := newFakeNEC(t)
	a := f.adapter(nil)

	// Power on while cooling: a clear message, no POWER ON sent.
	f.set(func(f *fakeNEC) { f.state = "cooling" })
	_, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "power_on"})
	if err == nil || !strings.Contains(err.Error(), "cooling down") {
		t.Errorf("power_on while cooling = %v", err)
	}
	if got := f.lastCmd(); bytes.Equal(got, necFrame(0, necCmdPowerOn)) {
		t.Error("POWER ON should not be sent while cooling")
	}

	// Power off while already cooling: nothing to do.
	resp, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "power_off"})
	if err != nil || resp.Parsed["already"] != "cooling" {
		t.Errorf("power_off while cooling: %v %v", resp, err)
	}

	// Power off while warming: refused with a clear message.
	f.set(func(f *fakeNEC) { f.state = "warming" })
	if _, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "power_off"}); err == nil || !strings.Contains(err.Error(), "warming up") {
		t.Errorf("power_off while warming = %v", err)
	}

	// After power_on, a projector that stops answering reads as warming,
	// and commands sent meanwhile say so.
	f.set(func(f *fakeNEC) { f.state = "standby" })
	if _, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "power_on"}); err != nil {
		t.Fatalf("power_on: %v", err)
	}
	f.set(func(f *fakeNEC) { f.silent = true })
	tel, _ := a.Poll(necCtx(t))
	if tel.Status != device.StatusOnline || tel.Metrics["power_status"] != "warming" {
		t.Errorf("silent after power_on: status %v power %v (%s)", tel.Status, tel.Metrics["power_status"], tel.Error)
	}
	if _, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "picture_mute"}); err == nil || !strings.Contains(err.Error(), "Projector is warming up") {
		t.Errorf("command while warming = %v", err)
	}
}

func TestNECSilentWithoutTransitionIsOffline(t *testing.T) {
	f := newFakeNEC(t)
	f.set(func(f *fakeNEC) { f.silent = true })
	tel, _ := f.adapter(nil).Poll(necCtx(t))
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "no reply") {
		t.Errorf("status %v error %q", tel.Status, tel.Error)
	}
}

func TestNECDefaultsAndCustomCommand(t *testing.T) {
	if b := NewNECProjectorAdapter(config.DeviceConfig{Address: "10.0.0.5"}); b.address != "10.0.0.5:7142" || b.controlID != 0 {
		t.Errorf("defaults: %s id %d", b.address, b.controlID)
	}
	if got := necFrame(5, necCmdPowerOn); !bytes.Equal(got, []byte{0x02, 0x00, 0x05, 0x00, 0x00, 0x07}) {
		t.Errorf("control ID frame % X", got)
	}
	f := newFakeNEC(t)
	a := NewNECProjectorAdapter(config.DeviceConfig{
		ID: "p", Address: f.ln.Addr().String(),
		Commands: map[string]string{"freeze_on": "01h 98h 00h 00h 01h {mode}"},
	})
	// The fake doesn't know 079 and replies 00h 00h (not recognised).
	_, err := a.SendCommand(necCtx(t), device.CommandRequest{Name: "freeze_on", Args: map[string]any{"mode": "01"}})
	if got := f.lastCmd(); !bytes.Equal(got, []byte{0x01, 0x98, 0x00, 0x00, 0x01, 0x01, 0x9B}) {
		t.Errorf("custom command sent % X", got)
	}
	if err == nil || !strings.Contains(err.Error(), "didn't recognise") {
		t.Errorf("custom command error = %v", err)
	}
	found := false
	for _, c := range a.Capabilities().Commands {
		found = found || c == "freeze_on"
	}
	if !found {
		t.Error("custom command missing from capabilities")
	}
}
