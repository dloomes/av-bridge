package publicapi

import (
	"fmt"
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
