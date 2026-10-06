package cloudpull

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices-cache.bin")
	in := []wireDevice{{
		ID: "display-1", Name: "Display", Type: "display", Protocol: "sony_bravia",
		Address: "10.0.0.5", Password: "s3cret", PollRate: 30,
		Tags: map[string]string{"room": "boardroom"},
	}}
	if err := saveCache(path, "secret-a", in); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "10.0.0.5") {
		t.Fatal("cache must not hold device config in plaintext")
	}

	got, ok, err := LoadCache(path, "secret-a")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	want := in[0].toConfig()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got[0].PollRate != 30*time.Second {
		t.Fatalf("poll rate = %v", got[0].PollRate)
	}
}

func TestCacheFromAnotherSecretIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices-cache.bin")
	if err := saveCache(path, "old-secret", []wireDevice{{ID: "d"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadCache(path, "new-secret"); ok || err == nil {
		t.Fatalf("want unusable cache after secret change, got ok=%v err=%v", ok, err)
	}
}

func TestMissingCache(t *testing.T) {
	_, ok, err := LoadCache(filepath.Join(t.TempDir(), "none"), "s")
	if ok || err != nil {
		t.Fatalf("missing cache: ok=%v err=%v", ok, err)
	}
}
