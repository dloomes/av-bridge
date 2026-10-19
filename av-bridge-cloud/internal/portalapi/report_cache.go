package portalapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// The heavy reports (Room usage, Device uptime) scan days of telemetry.
// Dashboards and wall screens ask for them every few minutes, often for
// the same organisation and window, so each result is kept for a while
// and shared: one scan serves every screen. Keyed by organisation, scope
// and window, so people with different access never share a result.
const reportCacheTTL = 15 * time.Minute

type reportEntry struct {
	mu  sync.Mutex // one computation per key at a time
	at  time.Time
	val any
}

var reportCache = struct {
	mu      sync.Mutex
	entries map[string]*reportEntry
}{entries: map[string]*reportEntry{}}

func reportKey(r *http.Request, name string, days int) string {
	p, _ := portalauth.From(r.Context())
	return fmt.Sprintf("%s|%s|%d|%v", name, p.CustomerID, days, principalScope(p))
}

// cachedReport returns the report for key, computing it with compute when
// there's no fresh copy. compute reports false when it failed (it has
// written the error response); failures aren't kept.
func cachedReport[T any](key string, compute func() (T, bool)) (T, bool) {
	reportCache.mu.Lock()
	e := reportCache.entries[key]
	if e == nil {
		e = &reportEntry{}
		reportCache.entries[key] = e
	}
	reportCache.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.val.(T); ok && time.Since(e.at) < reportCacheTTL {
		return v, true
	}
	v, ok := compute()
	if !ok {
		return v, false
	}
	e.val, e.at = v, time.Now()
	pruneReportCache()
	return v, true
}

// pruneReportCache drops long-expired entries so the map doesn't grow with
// every organisation and scope ever seen.
func pruneReportCache() {
	reportCache.mu.Lock()
	defer reportCache.mu.Unlock()
	if len(reportCache.entries) < 200 {
		return
	}
	for k, e := range reportCache.entries {
		if e.mu.TryLock() {
			if time.Since(e.at) > 2*reportCacheTTL {
				delete(reportCache.entries, k)
			}
			e.mu.Unlock()
		}
	}
}
