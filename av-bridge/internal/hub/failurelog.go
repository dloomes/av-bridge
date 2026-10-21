package hub

import (
	"log/slog"
	"time"
)

// failureRepeatEvery is how often a device that keeps failing logs again.
// An unreachable device retries every 30 seconds; logging each attempt
// would be ~2,900 lines a day per device, which on a 5,000-device
// Collector buries everything else (and every line goes to the cloud).
const failureRepeatEvery = 15 * time.Minute

// failureLog logs a repeating failure once, then at most every
// failureRepeatEvery with a count, then once on recovery. One per kind of
// failure per device; used only from that device's goroutine.
type failureLog struct {
	what     string // "connect", "poll"
	failures int
	since    time.Time
	logged   time.Time
	now      func() time.Time
}

func newFailureLog(what string) *failureLog {
	return &failureLog{what: what, now: time.Now}
}

// failed records a failure, and logs it if it's the first or the last
// line was failureRepeatEvery ago.
func (f *failureLog) failed(log *slog.Logger, err error) {
	now := f.now()
	f.failures++
	switch {
	case f.failures == 1:
		f.since, f.logged = now, now
		log.Warn(f.what+" failed", "error", err)
	case now.Sub(f.logged) >= failureRepeatEvery:
		f.logged = now
		log.Warn(f.what+" still failing", "error", err,
			"attempts", f.failures, "since", f.since.UTC().Format(time.RFC3339))
	}
}

// ok records a success; after failures it logs the recovery and resets.
// It reports whether the device had been failing.
func (f *failureLog) ok(log *slog.Logger) bool {
	if f.failures == 0 {
		return false
	}
	log.Info(f.what+" recovered", "failed_attempts", f.failures,
		"down_for", f.now().Sub(f.since).Round(time.Second).String())
	f.failures = 0
	return true
}
