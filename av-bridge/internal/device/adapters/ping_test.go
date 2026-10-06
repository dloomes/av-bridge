package adapters

import (
	"runtime"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

// Windows only supports privileged ICMP, so an untagged ping device must
// default to it there — otherwise every ping fails and the device shows
// offline forever. The tag overrides the default either way.
func TestPingPrivilegedDefault(t *testing.T) {
	a := NewPingAdapter(config.DeviceConfig{ID: "p", Address: "127.0.0.1"})
	if want := runtime.GOOS == "windows"; a.privileged != want {
		t.Errorf("default privileged = %v on %s, want %v", a.privileged, runtime.GOOS, want)
	}
	on := NewPingAdapter(config.DeviceConfig{ID: "p", Tags: map[string]string{"ping_privileged": "true"}})
	off := NewPingAdapter(config.DeviceConfig{ID: "p", Tags: map[string]string{"ping_privileged": "false"}})
	if !on.privileged || off.privileged {
		t.Errorf("tag override: true→%v false→%v", on.privileged, off.privileged)
	}
}

// A missing ping_interval_ms tag must keep the default interval: a zero
// interval makes pro-bing panic in a goroutine and crash the collector.
func TestPingIntervalDefault(t *testing.T) {
	a := NewPingAdapter(config.DeviceConfig{ID: "p"})
	if a.interval <= 0 {
		t.Fatalf("default interval = %v, must be positive", a.interval)
	}
	b := NewPingAdapter(config.DeviceConfig{ID: "p", Tags: map[string]string{"ping_interval_ms": "250"}})
	if b.interval.Milliseconds() != 250 {
		t.Fatalf("tagged interval = %v, want 250ms", b.interval)
	}
}
