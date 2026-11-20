package adapters

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// A VISCA-over-IP payload carries at most 16 bytes. A longer raw command
// from the device's commands: config (set in the portal) must fail that
// command cleanly, not panic the Collector.
func TestViscaOverlongRawCommandFailsCleanly(t *testing.T) {
	long := "81" + strings.Repeat(" 01", 15) + " FF" // 17 bytes
	if _, err := parseHexBytes(long); err == nil {
		t.Error("parseHexBytes accepted a 17-byte command")
	}
	if _, err := parseHexBytes("81 01 04 00 02 FF"); err != nil {
		t.Errorf("parseHexBytes rejected a normal command: %v", err)
	}

	f := newFakeViscaCamera(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := NewViscaOverIPAdapter(config.DeviceConfig{
		ID: "cam", Address: f.conn.LocalAddr().String(), PollRate: time.Minute,
		Commands: map[string]string{"too_long": long},
	})
	if err := a.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Disconnect() })
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: "too_long"}); err == nil {
		t.Error("SendCommand ran a 17-byte raw command")
	}
	// Any other route to the frame builder is refused too.
	if _, err := a.roundTrip(ctx, viscaPayloadCommand, make([]byte, 17)); err == nil {
		t.Error("roundTrip sent a 17-byte payload")
	}
}
