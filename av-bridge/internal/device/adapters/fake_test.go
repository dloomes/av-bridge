package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

func newFakeConfig(id string, tags map[string]string) config.DeviceConfig {
	return config.DeviceConfig{
		ID:       id,
		Name:     "Fake " + id,
		Type:     "display",
		Protocol: "fake",
		PollRate: 30 * time.Second,
		Tags:     tags,
	}
}

func TestFakeAdapter_ConnectPollDisconnect(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-1", nil))

	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if a.Status() != device.StatusOnline {
		t.Fatalf("expected online after Connect, got %v", a.Status())
	}
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if tel.Status != device.StatusOnline {
		t.Errorf("expected online telemetry, got %v", tel.Status)
	}
	if tel.Metrics == nil || tel.Metrics["fake"] != true {
		t.Errorf("expected fake=true marker in metrics, got %v", tel.Metrics)
	}
	// Default metrics_count is 5 → metric_1..metric_5 present.
	for i := 1; i <= 5; i++ {
		key := "metric_" + string(rune('0'+i))
		if _, ok := tel.Metrics[key]; !ok {
			t.Errorf("missing default metric key %s", key)
		}
	}
	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if a.Status() != device.StatusOffline {
		t.Errorf("expected offline after Disconnect, got %v", a.Status())
	}
}

func TestFakeAdapter_PinnedStatus(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-2", map[string]string{
		"fake_status": "degraded",
	}))
	_ = a.Connect(context.Background())
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if tel.Status != device.StatusDegraded {
		t.Errorf("expected degraded, got %v", tel.Status)
	}
}

func TestFakeAdapter_LatencyRespectsContext(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-3", map[string]string{
		"fake_latency_ms": "500",
	}))
	_ = a.Connect(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Poll(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected context deadline error, got nil")
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("Poll ignored context cancel: took %v", elapsed)
	}
}

func TestFakeAdapter_SetStatusCommand(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-4", nil))
	_ = a.Connect(context.Background())
	resp, err := a.SendCommand(context.Background(), device.CommandRequest{
		Name: "set_status",
		Args: map[string]any{"status": "offline"},
	})
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if resp.Parsed["status"] != "offline" {
		t.Errorf("unexpected response: %v", resp.Parsed)
	}
	if a.Status() != device.StatusOffline {
		t.Errorf("status not applied, got %v", a.Status())
	}
}

func TestFakeAdapter_UnknownCommand(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-5", nil))
	_, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "reboot"})
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestFakeAdapter_MetricsCountConfigurable(t *testing.T) {
	a := NewFakeAdapter(newFakeConfig("fake-6", map[string]string{
		"fake_metrics_count": "12",
	}))
	_ = a.Connect(context.Background())
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if _, ok := tel.Metrics["metric_12"]; !ok {
		t.Errorf("expected metric_12 with count=12, keys=%v", tel.Metrics)
	}
}

func TestFakeAdapter_DeterministicSeedPerID(t *testing.T) {
	// Same ID → same first-poll metric values. Different ID → different.
	a := NewFakeAdapter(newFakeConfig("stable-id", nil))
	b := NewFakeAdapter(newFakeConfig("stable-id", nil))
	c := NewFakeAdapter(newFakeConfig("other-id", nil))
	for _, x := range []*FakeAdapter{a, b, c} {
		_ = x.Connect(context.Background())
	}
	ta, _ := a.Poll(context.Background())
	tb, _ := b.Poll(context.Background())
	tc, _ := c.Poll(context.Background())
	if ta.Metrics["metric_1"] != tb.Metrics["metric_1"] {
		t.Errorf("same-ID adapters diverged: %v vs %v", ta.Metrics["metric_1"], tb.Metrics["metric_1"])
	}
	if ta.Metrics["metric_1"] == tc.Metrics["metric_1"] {
		t.Errorf("different-ID adapters produced same seed value %v — collision suggests bad hash", ta.Metrics["metric_1"])
	}
}

func TestFakeAdapter_ClampPct(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-5, 0}, {0, 0}, {50, 50}, {100, 100}, {150, 100},
	} {
		if got := clampPct(tc.in); got != tc.want {
			t.Errorf("clampPct(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
