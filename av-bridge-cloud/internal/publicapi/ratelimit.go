package publicapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
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

// limiterKey is the bucket key for the roadmap limiter: the whole
// X-Forwarded-For header plus the RemoteAddr host (port stripped). Behind
// the portal's Next.js rewrite on Amplify, the last XFF hop is Amplify's
// shared egress IP, so keying on it (as clientIP does) would put every
// viewer in one bucket; the earlier entries tell viewers apart. A caller
// that forges XFF only splits its own traffic across buckets, it cannot
// drain anyone else's. Long keys are hashed so a huge header can't inflate
// the limiter's memory.
func limiterKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	key := strings.TrimSpace(r.Header.Get("X-Forwarded-For")) + "|" + host
	if len(key) > 256 {
		sum := sha256.Sum256([]byte(key))
		key = "sha256:" + hex.EncodeToString(sum[:])
	}
	return key
}
