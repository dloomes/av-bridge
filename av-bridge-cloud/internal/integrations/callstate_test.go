package integrations

import "testing"

// Values observed from a real Lens tenant, plus the unknown-safe default.
func TestCallStateObservedValues(t *testing.T) {
	for in, want := range map[string]string{
		"IN_CALL": "in_call", "NOT_IN_CALL": "idle", "UNKNOWN": "unknown",
		"UNSUPPORTED": "unknown", "": "unknown", "SomethingNew": "unknown",
	} {
		if got := callState(in); got != want {
			t.Errorf("callState(%q) = %q, want %q", in, got, want)
		}
	}
}
