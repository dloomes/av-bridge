//go:build !windows

package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dloomes/av-bridge/internal/store"
)

// The state directory is created owner+group only (0750): it holds device
// state and alert de-duplication that no other user needs.
func TestNew_CreatesStateDirWithoutWorldAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := store.New(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Fatalf("state dir mode %v grants access to other users", perm)
	}
}
