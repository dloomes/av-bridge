package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

func TestSpoolBounds(t *testing.T) {
	dir := t.TempDir()
	sp, err := newSpool(dir, 25, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := sp.put([]byte("0123456789")); err != nil { // 10 bytes each
			t.Fatal(err)
		}
	}
	es := sp.entries()
	if len(es) != 2 {
		t.Fatalf("size cap: want 2 newest files kept, got %d", len(es))
	}

	// Age cap: a file named as if written two hours ago is dropped.
	old := filepath.Join(dir, "00000000000000000001-000001.json")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, e := range sp.entries() {
		if e.name == filepath.Base(old) {
			t.Fatal("expired file was not dropped")
		}
	}
}

func TestSpoolDropsLeftoverTemps(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "123-000001.json.tmp")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newSpool(dir, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("leftover temp file should be removed on open")
	}
}

// fakeCloud records ingest payloads in arrival order and can be switched
// between up and down.
type fakeCloud struct {
	mu     sync.Mutex
	got    []Payload
	down   atomic.Bool
	status atomic.Int32 // status to return when down; 0 = 503
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		code := int(f.status.Load())
		if code == 0 {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var p Payload
	_ = json.Unmarshal(b, &p)
	f.mu.Lock()
	f.got = append(f.got, p)
	f.mu.Unlock()
}

func (f *fakeCloud) deviceOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, p := range f.got {
		for _, tl := range p.Telemetry {
			ids = append(ids, tl.DeviceID)
		}
	}
	return ids
}

func newTestClient(t *testing.T, url, spoolDir string) *Client {
	t.Helper()
	return NewClient(config.CloudConfig{
		WebhookURL:    url,
		PushInterval:  time.Hour,
		RetryAttempts: 1,
		RetryDelay:    time.Millisecond,
		SpoolDir:      spoolDir,
		SpoolMaxBytes: 1 << 20,
		SpoolMaxAge:   time.Hour,
	}, "c1", "", "test", "")
}

func TestOutageIsSpooledAndReplayedInOrder(t *testing.T) {
	fc := &fakeCloud{}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	spoolDir := t.TempDir()
	ctx := context.Background()

	c := newTestClient(t, srv.URL, spoolDir)
	fc.down.Store(true)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "a"})
	c.flush(ctx)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "b"})
	c.flush(ctx)
	if n := len(c.spool.entries()); n != 2 {
		t.Fatalf("want 2 spooled payloads during outage, got %d", n)
	}

	// Restart mid-outage: a fresh client over the same spool dir.
	c = newTestClient(t, srv.URL, spoolDir)
	fc.down.Store(false)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "c"})
	c.flush(ctx)

	got := fc.deviceOrder()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered %v, want %v (order must be preserved)", got, want)
		}
	}
	if !c.spool.empty() {
		t.Fatal("spool should be empty after replay")
	}
}

func TestRejectedPayloadIsDroppedNotSpooled(t *testing.T) {
	fc := &fakeCloud{}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	c := newTestClient(t, srv.URL, t.TempDir())

	fc.down.Store(true)
	fc.status.Store(http.StatusBadRequest)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "bad"})
	c.flush(context.Background())
	if !c.spool.empty() {
		t.Fatal("a 400 means the payload itself is bad — it must not be spooled forever")
	}

	// Auth failures are retryable (secret may be mid-rotation).
	fc.status.Store(http.StatusUnauthorized)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "x"})
	c.flush(context.Background())
	if c.spool.empty() {
		t.Fatal("a 401 should be spooled for retry")
	}
}

func TestBacklogBlocksNewDataFromOvertaking(t *testing.T) {
	fc := &fakeCloud{}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	c := newTestClient(t, srv.URL, t.TempDir())
	ctx := context.Background()

	fc.down.Store(true)
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "old"})
	c.flush(ctx)
	// Still down: the new batch must queue behind the backlog.
	c.EnqueueTelemetry(&device.Telemetry{DeviceID: "new"})
	c.flush(ctx)
	if n := len(c.spool.entries()); n != 2 {
		t.Fatalf("want both batches spooled, got %d", n)
	}
	fc.down.Store(false)
	c.flush(ctx)
	got := fc.deviceOrder()
	if len(got) != 2 || got[0] != "old" || got[1] != "new" {
		t.Fatalf("delivered %v, want [old new]", got)
	}
}
