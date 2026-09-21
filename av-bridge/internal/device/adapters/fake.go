package adapters

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// FakeAdapter is a synthetic device used for load testing and capacity
// benchmarking. It does no I/O — every Connect/Poll/Disconnect returns
// synthetic data with optional latency, jitter, and flap simulation.
//
// One adapter instance is one fake device (mirroring every other adapter).
// To simulate a fleet, create N device rows with protocol=fake in the
// cloud/config; the hub spawns one goroutine per row exactly as it would
// for real hardware, so the platform's own scaling behaviour is exercised
// end-to-end (goroutine-per-device, cloud batching, DB ingest, portal
// listing) without needing physical devices.
//
// Configuration via device tags:
//
//	fake_status         string  pinned status: online|offline|unknown (default online)
//	fake_latency_ms     int     synthetic per-poll sleep in ms (default 0)
//	fake_jitter_ms      int     random extra sleep 0..jitter added to latency (default 0)
//	fake_flap_pct       int     0..100, chance of flipping status this poll (default 0)
//	fake_error_pct      int     0..100, chance of returning a poll error (default 0)
//	fake_metrics_count  int     number of synthetic metric fields to emit (default 5)
//	fake_events_per_min int     synthetic events per minute (default 0, no events)
//
// A device with all defaults just returns "online" with 5 changing metric
// values on every poll — cheap and predictable. Raise fake_latency_ms to
// simulate a slow WAN link; raise fake_flap_pct to force status noise into
// the alert engine; raise fake_events_per_min to stress the event channel.
type FakeAdapter struct {
	device.Base

	pinnedStatus device.Status
	latency      time.Duration
	jitter       time.Duration
	flapPct      int
	errorPct     int
	metricsCount int
	eventsPerMin int

	rngMu sync.Mutex
	rng   *rand.Rand

	stopEvents chan struct{}
	stopOnce   sync.Once
}

// NewFakeAdapter constructs a FakeAdapter from cfg. All tuning is via
// cfg.Tags — see the type comment for the recognised keys.
func NewFakeAdapter(cfg config.DeviceConfig) *FakeAdapter {
	a := &FakeAdapter{
		Base:         device.NewBase(cfg),
		pinnedStatus: device.StatusOnline,
		metricsCount: 5,
		// Seed from the device id so runs are deterministic per-device
		// but different across the fleet — repeatable load tests.
		rng: rand.New(rand.NewSource(int64(fnv1a(cfg.ID)))),
	}
	if s := cfg.Tags["fake_status"]; s != "" {
		a.pinnedStatus = device.Status(s)
	}
	if v := parseIntTag(cfg.Tags, "fake_latency_ms"); v > 0 {
		a.latency = time.Duration(v) * time.Millisecond
	}
	if v := parseIntTag(cfg.Tags, "fake_jitter_ms"); v > 0 {
		a.jitter = time.Duration(v) * time.Millisecond
	}
	if v := parseIntTag(cfg.Tags, "fake_flap_pct"); v > 0 {
		a.flapPct = clampPct(v)
	}
	if v := parseIntTag(cfg.Tags, "fake_error_pct"); v > 0 {
		a.errorPct = clampPct(v)
	}
	if v := parseIntTag(cfg.Tags, "fake_metrics_count"); v > 0 {
		a.metricsCount = v
	}
	if v := parseIntTag(cfg.Tags, "fake_events_per_min"); v > 0 {
		a.eventsPerMin = v
	}
	return a
}

func (a *FakeAdapter) Connect(ctx context.Context) error {
	a.SetStatus(a.pinnedStatus)
	if a.eventsPerMin > 0 {
		a.stopEvents = make(chan struct{})
		go a.emitLoop(ctx)
	}
	return nil
}

func (a *FakeAdapter) Disconnect() error {
	a.stopOnce.Do(func() {
		if a.stopEvents != nil {
			close(a.stopEvents)
		}
	})
	a.SetStatus(device.StatusOffline)
	return nil
}

