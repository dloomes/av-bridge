package update

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Instruction is the cloud's offer of a new version, delivered in the
// /bridge/poll response.
type Instruction struct {
	Version   string `json:"version"`
	Artefact  string `json:"artefact"` // e.g. "av-bridge-linux-amd64"
	URL       string `json:"url"`      // path on the cloud, e.g. "/public/downloads/av-bridge-linux-amd64"
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// Report states sent back to the cloud.
const (
	StateFailed     = "failed"
	StateRolledBack = "rolled_back"
	StateSucceeded  = "succeeded"
	StateRestarting = "restarting"
)

// Reporter sends an update state to the cloud. Best-effort: the cloud
// also times out updates it never hears back about.
type Reporter func(ctx context.Context, state, version, message string)

// Config is what the updater needs from the running collector.
type Config struct {
	Version    string // version of this running binary
	ExePath    string // the binary the service starts
	StateDir   string // where the pending-update marker lives
	ConfigPath string // passed to the new binary's -validate
	EnvPath    string // passed to the new binary's -validate, if set
	CloudBase  string // portal_api base URL; download URLs are relative to it
	TLSSkip    bool
	// Service reports whether the process runs under a service manager
	// that will restart it after the update exits it.
	Service bool
}

// Updater applies one update at a time.
type Updater struct {
	cfg     Config
	report  Reporter
	restart func() // asks main to shut down cleanly and exit for restart
	http    *http.Client
	busy    atomic.Bool
}

func New(cfg Config, report Reporter, restart func()) *Updater {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkip},
	}
	return &Updater{
		cfg:     cfg,
		report:  report,
		restart: restart,
		http:    &http.Client{Timeout: 10 * time.Minute, Transport: transport},
	}
}

// Platform is "os/arch" for the running binary.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Capability reports whether this collector can update itself, and if
// not, why — shown in the portal so an operator knows what to fix.
func (u *Updater) Capability() (bool, string) {
	if !HasTrustedKey() {
		return false, "this build has no update signing key"
	}
	if inContainer() {
		return false, "runs in a container; update the container image instead"
	}
	if !u.cfg.Service {
		return false, "not running as a service, so nothing would restart it after an update"
	}
	if u.cfg.ExePath == "" || u.cfg.CloudBase == "" {
		return false, "cloud.portal_api is not configured"
	}
	dir := filepath.Dir(u.cfg.ExePath)
	if err := writable(dir); err != nil {
		return false, fmt.Sprintf("the service can't write to %s; re-run the installer to enable updates", dir)
	}
	return true, ""
}

