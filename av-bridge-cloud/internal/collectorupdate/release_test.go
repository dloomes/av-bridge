package collectorupdate

import (
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"v1.0.20", "v1.0.19", true},
		{"v1.0.20", "v1.0.20", false},
		{"v1.0.19", "v1.0.20", false}, // never downgrade
		{"v1.1.0", "v1.0.99", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.0.10", "v1.0.9", true}, // numeric, not lexical
		{"v1.0.20", "dev", true},
		{"v1.0.20", "", true},
		{"dev", "v1.0.0", false},
		{"v1.0.20", "v1.0.20-3-gabc123", false},
	}
	for _, c := range cases {
		if got := Newer(c.cand, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

func TestInWindow(t *testing.T) {
	// 02:00 Europe/London window in January (UTC+0) and July (UTC+1).
	jan := time.Date(2026, 1, 10, 2, 30, 0, 0, time.UTC)
	if !InWindow(jan, "Europe/London", 120) {
		t.Error("02:30 GMT should be inside a 02:00 window")
	}
	julyUTC := time.Date(2026, 7, 10, 1, 30, 0, 0, time.UTC) // 02:30 BST
	if !InWindow(julyUTC, "Europe/London", 120) {
		t.Error("02:30 BST should be inside a 02:00 window")
	}
	if InWindow(time.Date(2026, 1, 10, 4, 0, 0, 0, time.UTC), "Europe/London", 120) {
		t.Error("04:00 is past the 2-hour window")
	}
	// Window crossing midnight: 23:00 → 01:00.
	if !InWindow(time.Date(2026, 1, 11, 0, 30, 0, 0, time.UTC), "Europe/London", 23*60) {
		t.Error("00:30 should be inside a 23:00 window")
	}
	// Building time zone respected.
	ny := time.Date(2026, 1, 10, 7, 30, 0, 0, time.UTC) // 02:30 New York
	if !InWindow(ny, "America/New_York", 120) || InWindow(ny, "Europe/London", 120) {
		t.Error("window must follow the building's time zone")
	}
}

func TestEligibility(t *testing.T) {
	yes, no := true, false
	signed := &Release{Version: "v1.0.20", Files: map[string]File{
		"av-bridge-linux-amd64": {SHA256: "aa", Signature: "sig"},
	}}
	base := Collector{Version: "v1.0.19", Platform: "linux/amd64", Capable: &yes}
	if ok, why := signed.Eligibility(base); !ok {
		t.Fatalf("should be eligible: %s", why)
	}
	cases := map[string]struct {
		r *Release
		c Collector
	}{
		"no release":       {nil, base},
		"unsigned":         {&Release{Version: "v1.0.20", Files: map[string]File{"av-bridge-linux-amd64": {SHA256: "aa"}}}, base},
		"old bridge":       {signed, Collector{Version: "v1.0.10"}},
		"blocked":          {signed, Collector{Version: "v1.0.19", Platform: "linux/amd64", Capable: &no, Blocker: "runs in a container"}},
		"no build":         {signed, Collector{Version: "v1.0.19", Platform: "linux/arm64", Capable: &yes}},
		"up to date":       {signed, Collector{Version: "v1.0.20", Platform: "linux/amd64", Capable: &yes}},
		"unknown platform": {signed, Collector{Version: "v1.0.19", Platform: "plan9/386", Capable: &yes}},
	}
	for name, c := range cases {
		if ok, why := c.r.Eligibility(c.c); ok || why == "" {
			t.Errorf("%s: want ineligible with a reason, got ok=%v %q", name, ok, why)
		}
	}
}

func TestParseWindow(t *testing.T) {
	if m, err := ParseWindow("02:30"); err != nil || m != 150 || FormatWindow(m) != "02:30" {
		t.Errorf("ParseWindow(02:30) = %d %v", m, err)
	}
	if _, err := ParseWindow("25:00"); err == nil {
		t.Error("25:00 should be rejected")
	}
}
