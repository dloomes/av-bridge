package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/notify"
	"github.com/dloomes/av-bridge/internal/store"
)

// mockDevice satisfies device.Device for testing
type mockDevice struct {
	device.Base
	id     string
	status device.Status
}

func newMockDevice(id string, status device.Status) *mockDevice {
	cfg := config.DeviceConfig{ID: id, Name: id, Type: "display", Location: "Test"}
	return &mockDevice{Base: device.NewBase(cfg), id: id, status: status}
}

func (m *mockDevice) ID() string                  { return m.id }
func (m *mockDevice) Status() device.Status        { return m.status }
func (m *mockDevice) Connect(_ context.Context) error  { return nil }
func (m *mockDevice) Disconnect() error                { return nil }
func (m *mockDevice) Poll(_ context.Context) (*device.Telemetry, error) {
	return m.BaseTelemetry(), nil
}
func (m *mockDevice) SendCommand(_ context.Context, _ device.CommandRequest) (*device.CommandResponse, error) {
	return &device.CommandResponse{}, nil
}

// mockHub satisfies notify.DeviceSource
type mockHub struct {
	devs []device.Device
}

func (h *mockHub) Devices() []device.Device { return h.devs }

// mockCloud captures enqueued events
type mockCloud struct {
	events []*device.Event
}

func (c *mockCloud) EnqueueEvent(e *device.Event) { c.events = append(c.events, e) }

// alertEngine_mock wires a notify.Engine with a mockCloud
// (we need to extend cloud.Client's interface minimally)
// Instead we test the store integration directly

func TestAlertSuppression_RepeatInterval(t *testing.T) {
	path := t.TempDir() + "/state.json"
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}

	// Mark an alert as sent just now
	if err := st.MarkAlertSent("device-01", "device_offline"); err != nil {
		t.Fatal(err)
	}

	last := st.LastAlertSent("device-01", "device_offline")
	if last.IsZero() {
		t.Fatal("expected non-zero last alert time")
	}

	// Should be within the repeat interval (30 min default)
	repeatInterval := 30 * time.Minute
	if time.Since(last) >= repeatInterval {
		t.Errorf("alert should be within repeat interval")
	}
}

func TestAlertEngine_RuleDefaults(t *testing.T) {
	rules := notify.RuleConfig{}
	// Zero values should produce valid engine without panic
	path := t.TempDir() + "/state.json"
	st, _ := store.New(path)
	hub := &mockHub{}

	// Can't call cloud.NewClient without real config, so we verify
	// the engine construction itself is stable with zero rules
	_ = notify.New(rules, st, nil, hub, nil)
}

func TestDeviceState_OfflineTracking(t *testing.T) {
	path := t.TempDir() + "/state.json"
	st, _ := store.New(path)

	// Simulate device going offline
	state := st.GetDevice("device-01")
	state.LastStatus = "online"
	state.LastSeen = time.Now().Add(-10 * time.Minute)
	_ = st.UpsertDevice(state)

	// Re-read
	got := st.GetDevice("device-01")
	if got.LastStatus != "online" {
		t.Errorf("expected online, got %q", got.LastStatus)
	}
	if time.Since(got.LastSeen) < 9*time.Minute {
		t.Errorf("last seen should be ~10 min ago, got %v", time.Since(got.LastSeen))
	}
}

func TestNotify_SeverityConstants(t *testing.T) {
	// Ensure severity values are stable strings used in payloads
	if notify.SeverityInfo != "info" {
		t.Errorf("unexpected SeverityInfo: %q", notify.SeverityInfo)
	}
	if notify.SeverityWarning != "warning" {
		t.Errorf("unexpected SeverityWarning: %q", notify.SeverityWarning)
	}
	if notify.SeverityCritical != "critical" {
		t.Errorf("unexpected SeverityCritical: %q", notify.SeverityCritical)
	}
}