func inContainer() bool {
	if os.Getenv("AV_BRIDGE_CONTAINER") != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".av-bridge-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Apply downloads, verifies and installs inst, then asks for a restart.
// Runs synchronously; callers start it in a goroutine. A second call
// while one is running is ignored.
func (u *Updater) Apply(ctx context.Context, inst Instruction) {
	if !u.busy.CompareAndSwap(false, true) {
		return
	}
	defer u.busy.Store(false)

	log := slog.With("from", u.cfg.Version, "to", inst.Version)
	log.Info("collector update starting")
	if err := u.install(ctx, inst); err != nil {
		log.Error("collector update failed", "error", err)
		u.report(ctx, StateFailed, inst.Version, err.Error())
		return
	}
	log.Info("collector update installed, restarting")
	u.report(ctx, StateRestarting, inst.Version, "")
	u.restart()
}

const maxDownload = 200 << 20

func (u *Updater) install(ctx context.Context, inst Instruction) error {
	if ok, why := u.Capability(); !ok {
		return errors.New(why)
	}
	if inst.Version == "" || inst.SHA256 == "" || inst.Signature == "" || inst.Artefact == "" {
		return errors.New("incomplete update offer from the cloud")
	}
	if inst.Version == u.cfg.Version {
		return fmt.Errorf("already running %s", inst.Version)
	}
	if !strings.HasPrefix(inst.URL, "/") {
		return errors.New("update URL must be a path on the cloud")
	}

	// 1. Download next to the live binary so the final rename is atomic.
	dir := filepath.Dir(u.cfg.ExePath)
	tmp := filepath.Join(dir, ".av-bridge-update"+exeSuffix())
	_ = os.Remove(tmp)
	sum, err := u.download(ctx, strings.TrimRight(u.cfg.CloudBase, "/")+inst.URL, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	// 2. Hash and signature.
	if !strings.EqualFold(sum, inst.SHA256) {
		return fmt.Errorf("download corrupt: SHA-256 %s, expected %s", sum, inst.SHA256)
	}
	if !Verify(inst.Artefact, inst.Version, strings.ToLower(inst.SHA256), inst.Signature) {
		return errors.New("signature check failed — update rejected")
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}

	// 3. The new binary must say it's the version we expect, and accept
	//    this collector's config.
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, tmp, "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), inst.Version) {
		return fmt.Errorf("new binary reports %q, expected %s", strings.TrimSpace(string(out)), inst.Version)
	}
	args := []string{"-config", u.cfg.ConfigPath, "-validate"}
	if u.cfg.EnvPath != "" {
		args = append(args, "-env", u.cfg.EnvPath)
	}
	if out, err := exec.CommandContext(vctx, tmp, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("new version rejects this collector's config: %s", strings.TrimSpace(string(out)))
	}

	// 4. Record the attempt before touching the live binary, so a crash
	//    from here on still ends in a rollback or a report.
	prev := u.cfg.ExePath + ".prev"
	if err := writeMarker(u.cfg.StateDir, marker{
		From: u.cfg.Version, To: inst.Version, ExePath: u.cfg.ExePath,
		PrevPath: prev, StartedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record pending update: %w", err)
	}

	// 5. Swap. Renaming a running binary is allowed on Linux and Windows.
	_ = os.Remove(prev)
	if err := os.Rename(u.cfg.ExePath, prev); err != nil {
		clearMarker(u.cfg.StateDir)
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(tmp, u.cfg.ExePath); err != nil {
		_ = os.Rename(prev, u.cfg.ExePath)
		clearMarker(u.cfg.StateDir)
		return fmt.Errorf("install new binary: %w", err)
	}
	cleanup = false
	return nil
}

func (u *Updater) download(ctx context.Context, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxDownload {
		return "", errors.New("download larger than 200 MB")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ---- pending-update marker and rollback ------------------------------------

// marker records an update in flight across the restart.
type marker struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	ExePath   string    `json:"exe_path"`
	PrevPath  string    `json:"prev_path"`
	StartedAt time.Time `json:"started_at"`
	Boots     int       `json:"boots"`
	// RollbackReason is set once a rollback has been done, for the
	// restored old version to report.
	RollbackReason string `json:"rollback_reason,omitempty"`
}

const markerName = "update-pending.json"

func writeMarker(dir string, m marker) error {
	b, _ := json.Marshal(m)
	tmp := filepath.Join(dir, markerName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, markerName))
}

func readMarker(dir string) (marker, bool) {
	b, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func clearMarker(dir string) { _ = os.Remove(filepath.Join(dir, markerName)) }

// HealthWindow is how long a freshly updated collector has to reach the
// cloud before it rolls itself back.
const HealthWindow = 5 * time.Minute

// maxBoots: a new version that keeps crashing before the health window
// ends is rolled back on this many starts.
const maxBoots = 3

// Startup is called once, early, on every start. It resolves an update
// left in flight by the previous run:
//   - running the new version: start the health watch (see Watch);
//   - running the old version after a rollback: report it.
//
// It returns a Watch when the caller must confirm health, or nil.
func (u *Updater) Startup(ctx context.Context) *Watch {
	m, ok := readMarker(u.cfg.StateDir)
	if !ok {
		return nil
	}
	switch u.cfg.Version {
	case m.To:
		m.Boots++
		if m.Boots > maxBoots {
			u.rollback(ctx, m, fmt.Sprintf("%s failed to start %d times", m.To, maxBoots))
			return nil
		}
		_ = writeMarker(u.cfg.StateDir, m)
		w := &Watch{u: u, m: m, healthy: make(chan struct{}), done: make(chan struct{})}
		go w.run(ctx)
		return w
	case m.From:
		reason := m.RollbackReason
		if reason == "" {
			reason = "the update did not take effect"
		}
		u.report(ctx, StateRolledBack, m.To, reason)
		clearMarker(u.cfg.StateDir)
	default:
		clearMarker(u.cfg.StateDir)
	}
	return nil
}

// rollback restores the previous binary and restarts into it. The marker
// stays, carrying the reason, so the restored version can report it.
func (u *Updater) rollback(ctx context.Context, m marker, reason string) {
	slog.Error("collector update rolling back", "to", m.From, "reason", reason)
	if _, err := os.Stat(m.PrevPath); err != nil {
		slog.Error("rollback impossible: previous binary missing", "path", m.PrevPath)
		u.report(ctx, StateFailed, m.To, reason+"; rollback impossible, previous binary missing")
		clearMarker(u.cfg.StateDir)
		return
	}
	failed := m.ExePath + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(m.ExePath, failed); err != nil {
		slog.Error("rollback: move failed binary aside", "error", err)
		return
	}
	if err := os.Rename(m.PrevPath, m.ExePath); err != nil {
		_ = os.Rename(failed, m.ExePath)
		slog.Error("rollback: restore previous binary", "error", err)
		return
	}
	m.RollbackReason = reason
	_ = writeMarker(u.cfg.StateDir, m)
	u.restart()
}

// Watch confirms a just-updated collector is healthy.
type Watch struct {
	u       *Updater
	m       marker
	healthy chan struct{}
	done    chan struct{}
	once    atomic.Bool
}

// Healthy is called after the first successful cloud poll.
func (w *Watch) Healthy() {
	if w.once.CompareAndSwap(false, true) {
		close(w.healthy)
	}
}

func (w *Watch) run(ctx context.Context) {
	defer close(w.done)
	t := time.NewTimer(HealthWindow)
	defer t.Stop()
	select {
	case <-w.healthy:
		clearMarker(w.u.cfg.StateDir)
		slog.Info("collector update confirmed", "version", w.m.To)
		w.u.report(ctx, StateSucceeded, w.m.To, "")
	case <-t.C:
		w.u.rollback(ctx, w.m, fmt.Sprintf("%s couldn't reach the cloud within %s", w.m.To, HealthWindow))
	case <-ctx.Done():
	}
}
