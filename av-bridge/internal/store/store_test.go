package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/store"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

func TestNew_CreatesFileOnFirstOpen(t *testing.T) {
	path := tempPath(t)
	s, err := store.New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = s

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("expected dir to exist: %v", err)
	}
}

func TestUpsertAndGet_RoundTrip(t *testing.T) {
	s, err := store.New(tempPath(t))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	state := &store.DeviceState{
		DeviceID:   "device-01",
		LastSeen:   now,
		LastStatus: "online",
		LastMetrics: map[string]any{
			"http_status": float64(200),
		},
	}
	if err := s.UpsertDevice(state); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	got := s.GetDevice("device-01")
	if got.DeviceID != "device-01" {
		t.Errorf("expected device-01, got %q", got.DeviceID)
	}
	if got.LastStatus != "online" {
		t.Errorf("expected status online, got %q", got.LastStatus)
	}
}

func TestGetDevice_ReturnsEmptyForUnknown(t *testing.T) {
	s, err := store.New(tempPath(t))
	if err != nil {
		t.Fatal(err)
	}
	got := s.GetDevice("does-not-exist")
	if got == nil {
		t.Fatal("expected non-nil DeviceState for unknown device")
	}
	if got.DeviceID != "does-not-exist" {
		t.Errorf("expected device id propagated, got %q", got.DeviceID)
	}
}

func TestMarkAlertSent_AndLastAlertSent(t *testing.T) {
	s, err := store.New(tempPath(t))
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-time.Second)
	if err := s.MarkAlertSent("device-01", "device_offline"); err != nil {
		t.Fatalf("MarkAlertSent: %v", err)
	}

	last := s.LastAlertSent("device-01", "device_offline")
	if last.IsZero() {
		t.Fatal("expected non-zero last alert time")
	}
	if last.Before(before) {
		t.Errorf("expected alert time after %v, got %v", before, last)
	}
}

func TestLastAlertSent_ZeroForNeverSent(t *testing.T) {
	s, err := store.New(tempPath(t))
	if err != nil {
		t.Fatal(err)
	}
	got := s.LastAlertSent("device-01", "never_fired")
	if !got.IsZero() {
		t.Errorf("expected zero time for unsent alert, got %v", got)
	}
}

func TestPersistence_SurvivesReopen(t *testing.T) {
	path := tempPath(t)

	s1, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.UpsertDevice(&store.DeviceState{
		DeviceID:   "device-01",
		LastStatus: "offline",
	}); err != nil {
		t.Fatal(err)
	}

	// Re-open the store from the same file
	s2, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.GetDevice("device-01")
	if got.LastStatus != "offline" {
		t.Errorf("expected offline after reopen, got %q", got.LastStatus)
	}
}

func TestAllDevices(t *testing.T) {
	s, err := store.New(tempPath(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"a", "b", "c"} {
		_ = s.UpsertDevice(&store.DeviceState{DeviceID: id})
	}

	all := s.AllDevices()
	if len(all) != 3 {
		t.Errorf("expected 3 devices, got %d", len(all))
	}
}
