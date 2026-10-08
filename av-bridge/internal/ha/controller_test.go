package ha

import (
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
)

type fakeHub struct {
	mu    sync.Mutex
	calls int
	last  []config.DeviceConfig
}

func (f *fakeHub) Reconcile(want []config.DeviceConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = want
}

func newTest() (*Controller, *fakeHub, *int, *time.Time) {
	hub := &fakeHub{}
	pulls := 0
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := New(hub, func() { pulls++ })
	c.now = func() time.Time { return clock }
	return c, hub, &pulls, &clock
}

func TestStandalone(t *testing.T) {
	c, hub, pulls, clock := newTest()
	c.Observe("", 0)
	*clock = clock.Add(time.Hour)
	c.Check()
	if hub.calls != 0 || *pulls != 0 || !c.Serving() {
		t.Fatalf("a collector outside a group must never be fenced or stopped (calls=%d pulls=%d)", hub.calls, *pulls)
	}
}

func TestStandbyDropsDevices(t *testing.T) {
	c, hub, _, _ := newTest()
	c.Observe("standby", 60*time.Second)
	if hub.calls != 1 || hub.last != nil {
		t.Fatalf("standby must drop all devices (calls=%d)", hub.calls)
	}
	if c.Serving() {
		t.Fatal("standby must not apply device lists")
	}
	c.Observe("standby", 60*time.Second)
	if hub.calls != 1 {
		t.Fatal("repeated standby polls shouldn't re-reconcile")
	}
}

func TestActiveFencesBeforeLeaseCanExpire(t *testing.T) {
	c, hub, pulls, clock := newTest()
	c.Observe("active", 60*time.Second)
	if *pulls != 1 || !c.Serving() {
		t.Fatalf("becoming active should pull config (pulls=%d)", *pulls)
	}
	// Renewed at 30s: still fine at 65s.
	*clock = clock.Add(30 * time.Second)
	c.Observe("active", 60*time.Second)
	*clock = clock.Add(35 * time.Second)
	c.Check()
	if hub.calls != 0 {
		t.Fatal("renewed lease must not fence")
	}
	// No poll for TTL - margin (40s) after the last one: fence.
	*clock = clock.Add(6 * time.Second)
	c.Check()
	if hub.calls != 1 || hub.last != nil || c.Serving() {
		t.Fatalf("active machine must stop polling 40s after its last poll (calls=%d)", hub.calls)
	}
	// Contact restored and still active: devices come back via a pull.
	c.Observe("active", 60*time.Second)
	if *pulls != 2 || !c.Serving() {
		t.Fatalf("re-confirmed active should pull again (pulls=%d)", *pulls)
	}
}

func TestLeavingGroupResumes(t *testing.T) {
	c, _, pulls, _ := newTest()
	c.Observe("standby", 60*time.Second)
	c.Observe("", 0)
	if !c.Serving() || *pulls != 1 {
		t.Fatalf("a standby whose group was dissolved should resume (pulls=%d)", *pulls)
	}
}
