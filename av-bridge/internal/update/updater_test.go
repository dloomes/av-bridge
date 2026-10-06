package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := trustedKeys
	trustedKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	t.Cleanup(func() { trustedKeys = old })
	return priv
}

func TestSignatureBindsArtefactAndVersion(t *testing.T) {
	priv := testKey(t)
	sha := SHA256Hex([]byte("binary"))
	sig := Sign(priv, "av-bridge-linux-amd64", "v1.2.0", sha)
	if !Verify("av-bridge-linux-amd64", "v1.2.0", sha, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify("av-bridge-linux-amd64", "v1.3.0", sha, sig) {
		t.Fatal("signature must not verify for a different version (replay as newer)")
	}
	if Verify("av-bridge-linux-arm64", "v1.2.0", sha, sig) {
		t.Fatal("signature must not verify for a different artefact")
	}
	if Verify("av-bridge-linux-amd64", "v1.2.0", SHA256Hex([]byte("other")), sig) {
		t.Fatal("signature must not verify for different contents")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if Verify("av-bridge-linux-amd64", "v1.2.0", sha, Sign(other, "av-bridge-linux-amd64", "v1.2.0", sha)) {
		t.Fatal("signature from an untrusted key accepted")
	}
}

type recorder struct {
	mu     sync.Mutex
	states []string
	msgs   []string
}

func (r *recorder) report(_ context.Context, state, _ string, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
	r.msgs = append(r.msgs, msg)
}

func (r *recorder) last() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return "", ""
	}
	return r.states[len(r.states)-1], r.msgs[len(r.msgs)-1]
}

// newTestUpdater lays out a fake install: a "live" binary file in dir.
func newTestUpdater(t *testing.T, cloud string) (*Updater, *recorder, *int, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "av-bridge"+exeSuffix())
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	restarts := 0
	u := New(Config{
		Version: "v1.0.0", ExePath: exe, StateDir: dir, ConfigPath: "x",
		CloudBase: cloud, Service: true,
	}, rec.report, func() { restarts++ })
	return u, rec, &restarts, exe
}

func TestApplyRejectsBadSignatureAndHash(t *testing.T) {
	priv := testKey(t)
	payload := []byte("new binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer srv.Close()

	u, rec, restarts, exe := newTestUpdater(t, srv.URL)
	sha := SHA256Hex(payload)

	// Wrong hash.
	u.Apply(context.Background(), Instruction{Version: "v1.1.0", Artefact: "a", URL: "/d",
		SHA256: SHA256Hex([]byte("x")), Signature: Sign(priv, "a", "v1.1.0", sha)})
	if s, m := rec.last(); s != StateFailed || m == "" {
		t.Fatalf("bad hash: got %s %q", s, m)
	}
	// Signed for another version.
	u.Apply(context.Background(), Instruction{Version: "v1.1.0", Artefact: "a", URL: "/d",
		SHA256: sha, Signature: Sign(priv, "a", "v9.9.9", sha)})
	if s, _ := rec.last(); s != StateFailed {
		t.Fatalf("bad signature: got %s", s)
	}
	if *restarts != 0 {
		t.Fatal("a rejected update must not restart the collector")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("a rejected update must leave the live binary alone")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".av-bridge-update*")); len(left) != 0 {
		t.Fatalf("temp download not cleaned up: %v", left)
	}
}

func TestStartupRollsBackAfterRepeatedCrashes(t *testing.T) {
	u, rec, restarts, exe := newTestUpdater(t, "http://cloud")
	dir := filepath.Dir(exe)
	if err := os.WriteFile(exe+".prev", []byte("old-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(dir, marker{From: "v0.9.0", To: "v1.0.0", ExePath: exe, PrevPath: exe + ".prev", Boots: maxBoots}); err != nil {
		t.Fatal(err)
	}
	if w := u.Startup(context.Background()); w != nil {
		t.Fatal("a crash-looping version should roll back, not watch")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old-good" {
		t.Fatal("previous binary should be restored")
	}
	if *restarts != 1 {
		t.Fatalf("want a restart into the old version, got %d", *restarts)
	}

	// The restored old version starts and reports the rollback.
	old := New(Config{Version: "v0.9.0", ExePath: exe, StateDir: dir, Service: true}, rec.report, func() {})
	old.Startup(context.Background())
	if s, m := rec.last(); s != StateRolledBack || m == "" {
		t.Fatalf("old version should report rolled_back with a reason, got %s %q", s, m)
	}
	if _, ok := readMarker(dir); ok {
		t.Fatal("marker should be cleared once the rollback is reported")
	}
}

func TestStartupConfirmsHealthyUpdate(t *testing.T) {
	u, rec, restarts, exe := newTestUpdater(t, "http://cloud")
	dir := filepath.Dir(exe)
	_ = writeMarker(dir, marker{From: "v0.9.0", To: "v1.0.0", ExePath: exe, PrevPath: exe + ".prev"})
	w := u.Startup(context.Background())
	if w == nil {
		t.Fatal("new version should start a health watch")
	}
	w.Healthy()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not finish after Healthy")
	}
	if s, _ := rec.last(); s != StateSucceeded {
		t.Fatalf("want succeeded, got %s", s)
	}
	if *restarts != 0 {
		t.Fatal("healthy update must not restart")
	}
	if _, ok := readMarker(dir); ok {
		t.Fatal("marker should be cleared")
	}
}

func TestCapabilityBlockers(t *testing.T) {
	u, _, _, _ := newTestUpdater(t, "http://cloud")
	if ok, why := u.Capability(); !ok && !inContainer() {
		t.Fatalf("writable service install should be capable: %s", why)
	}
	u.cfg.Service = false
	if ok, _ := u.Capability(); ok {
		t.Fatal("foreground (non-service) collectors can't restart themselves")
	}
}
