package publicapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := newIPLimiter(3, time.Minute, 100)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th request allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other IP refused")
	}
	now = now.Add(time.Minute)
	if !l.Allow("a") {
		t.Fatal("new window refused")
	}

	// Old windows are evicted once a window has passed.
	now = now.Add(2 * time.Minute)
	l.Allow("c")
	if n := l.size(); n != 1 {
		t.Fatalf("size after eviction = %d, want 1", n)
	}

	// The map never grows past its cap.
	for i := 0; i < 250; i++ {
		l.Allow(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := l.size(); n > 100 {
		t.Fatalf("size = %d, want <= 100", n)
	}
}

func TestIPLimiterConcurrent(t *testing.T) {
	l := newIPLimiter(30, time.Minute, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("same") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 30 {
		t.Fatalf("allowed = %d, want 30", allowed)
	}
}

func TestLimiterKey(t *testing.T) {
	req := func(xff, remote string) *http.Request {
		r := httptest.NewRequest("GET", "/public/roadmap/x", nil)
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		r.RemoteAddr = remote
		return r
	}
	// Behind Amplify the last hop is a shared egress IP; viewers differ in
	// the earlier entries.
	a := limiterKey(req("198.51.100.1, 203.0.113.9", "10.0.0.5:4000"))
	b := limiterKey(req("198.51.100.2, 203.0.113.9", "10.0.0.5:4000"))
	if a == b {
		t.Fatalf("different first XFF entries share key %q", a)
	}
	// The source port does not split a caller's bucket.
	c := limiterKey(req("", "1.2.3.4:5555"))
	d := limiterKey(req("", "1.2.3.4:6666"))
	if c != d {
		t.Fatalf("ports split the key: %q vs %q", c, d)
	}
	if strings.Contains(c, "5555") {
		t.Fatalf("key keeps the port: %q", c)
	}
	// IPv6 RemoteAddr.
	if e, f := limiterKey(req("", "[2001:db8::1]:1")), limiterKey(req("", "[2001:db8::1]:2")); e != f {
		t.Fatalf("IPv6 ports split the key: %q vs %q", e, f)
	}
}

// The roadmap handler buckets on limiterKey: a malformed token is answered
// before any database work, so a Handler with only a limiter will do.
func TestRoadmapPublicLimiterBuckets(t *testing.T) {
	h := &Handler{roadmapLimiter: newIPLimiter(1, time.Minute, 100)}
	get := func(xff, remote string) int {
		r := httptest.NewRequest("GET", "/public/roadmap/junk", nil)
		r.SetPathValue("token", "junk")
		r.Header.Set("X-Forwarded-For", xff)
		r.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.RoadmapPublic(rec, r)
		return rec.Code
	}
	if c := get("198.51.100.1, 203.0.113.9", "10.0.0.5:4000"); c != 404 {
		t.Fatalf("first viewer = %d", c)
	}
	if c := get("198.51.100.2, 203.0.113.9", "10.0.0.5:4000"); c != 404 {
		t.Fatalf("second viewer behind the same last hop = %d, want its own bucket", c)
	}
	if c := get("198.51.100.1, 203.0.113.9", "10.0.0.5:4001"); c != 429 {
		t.Fatalf("first viewer again from another port = %d, want 429", c)
	}
}
