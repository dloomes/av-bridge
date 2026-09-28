package adapters

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// fakeSICP is a Philips display speaking SICP V2.03 on a local port.
type fakeSICP struct {
	ln net.Listener
	id byte

	mu        sync.Mutex
	power     byte // 0x01 standby, 0x02 on
	source    byte
	volume    byte
	mute      byte
	backlight byte
	oldModel  bool // only the short Volume Set / Step forms
	dropFirst bool // ignore the first frame received, to exercise the retry
	sets      [][]byte
}

func newFakeSICP(t *testing.T) *fakeSICP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSICP{ln: ln, id: 1, power: 0x02, source: 0x0D, volume: 30}
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

func (f *fakeSICP) adapter(tags map[string]string) *PhilipsSICPAdapter {
	return NewPhilipsSICPAdapter(config.DeviceConfig{
		ID: "wall1", Protocol: "philips_sicp", Type: "display",
		Address: f.ln.Addr().String(), Tags: tags,
	})
}

func (f *fakeSICP) serve(c net.Conn) {
	defer c.Close()
	for {
		head := make([]byte, 1)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		rest := make([]byte, int(head[0])-1)
		if _, err := io.ReadFull(c, rest); err != nil {
			return
		}
		frame := append(head, rest...)
		var x byte
		for _, b := range frame[:len(frame)-1] {
			x ^= b
		}
		if x != frame[len(frame)-1] {
			c.Write(sicpFrame(f.id, 0x00, sicpNav)) // checksum error → NAV, per spec
			continue
		}
		if frame[1] != f.id {
			continue // wrong monitor ID: no reply
		}
		f.mu.Lock()
		drop := f.dropFirst
		f.dropFirst = false
		f.mu.Unlock()
		if drop {
			continue
		}
		c.Write(f.reply(frame[3 : len(frame)-1]))
	}
}

func (f *fakeSICP) reply(d []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	ack := sicpFrame(f.id, 0x00, sicpAck)
	nav := sicpFrame(f.id, 0x00, sicpNav)
	report := func(b ...byte) []byte { return sicpFrame(f.id, append([]byte{d[0]}, b...)...) }
	on := f.power == 0x02
	record := func() { f.sets = append(f.sets, append([]byte{}, d...)) }

	switch d[0] {
	case sicpCmdPowerGet:
		return report(f.power)
	case sicpCmdPowerSet:
		record()
		f.power = d[1]
		return ack
	case sicpCmdMiscGet:
		return report(0x04, 0xD2) // 1234 hours
	case sicpCmdModelGet:
		switch d[1] {
		case 0x00:
			return report([]byte("65BDL3052E")...)
		case 0x01:
			return report([]byte("FB02.08")...)
		}
		return nav
	case sicpCmdSerialGet:
		return report([]byte("AU0A1234567890")...)
	}
	if !on {
		return nav // standby: picture and audio commands unavailable
	}
	switch d[0] {
	case sicpCmdSourceGet:
		return report(f.source, 0x00, 0x00, 0x00)
	case sicpCmdSourceSet:
		record()
		f.source = d[1]
		return ack
	case sicpCmdVolumeGet:
		return report(f.volume, f.volume)
	case sicpCmdVolumeSet:
		if f.oldModel && len(d) > 2 {
			return nav
		}
		record()
		f.volume = d[1]
		return ack
	case sicpCmdVolumeStep:
		if f.oldModel && len(d) > 2 {
			return nav
		}
		record()
		return ack
	case sicpCmdMuteGet:
		return report(f.mute)
	case sicpCmdMuteSet:
		record()
		f.mute = d[1]
		return ack
	case sicpCmdTempGet:
		return report(0x1C, 0x1F) // 28 °C, 31 °C
	case sicpCmdVideoPresent:
		return report(0x01)
	case sicpCmdBacklightGet:
		return report(f.backlight)
	case sicpCmdBacklightSet:
		record()
		f.backlight = d[1]
		return ack
	case sicpCmdRestart:
		record()
		return ack
	}
	return sicpFrame(f.id, 0x00, sicpNack)
}

func (f *fakeSICP) lastSet() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sets) == 0 {
		return nil
	}
	return f.sets[len(f.sets)-1]
}

func sicpCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSICPFrameMatchesSpecExamples(t *testing.T) {
	for _, c := range []struct {
		data []byte
		want []byte
	}{
		{[]byte{0x19}, []byte{0x05, 0x01, 0x00, 0x19, 0x1D}},                                                 // get power
		{[]byte{0x18, 0x01}, []byte{0x06, 0x01, 0x00, 0x18, 0x01, 0x1E}},                                     // power off
		{[]byte{0xAC, 0x0D, 0x09, 0x01, 0x00}, []byte{0x09, 0x01, 0x00, 0xAC, 0x0D, 0x09, 0x01, 0x00, 0xA1}}, // HDMI 1
		{[]byte{0x44, 0x16, 0x32}, []byte{0x07, 0x01, 0x00, 0x44, 0x16, 0x32, 0x66}},                         // volume 22 / 50
		{[]byte{0x0F, 0x02}, []byte{0x06, 0x01, 0x00, 0x0F, 0x02, 0x0A}},                                     // operating hours
	} {
		if got := sicpFrame(1, c.data...); !bytes.Equal(got, c.want) {
			t.Errorf("frame(% X) = % X, want % X", c.data, got, c.want)
		}
	}
}

func TestPhilipsPollOn(t *testing.T) {
	f := newFakeSICP(t)
	a := f.adapter(nil)
	if err := a.Connect(sicpCtx(t)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tel, _ := a.Poll(sicpCtx(t))
	if tel.Status != device.StatusOnline {
		t.Fatalf("status %v: %s", tel.Status, tel.Error)
	}
	want := map[string]any{
		"power_status": "on", "current_input": "hdmi1", "volume": 30, "volume_audio_out": 30,
		"mute": false, "temperature_c": 28, "temperature_2_c": 31, "signal_present": true,
		"backlight": "on", "operating_hours": 1234,
		"model": "65BDL3052E", "firmware_version": "FB02.08", "serial_number": "AU0A1234567890",
	}
	for k, v := range want {
		if tel.Metrics[k] != v {
			t.Errorf("%s = %#v, want %#v", k, tel.Metrics[k], v)
		}
	}
}

func TestPhilipsPollStandby(t *testing.T) {
	f := newFakeSICP(t)
	f.power = 0x01
	tel, _ := f.adapter(nil).Poll(sicpCtx(t))
	if tel.Status != device.StatusOnline || tel.Metrics["power_status"] != "standby" {
		t.Fatalf("status %v power %v (%s)", tel.Status, tel.Metrics["power_status"], tel.Error)
	}
	if tel.Metrics["operating_hours"] != 1234 || tel.Metrics["model"] != "65BDL3052E" {
		t.Errorf("identity and hours should still be read in standby: %v", tel.Metrics)
	}
	for _, k := range []string{"current_input", "volume", "mute", "signal_present"} {
		if _, ok := tel.Metrics[k]; ok {
			t.Errorf("%s should be absent in standby", k)
		}
	}
}

func TestPhilipsPollUnreachable(t *testing.T) {
	a := NewPhilipsSICPAdapter(config.DeviceConfig{ID: "x", Address: "127.0.0.1:1"})
	tel, _ := a.Poll(sicpCtx(t))
	if tel.Status != device.StatusOffline || tel.Error == "" {
		t.Errorf("status %v error %q", tel.Status, tel.Error)
	}
}

func TestPhilipsCommands(t *testing.T) {
	cases := []struct {
		req  device.CommandRequest
		want []byte
	}{
		{device.CommandRequest{Name: "power_off"}, []byte{0x18, 0x01}},
		{device.CommandRequest{Name: "power_on"}, []byte{0x18, 0x02}},
		{device.CommandRequest{Name: "input_hdmi2"}, []byte{0xAC, 0x06, 0x09, 0x01, 0x00}},
		{device.CommandRequest{Name: "input_displayport"}, []byte{0xAC, 0x0A, 0x09, 0x01, 0x00}},
		{device.CommandRequest{Name: "set_volume", Args: map[string]any{"level": float64(45)}}, []byte{0x44, 45, 45}},
		{device.CommandRequest{Name: "volume_up"}, []byte{0x41, 0x01, 0x02}},
		{device.CommandRequest{Name: "mute"}, []byte{0x47, 0x01}},
		{device.CommandRequest{Name: "backlight_off"}, []byte{0x72, 0x01}},
		{device.CommandRequest{Name: "reboot"}, []byte{0x57, 0x00}},
	}
	f := newFakeSICP(t)
	a := f.adapter(nil)
	for _, c := range cases {
		resp, err := a.SendCommand(sicpCtx(t), c.req)
		if err != nil {
			t.Errorf("%s: %v", c.req.Name, err)
			continue
		}
		if got := f.lastSet(); !bytes.Equal(got, c.want) {
			t.Errorf("%s sent % X, want % X", c.req.Name, got, c.want)
		}
		if resp.Parsed["success"] != true {
			t.Errorf("%s: success = %v", c.req.Name, resp.Parsed["success"])
		}
	}
	if f.volume != 45 || f.mute != 1 || f.backlight != 1 {
		t.Errorf("display state volume=%d mute=%d backlight=%d", f.volume, f.mute, f.backlight)
	}
}

func TestPhilipsOldModelFallsBackToShortForm(t *testing.T) {
	f := newFakeSICP(t)
	f.oldModel = true
	a := f.adapter(nil)
	if _, err := a.SendCommand(sicpCtx(t), device.CommandRequest{Name: "set_volume", Args: map[string]any{"level": "20"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSet(); !bytes.Equal(got, []byte{0x44, 20}) {
		t.Errorf("sent % X, want short form 44 14", got)
	}
	if _, err := a.SendCommand(sicpCtx(t), device.CommandRequest{Name: "volume_down"}); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSet(); !bytes.Equal(got, []byte{0x41, 0x00}) {
		t.Errorf("sent % X, want 41 00", got)
	}
}

func TestPhilipsNAVAndBadArgs(t *testing.T) {
	f := newFakeSICP(t)
	f.power = 0x01
	a := f.adapter(nil)
	if _, err := a.SendCommand(sicpCtx(t), device.CommandRequest{Name: "mute"}); err == nil {
		t.Error("mute in standby should surface the NAV")
	}
	for _, req := range []device.CommandRequest{
		{Name: "set_volume"},
		{Name: "set_volume", Args: map[string]any{"level": 101}},
		{Name: "set_volume", Args: map[string]any{"level": "loud"}},
		{Name: "input_hdmi9"},
	} {
		if _, err := a.SendCommand(sicpCtx(t), req); err == nil {
			t.Errorf("%s %v: expected an error", req.Name, req.Args)
		}
	}
}

func TestPhilipsRetriesAfterNoReply(t *testing.T) {
	f := newFakeSICP(t)
	f.dropFirst = true
	tel, _ := f.adapter(nil).Poll(sicpCtx(t))
	if tel.Status != device.StatusOnline || tel.Metrics["power_status"] != "on" {
		t.Errorf("status %v power %v (%s)", tel.Status, tel.Metrics["power_status"], tel.Error)
	}
}

func TestPhilipsMonitorIDAndCustomCommand(t *testing.T) {
	f := newFakeSICP(t)
	f.id = 7
	a := NewPhilipsSICPAdapter(config.DeviceConfig{
		ID: "wall7", Address: f.ln.Addr().String(),
		Tags:     map[string]string{"monitor_id": "7"},
		Commands: map[string]string{"input_browser": "AC 10 {url} 01 00"},
	})
	if a.address != f.ln.Addr().String() || a.monitorID != 7 {
		t.Fatalf("address %s id %d", a.address, a.monitorID)
	}
	if _, err := a.SendCommand(sicpCtx(t), device.CommandRequest{Name: "input_browser", Args: map[string]any{"url": "02"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSet(); !bytes.Equal(got, []byte{0xAC, 0x10, 0x02, 0x01, 0x00}) {
		t.Errorf("sent % X", got)
	}
	if b := NewPhilipsSICPAdapter(config.DeviceConfig{Address: "10.0.0.5"}); b.address != "10.0.0.5:5000" || b.monitorID != 1 {
		t.Errorf("defaults: %s id %d", b.address, b.monitorID)
	}
}
