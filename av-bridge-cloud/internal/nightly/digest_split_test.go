package nightly

import (
	"strings"
	"testing"
	"time"
)

// The digest goes out once the morning's work is over: after the later of
// the power-on and the readiness checks, whichever are switched on.
func TestDigestSendAfter(t *testing.T) {
	at := func(h, m int) time.Duration { return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute }
	cases := []struct {
		name string
		c    customerInfo
		want time.Duration
	}{
		{"power only", customerInfo{enabled: true, powerOnTime: at(7, 30), readinessTime: at(8, 0)}, at(7, 30)},
		{"readiness only", customerInfo{powerOnTime: at(7, 30), readinessEnabled: true, readinessTime: at(8, 0)}, at(8, 0)},
		{"both, readiness later", customerInfo{enabled: true, powerOnTime: at(7, 30), readinessEnabled: true, readinessTime: at(7, 45)}, at(7, 45)},
		{"both, power later", customerInfo{enabled: true, powerOnTime: at(8, 30), readinessEnabled: true, readinessTime: at(7, 45)}, at(8, 30)},
	}
	for _, tc := range cases {
		if got := tc.c.sendAfter(); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	s := &DigestSender{cfg: DigestConfig{SendAfterOffset: 30 * time.Minute}}
	c := customerInfo{timezone: "UTC", enabled: true, powerOnTime: at(7, 30), readinessEnabled: true, readinessTime: at(8, 0)}
	if s.isDue(c, time.Date(2030, 1, 7, 8, 15, 0, 0, time.UTC)) {
		t.Error("due before the readiness checks were over")
	}
	if !s.isDue(c, time.Date(2030, 1, 7, 8, 31, 0, 0, time.UTC)) {
		t.Error("not due after the readiness checks + 30 minutes")
	}
}

// Each kind of run gets its own section, power first; a kind with no runs
// gets none.
func TestDigestSections(t *testing.T) {
	runs := []runRow{
		{kind: "readiness", roomName: "Boardroom", status: "failed", phase: "failed", failureReason: "codec offline"},
		{kind: "power", roomName: "Boardroom", status: "succeeded"},
		{kind: "power", roomName: "Huddle", status: "succeeded"},
	}
	secs := sectionsOf(runs)
	if len(secs) != 2 || secs[0].title != "Power schedule" || secs[1].title != "Readiness checks" {
		t.Fatalf("sections: %+v", secs)
	}
	if secs[0].sum.ready != 2 || secs[1].sum.failed != 1 {
		t.Errorf("counts: power %+v readiness %+v", secs[0].sum, secs[1].sum)
	}
	if got := sectionsOf(runs[1:]); len(got) != 1 || got[0].title != "Power schedule" {
		t.Errorf("power-only: %+v", got)
	}

	c := customerInfo{customerName: "Acme", timezone: "UTC"}
	now := time.Date(2030, 1, 7, 9, 0, 0, 0, time.UTC)
	text := renderText(c, runs, nil, summariseRuns(runs), now)
	page := renderHTML(c, runs, nil, summariseRuns(runs), now)
	for _, want := range []string{"POWER SCHEDULE", "READINESS CHECKS", "codec offline", "2 rooms switched off overnight and back on."} {
		if !strings.Contains(text, want) {
			t.Errorf("text digest missing %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"Power schedule", "Readiness checks", "codec offline"} {
		if !strings.Contains(page, want) {
			t.Errorf("HTML digest missing %q", want)
		}
	}
}
