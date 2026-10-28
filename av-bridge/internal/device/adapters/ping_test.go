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

// A Ping step pings whatever address a device has, in its adapter's form.
func TestHostFromAddress(t *testing.T) {
	for _, c := range []struct {
		addr, host string
		ok         bool
	}{
		{"10.0.0.5", "10.0.0.5", true},
		{"10.0.0.5:23", "10.0.0.5", true},
		{" display.local:20060 ", "display.local", true},
		{"https://10.0.0.9/api/v1", "10.0.0.9", true},
		{"http://user:pw@codec.local:8080", "codec.local", true},
		{"[fe80::1]:80", "fe80::1", true},
		{"fe80::1", "fe80::1", true},
		{"10.0.0.7/24", "10.0.0.7", true},
		{"compute-1", "compute-1", true},
		{"COM3", "", false},
		{"/dev/ttyUSB0", "", false},
		{"", "", false},
	} {
		host, ok := HostFromAddress(c.addr)
		if host != c.host || ok != c.ok {
			t.Errorf("HostFromAddress(%q) = %q, %v; want %q, %v", c.addr, host, ok, c.host, c.ok)
		}
	}
}
