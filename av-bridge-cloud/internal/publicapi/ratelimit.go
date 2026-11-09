package publicapi

import (
	"sync"
	"time"
)

// ipLimiter is a per-IP fixed-window counter: at most limit requests per
// window from one IP. It is safe for concurrent use. Memory stays bounded:
// windows older than one window length are swept at most once per window,
// and if the map still reaches maxIPs it is cleared (failing open rather
// than refusing everyone). Per process, so with N cloud tasks an IP can get
// up to N x limit; that is fine for a soft abuse guard.
type ipLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	maxIPs    int
	windows   map[string]*ipWindow
	lastSweep time.Time
	now       func() time.Time
}

type ipWindow struct {
	start time.Time
	count int
}

func newIPLimiter(limit int, window time.Duration, maxIPs int) *ipLimiter {
	return &ipLimiter{
		limit:   limit,
		window:  window,
		maxIPs:  maxIPs,
		windows: map[string]*ipWindow{},
		now:     time.Now,
	}
}

// Allow counts one request from ip and reports whether it is within limit.
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.window {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}
		l.lastSweep = now
	}
	w, ok := l.windows[ip]
	if !ok || now.Sub(w.start) >= l.window {
		if !ok && len(l.windows) >= l.maxIPs {
			clear(l.windows)
		}
		l.windows[ip] = &ipWindow{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

func (l *ipLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.windows)
}
