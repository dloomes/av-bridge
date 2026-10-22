package hub

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/store"
)

func newTestHub(t *testing.T, devices ...config.DeviceConfig) *Hub {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Devices: devices}
	cfg.Hub.HeartbeatPeriod = time.Hour
	h := New(cfg, cloud.NewClient(config.CloudConfig{}, "c", "s", "test", ""), nil, st)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.runCtx = ctx
	for _, d := range devices {
		h.spawnDevice(d)
	}
	return h
}

// A device polled hourly still gives a fresh reading on demand, at once,
// and the reading is handled like any other poll.
func TestPollNowGivesFreshReading(t *testing.T) {
	h := newTestHub(t, config.DeviceConfig{
		ID: "d1", Name: "Display", Protocol: "fake", PollRate: time.Hour,
		Tags: map[string]string{"fake_metrics_count": "2"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tel, err := h.PollNow(ctx, "d1")
	if err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if tel.Status != device.StatusOnline || len(tel.Metrics) == 0 {
		t.Errorf("reading: status %s, metrics %v", tel.Status, tel.Metrics)
	}
	if time.Since(tel.Timestamp) > 5*time.Second {
		t.Errorf("reading is %s old, want fresh", time.Since(tel.Timestamp))
	}
	if st := h.store.GetDevice("d1"); st.LastStatus != string(device.StatusOnline) {
		t.Errorf("on-demand reading didn't go through the normal poll path (stored status %q)", st.LastStatus)
	}

	if _, err := h.PollNow(ctx, "nope"); err == nil {
		t.Error("unknown device should be an error")
	}
}

// Requests from many checks at once are served one after another by the
// device's goroutine, never two polls at the same time.
func TestPollNowConcurrentRequests(t *testing.T) {
	h := newTestHub(t, config.DeviceConfig{ID: "d1", Protocol: "fake", PollRate: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.PollNow(ctx, "d1"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// While a device is between connection attempts, an on-demand poll is
// answered straight away as offline rather than waiting out the backoff.
func TestWaitBackoffAnswersPollsOffline(t *testing.T) {
	polls := make(chan pollRequest)
	done := make(chan bool, 1)
	go func() {
		done <- waitBackoff(context.Background(), 300*time.Millisecond, polls, func() *device.Telemetry {
			return &device.Telemetry{Status: device.StatusOffline, Error: "connect refused"}
		})
	}()
	req := pollRequest{reply: make(chan *device.Telemetry, 1)}
	start := time.Now()
	polls <- req
	tel := <-req.reply
	if tel.Status != device.StatusOffline || time.Since(start) > 100*time.Millisecond {
		t.Errorf("got %s after %s, want offline at once", tel.Status, time.Since(start))
	}
	if ok := <-done; !ok {
		t.Error("backoff should complete normally")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitBackoff(ctx, time.Hour, polls, nil) {
		t.Error("cancelled context should end the wait")
	}
}

// A tag edit restarts the adapter (tags are settings: outlet names,
// outlet_count, tls_skip_verify...); a name or location edit doesn't.
func TestAdapterRelevantChangeIncludesTags(t *testing.T) {
	base := config.DeviceConfig{ID: "pdu", Name: "PDU", Protocol: "aten_pdu", Address: "10.0.0.5",
		Tags: map[string]string{"outlet_count": "8"}}
	renamed := base
	renamed.Name, renamed.Location = "Rack PDU", "Comms room"
	if adapterRelevantChange(base, renamed) {
		t.Error("a rename shouldn't restart the adapter")
	}
	tagged := base
	tagged.Tags = map[string]string{"outlet_count": "8", "outlet_1_name": "Codec"}
	if !adapterRelevantChange(base, tagged) {
		t.Error("a tag change must restart the adapter so it uses the new tags")
	}
}

// An adapter that writes discovered values into its own Tags (Sony,
// Aurora) mustn't change the hub's copy, or every config pull would look
// like a tag change.
func TestAdapterConfigIsACopy(t *testing.T) {
	orig := config.DeviceConfig{ID: "tv", Tags: map[string]string{"a": "1"},
		Commands: map[string]string{"x": "y"}, Subscriptions: []config.SubscriptionSpec{{Tag: "t"}}}
	c := adapterConfig(orig)
	c.Tags["model"] = "FW-85BZ40L"
	c.Commands["z"] = "w"
	c.Subscriptions[0].Tag = "changed"
	if len(orig.Tags) != 1 || len(orig.Commands) != 1 || orig.Subscriptions[0].Tag != "t" {
		t.Errorf("hub's config was changed through the adapter's copy: %+v", orig)
	}
}
