package adapters

import (
	"testing"

	"github.com/dloomes/av-bridge/internal/device"
)

// A decoder with no stream selected sits in s_idle while answering every
// request; it must show online, not offline.
func TestMapVPXStatus(t *testing.T) {
	cases := map[string]device.Status{
		"s_srv_on":    device.StatusOnline,
		"s_idle":      device.StatusOnline,
		"s_stop":      device.StatusOnline,
		"s_search":    device.StatusOnline,
		"s_attaching": device.StatusOnline,
		"s_something": device.StatusOnline,
		"s_error":     device.StatusOffline,
		"":            device.StatusOffline,
	}
	for state, want := range cases {
		if got := mapVPXStatus(state); got != want {
			t.Errorf("mapVPXStatus(%q) = %v, want %v", state, got, want)
		}
	}
}
