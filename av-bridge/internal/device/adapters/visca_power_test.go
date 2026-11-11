package adapters

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// fakeViscaCamera answers VISCA-over-IP on a local UDP port. Power commands
// are rejected "not executable" until rejectPower runs out, or always when
// rejectWhenSame and the camera is already in the requested state.
type fakeViscaCamera struct {
	conn *net.UDPConn

	mu             sync.Mutex
	on             bool
	rejectPower    int
	rejectWhenSame bool
	powerCmds      int
}

func newFakeViscaCamera(t *testing.T, on bool) *fakeViscaCamera {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeViscaCamera{conn: conn, on: on}
	t.Cleanup(func() { conn.Close() })
	go f.serve()
	return f
}

func (f *fakeViscaCamera) serve() {
	buf := make([]byte, 1024)
	for {
		n, from, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, seq, payload, err := decodeViscaIP(buf[:n])
		if err != nil {
			continue
		}
		reply := []byte{0x90, 0x50, 0xFF}
		f.mu.Lock()
		switch {
		case bytes.Equal(payload, viscaPower(true)), bytes.Equal(payload, viscaPower(false)):
			f.powerCmds++
			want := payload[4] == 0x02
			if f.rejectPower > 0 || (f.rejectWhenSame && f.on == want) {
				if f.rejectPower > 0 {
					f.rejectPower--
				}
				reply = []byte{0x90, 0x60, 0x41, 0xFF}
			} else {
				f.on = want
			}
		case bytes.Equal(payload, viscaInqPower()):
			v := byte(0x03)
			if f.on {
				v = 0x02
			}
			reply = []byte{0x90, 0x50, v, 0xFF}
		}
		f.mu.Unlock()
		_, _ = f.conn.WriteToUDP(encodeViscaIP(viscaPayloadReply, seq, reply), from)
	}
}

func (f *fakeViscaCamera) state() (on bool, cmds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on, f.powerCmds
}

func connectFakeVisca(t *testing.T, f *fakeViscaCamera) *ViscaOverIPAdapter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := NewViscaOverIPAdapter(config.DeviceConfig{ID: "cam", Address: f.conn.LocalAddr().String(), PollRate: time.Minute})
	if err := a.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Disconnect() })
	return a
}

func shortViscaPowerPacing(t *testing.T) {
	b, at, r := viscaPowerBudget, viscaPowerAttempt, viscaPowerRetry
	viscaPowerBudget, viscaPowerAttempt, viscaPowerRetry = 2*time.Second, 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { viscaPowerBudget, viscaPowerAttempt, viscaPowerRetry = b, at, r })
}

func TestViscaPowerRetriesWhileCameraIsBusy(t *testing.T) {
	shortViscaPowerPacing(t)
	f := newFakeViscaCamera(t, true)
	f.rejectPower = 3 // still warming up for three attempts
	a := connectFakeVisca(t, f)

	if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "power_off"}); err != nil {
		t.Fatalf("power_off: %v", err)
	}
	if on, cmds := f.state(); on || cmds != 4 {
		t.Fatalf("camera on=%v after %d commands, want off after 4", on, cmds)
	}
}

func TestViscaPowerAlreadyInStateSucceeds(t *testing.T) {
	shortViscaPowerPacing(t)
	f := newFakeViscaCamera(t, true)
	f.rejectWhenSame = true
	a := connectFakeVisca(t, f)

	resp, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "power_on"})
	if err != nil {
		t.Fatalf("power_on on a camera that's already on: %v", err)
	}
	if !strings.Contains(resp.Raw, "confirmed") {
		t.Errorf("Raw = %q, want the inquiry confirmation", resp.Raw)
	}
}

func TestViscaPowerGivesUpWithTheCameraError(t *testing.T) {
	shortViscaPowerPacing(t)
	f := newFakeViscaCamera(t, false)
	f.rejectPower = 1000
	a := connectFakeVisca(t, f)

	_, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "power_on"})
	if err == nil || !strings.Contains(err.Error(), "not executable") || !strings.Contains(err.Error(), "didn't switch on") {
		t.Fatalf("err = %v, want a give-up error carrying the camera's reason", err)
	}
}