func (a *FakeAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	// Synthetic latency — models a real device's round-trip so a fleet of
	// fakes over a "slow WAN" behaves like the real thing does.
	if d := a.sleepDuration(); d > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}

	if a.errorPct > 0 && a.roll() < a.errorPct {
		a.SetStatus(device.StatusOffline)
		return nil, fmt.Errorf("fake adapter %s: synthetic poll error", a.Cfg.ID)
	}

	status := a.pinnedStatus
	if a.flapPct > 0 && a.roll() < a.flapPct {
		// Flip to the "other" state — offline if online, online if not.
		if status == device.StatusOnline {
			status = device.StatusOffline
		} else {
			status = device.StatusOnline
		}
	}
	a.SetStatus(status)

	t := a.BaseTelemetry()
	t.Status = status
	t.Metrics = a.synthMetrics()
	return t, nil
}

func (a *FakeAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	start := time.Now()
	if d := a.sleepDuration(); d > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}
	switch cmd.Name {
	case "noop":
		return &device.CommandResponse{
			Raw:     "ok",
			Parsed:  map[string]any{"ok": true},
			Latency: time.Since(start),
		}, nil
	case "set_status":
		// Let a load-test driver pin status on the fly to exercise the
		// alert engine. Accepts args["status"] = "online" | "offline".
		if s, ok := cmd.Args["status"].(string); ok && s != "" {
			a.pinnedStatus = device.Status(s)
			a.SetStatus(a.pinnedStatus)
			return &device.CommandResponse{
				Raw:     "status=" + s,
				Parsed:  map[string]any{"status": s},
				Latency: time.Since(start),
			}, nil
		}
		return nil, fmt.Errorf("fake adapter: set_status requires args.status")
	default:
		return nil, fmt.Errorf("fake adapter: unsupported command %q (supported: noop, set_status)", cmd.Name)
	}
}

// emitLoop generates synthetic events at the configured rate. Runs until
// the device is disconnected or the parent context is cancelled.
func (a *FakeAdapter) emitLoop(ctx context.Context) {
	if a.eventsPerMin <= 0 {
		return
	}
	interval := time.Minute / time.Duration(a.eventsPerMin)
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopEvents:
			return
		case <-t.C:
			a.Emit(&device.Event{
				DeviceID:   a.Cfg.ID,
				DeviceName: a.Cfg.Name,
				DeviceType: a.Cfg.Type,
				EventType:  "fake_tick",
				Payload:    map[string]any{"n": a.roll()},
				Timestamp:  time.Now().UTC(),
			})
		}
	}
}

// synthMetrics returns a map of metricsCount changing metric values. Values
// are deterministic per-device (seeded from the device id) but drift each
// poll — good enough to look "live" on the portal and to exercise the
// cloud's numeric ingest path.
func (a *FakeAdapter) synthMetrics() map[string]any {
	m := make(map[string]any, a.metricsCount+2)
	m["fake"] = true
	m["response_ms"] = a.latency.Milliseconds()
	for i := 0; i < a.metricsCount; i++ {
		key := fmt.Sprintf("metric_%d", i+1)
		m[key] = a.roll()
	}
	return m
}

func (a *FakeAdapter) sleepDuration() time.Duration {
	d := a.latency
	if a.jitter > 0 {
		d += time.Duration(a.rngIntn(int(a.jitter)))
	}
	return d
}

func (a *FakeAdapter) roll() int {
	return a.rngIntn(100)
}

func (a *FakeAdapter) rngIntn(n int) int {
	if n <= 0 {
		return 0
	}
	a.rngMu.Lock()
	defer a.rngMu.Unlock()
	return a.rng.Intn(n)
}

// ── Capabilities ─────────────────────────────────────────────────────────────

var fakeCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: false, Off: false},
	Commands: []string{
		"noop",
		"set_status",
	},
	Metrics: []string{
		"fake",
		"response_ms",
		"metric_1", "metric_2", "metric_3", "metric_4", "metric_5",
	},
}

func (a *FakeAdapter) Capabilities() device.Capabilities {
	return fakeCapabilities
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func clampPct(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// fnv1a is a tiny non-crypto hash used only to derive a per-device rng
// seed. Same input → same seed → repeatable load-test runs.
func fnv1a(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
