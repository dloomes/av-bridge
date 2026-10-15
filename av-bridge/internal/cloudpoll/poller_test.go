package cloudpoll

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/hub"
	"github.com/dloomes/av-bridge/internal/store"
)

// A device's commands run one at a time, in order; different devices run
// side by side, never more than the limit at once.
func TestDispatcherOrderPerDeviceParallelAcross(t *testing.T) {
	var (
		mu            sync.Mutex
		order         = map[string][]string{}
		runningDevice = map[string]int{}
		running, peak int
	)
	d := newDispatcher(4, func(_ context.Context, c bridgeCommand) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		runningDevice[c.ReportedID]++
		if runningDevice[c.ReportedID] > 1 {
			t.Errorf("two commands at once on %s", c.ReportedID)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order[c.ReportedID] = append(order[c.ReportedID], c.ID)
		runningDevice[c.ReportedID]--
		running--
		mu.Unlock()
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		for _, dev := range []string{"a", "b", "c", "d", "e", "f"} {
			d.submit(ctx, bridgeCommand{ID: fmt.Sprintf("%s%d", dev, i), ReportedID: dev})
		}
	}
	d.wait(ctx)
	if d.load() != 0 {
		t.Fatalf("load %d after wait", d.load())
	}
	for dev, ids := range order {
		for i, id := range ids {
			if id != fmt.Sprintf("%s%d", dev, i) {
				t.Errorf("%s ran out of order: %v", dev, ids)
				break
			}
		}
	}
	if peak != 4 {
		t.Errorf("peak concurrency %d, want the limit of 4", peak)
	}
}

// fakeCloud hands out queued commands the way /bridge/poll does (up to
// the requested max) and records results.
type fakeCloud struct {
	mu       sync.Mutex
	queue    []bridgeCommand
	results  map[string]bool
	maxAsked int
	done     chan struct{}
	want     int
	reported atomic.Int64
}

func (f *fakeCloud) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/bridge/poll":
		var req struct {
			Max int `json:"max"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		if req.Max > f.maxAsked {
			f.maxAsked = req.Max
		}
		n := req.Max
		if n > len(f.queue) {
			n = len(f.queue)
		}
		batch := f.queue[:n]
		f.queue = f.queue[n:]
		f.mu.Unlock()
		if n == 0 {
			time.Sleep(20 * time.Millisecond) // a short "hold"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": batch})
	case strings.HasPrefix(r.URL.Path, "/bridge/commands/"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/bridge/commands/"), "/result")
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.results[id] = body.Error == ""
		f.mu.Unlock()
		if f.reported.Add(1) == int64(f.want) {
			close(f.done)
		}
	default:
		http.NotFound(w, r)
	}
}

// 2,000 devices, each command taking 300 ms: one at a time that's ten
// minutes; in parallel it's seconds.
func TestPollerRunsCommandsInParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const devices = 2000
	var cfgs []config.DeviceConfig
	for i := 0; i < devices; i++ {
		cfgs = append(cfgs, config.DeviceConfig{
			ID: fmt.Sprintf("d%04d", i), Protocol: "fake", PollRate: time.Hour,
			Tags: map[string]string{"fake_latency_ms": "300"},
		})
	}
	st, err := store.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	hcfg := &config.Config{}
	hcfg.Hub.HeartbeatPeriod = time.Hour
	h := hub.New(hcfg, cloud.NewClient(config.CloudConfig{}, "c", "s", "test", ""), nil, st)
	h.Reconcile(cfgs)

	fc := &fakeCloud{results: map[string]bool{}, done: make(chan struct{}), want: devices}
	for i := 0; i < devices; i++ {
		fc.queue = append(fc.queue, bridgeCommand{
			ID: fmt.Sprintf("cmd%04d", i), ReportedID: fmt.Sprintf("d%04d", i), Name: "noop",
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(fc.handler))
	defer srv.Close()

	p := NewPoller(config.CloudConfig{
		PortalAPI: srv.URL, HMACSecret: "x",
		CommandPollInterval: 10 * time.Millisecond, CommandLongPollTimeout: 5 * time.Second,
		CommandMaxBatch: 100, CommandConcurrency: 64,
	}, "c", h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	go p.Run(ctx)

	select {
	case <-fc.done:
	case <-time.After(60 * time.Second):
		t.Fatalf("only %d of %d commands finished in 60s", fc.reported.Load(), devices)
	}
	took := time.Since(start)
	fc.mu.Lock()
	defer fc.mu.Unlock()
	failed := 0
	for _, ok := range fc.results {
		if !ok {
			failed++
		}
	}
	t.Logf("%d commands at 300 ms each: %s (one at a time: %s)", devices, took.Round(time.Millisecond),
		(devices * 300 * time.Millisecond).Round(time.Second))
	if failed > 0 {
		t.Errorf("%d commands failed", failed)
	}
	// 2,000 / 64 × 0.3 s ≈ 9.4 s, plus polling overhead.
	if took > 20*time.Second {
		t.Errorf("took %s, want well under 20s", took)
	}
	if fc.maxAsked > 100 || fc.maxAsked < 64 {
		t.Errorf("largest batch asked for: %d", fc.maxAsked)
	}
}
