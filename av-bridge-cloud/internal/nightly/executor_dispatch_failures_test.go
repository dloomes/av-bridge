package nightly

import "testing"

func TestDispatchFailures(t *testing.T) {
	got := dispatchFailures([]map[string]any{
		{"device_name": "Display", "status": "succeeded"},
		{"device_name": "Camera", "status": "failed", "error": "visca error 0x41: command not executable"},
		{"device_name": "Codec", "status": "expired"},
	})
	want := "Camera: visca error 0x41: command not executable; Codec: expired"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := dispatchFailures([]map[string]any{{"device_name": "Display", "status": "succeeded"}}); got != "" {
		t.Fatalf("all succeeded: got %q, want empty", got)
	}
}
