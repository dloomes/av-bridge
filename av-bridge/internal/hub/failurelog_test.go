package hub

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestFailureLogRateLimits(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	f := newFailureLog("poll")
	f.now = func() time.Time { return clock }
	err := errors.New("i/o timeout")

	// A day of failures every 30 seconds.
	for i := 0; i < 2880; i++ {
		f.failed(log, err)
		clock = clock.Add(30 * time.Second)
	}
	lines := strings.Count(out.String(), "\n")
	// First failure, then one every 15 minutes (attempts 31, 61 ... 2851): 1 + 95.
	if lines != 96 {
		t.Errorf("lines for a day of failures = %d, want 96", lines)
	}
	if !strings.Contains(out.String(), "poll still failing") || !strings.Contains(out.String(), "attempts=2851") {
		t.Errorf("repeat lines should say still failing with a count:\n%s", tail(out.String()))
	}

	out.Reset()
	if !f.ok(log) || !strings.Contains(out.String(), "poll recovered") || !strings.Contains(out.String(), "failed_attempts=2880") {
		t.Errorf("recovery = %q", out.String())
	}
	out.Reset()
	if f.ok(log) || out.Len() != 0 {
		t.Errorf("a success with no failures logs nothing, got %q", out.String())
	}
	f.failed(log, err)
	if !strings.Contains(out.String(), "poll failed") {
		t.Errorf("a new failure after recovery is logged at once: %q", out.String())
	}
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}
