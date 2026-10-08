// Package cloudpoll runs the bridge's outbound command poller. It lives
// outside the `cloud` package because it needs to dispatch through the hub,
// and `hub` already imports `cloud` — putting the poller in `cloud` would
// create an import cycle.
//
// Poll shape: long-poll. The cloud's /bridge/poll handler blocks up to
// ~25s waiting for a cmd_pending NOTIFY, so the bridge just loops tight:
// a successful poll returns immediately when work exists, or after the
// server's hold window when there's nothing to do — no client-side
// ticker required. CommandPollInterval only paces reconnects after
// transport-level failures.
package cloudpoll

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/hub"
	"github.com/dloomes/av-bridge/internal/update"
)

// CommandReconnectName is the reserved command name the cloud uses to ask the
// bridge to Disconnect + Connect a device. Mirrors the constant on the cloud
// side; values must match.
const CommandReconnectName = "_reconnect"

// Poller polls the cloud for pending commands on a tick, dispatches them
// through the local hub, and posts results back. Uses the same HMAC signing
// scheme as the ingest push (X-Signature: sha256=<hex>) so the bridge only has
// one auth posture to maintain.
type Poller struct {
	cfg         config.CloudConfig
	collectorID string
	hub         *hub.Hub
	http        *http.Client
	baseURL     string
	onResync    func()
	// Self-update plumbing (see internal/update). All optional.
	version    string
	capability func() (bool, string)
	onUpdate   func(update.Instruction)
	onPollOK   func()
	onRole     func(role string, ttl time.Duration)
}

func NewPoller(cfg config.CloudConfig, collectorID string, h *hub.Hub) *Poller {
	base := strings.TrimRight(cfg.PortalAPI, "/")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
	}
	// HTTP client timeout must exceed the server's max hold window so a
	// healthy long-poll never trips it — see CommandLongPollTimeout doc.
	timeout := cfg.CommandLongPollTimeout
	if timeout <= 0 {
		timeout = 35 * time.Second
	}
	return &Poller{
		cfg:         cfg,
		collectorID: collectorID,
		hub:         h,
		baseURL:     base,
		http:        &http.Client{Timeout: timeout, Transport: transport},
	}
}

// Run blocks until ctx is cancelled. No-ops if portal_api or hmac_secret are
// unset — both are required for the bridge to authenticate against the cloud.
//
// Loop shape: long-poll, no client ticker. pollOnce returns immediately
// with work when work exists, or after the server's max hold when it
// doesn't — either way we re-poll straight away. On transport error we
// back off by CommandPollInterval so a wedged upstream doesn't get
// hammered.
func (p *Poller) Run(ctx context.Context) {
	if p.baseURL == "" || p.cfg.HMACSecret == "" {
		slog.Warn("command poller disabled (cloud.portal_api or cloud.hmac_secret missing)")
		return
	}
	slog.Info("command poller started (long-poll)",
		"reconnect_backoff", p.cfg.CommandPollInterval,
		"long_poll_timeout", p.cfg.CommandLongPollTimeout,
		"base_url", p.baseURL, "collector_id", p.collectorID)

	for {
		if ctx.Err() != nil {
			return
		}
		if ok := p.pollOnce(ctx); !ok {
			// Transport / decode failure — back off before retrying so
			// a hard-down cloud doesn't get retried in a tight loop.
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.cfg.CommandPollInterval):
			}
		}
	}
}

type bridgeCommand struct {
	ID         string          `json:"id"`
	DeviceID   string          `json:"device_id"`
	ReportedID string          `json:"reported_id"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args,omitempty"`
}

type pollResp struct {
	Commands []bridgeCommand `json:"commands"`
	// Resync: the cloud says this collector's device set changed since
	// its last config pull — hand off to the config puller now.
	Resync bool `json:"resync,omitempty"`
	// Update: the cloud offers a new collector version to install.
	Update *update.Instruction `json:"update,omitempty"`
	// Role / LeaseTTLSeconds: warm-standby group membership ("active" or
	// "standby"); empty when the collector isn't in a group.
	Role            string `json:"role,omitempty"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds,omitempty"`
}

// OnRole registers the callback that receives the group role from every
// successful poll (see internal/ha). Set before Run.
func (p *Poller) OnRole(fn func(role string, ttl time.Duration)) {
	p.onRole = fn
}

// EnableUpdates tells the cloud, on every poll, which version and
// platform this collector runs and whether it can update itself, and
// routes update offers to onUpdate. onPollOK runs after every
// successful poll (the updater uses the first one as its health check).
func (p *Poller) EnableUpdates(version string, capability func() (bool, string), onUpdate func(update.Instruction), onPollOK func()) {
	p.version = version
	p.capability = capability
	p.onUpdate = onUpdate
	p.onPollOK = onPollOK
}

// ReportUpdate posts a self-update state to the cloud. Best-effort.
func (p *Poller) ReportUpdate(ctx context.Context, state, version, message string) {
	if p.baseURL == "" || p.cfg.HMACSecret == "" {
		return
	}
	b, _ := json.Marshal(map[string]any{
		"collector_id": p.collectorID,
		"state":        state,
		"version":      version,
		"message":      message,
	})
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := p.signedPost(rctx, "/bridge/update-status", b)
	if err != nil {
		slog.Warn("update status post failed", "state", state, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		drained, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("update status rejected", "status", resp.StatusCode, "body", string(drained))
	}
}

// OnResync registers the callback run when a poll response carries the
// cloud's resync flag. Set before Run.
func (p *Poller) OnResync(fn func()) {
	p.onResync = fn
}

// pollOnce returns true on any successful round-trip (including an empty
// commands list after a long-hold), false on transport/decode/HTTP-error
// failure. The caller uses the false result to trigger reconnect backoff.
func (p *Poller) pollOnce(ctx context.Context) bool {
	req := map[string]any{
		"collector_id": p.collectorID,
		"max":          p.cfg.CommandMaxBatch,
	}
	if p.capability != nil {
		capable, blocker := p.capability()
		req["version"] = p.version
		req["platform"] = update.Platform()
		req["update_capable"] = capable
		req["update_blocker"] = blocker
	}
	body, _ := json.Marshal(req)
	resp, err := p.signedPost(ctx, "/bridge/poll", body)
	if err != nil {
		// ctx cancels aren't a "real" error — the shutdown path handles
		// that. Everything else is worth logging.
		if ctx.Err() == nil {
			slog.Warn("command poll request failed", "error", err)
		}
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("command poll rejected", "status", resp.StatusCode, "body", string(b))
		return false
	}
	var out pollResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		slog.Warn("command poll decode failed", "error", err)
		return false
	}
	if p.onPollOK != nil {
		p.onPollOK()
	}
	// Role before commands: a machine told it's standby drops its devices
	// first (the cloud doesn't send a standby commands anyway).
	if p.onRole != nil {
		p.onRole(out.Role, time.Duration(out.LeaseTTLSeconds)*time.Second)
	}
	if out.Resync && p.onResync != nil {
		p.onResync()
	}
	if out.Update != nil && p.onUpdate != nil {
		p.onUpdate(*out.Update)
	}
	for _, c := range out.Commands {
		p.execute(ctx, c)
	}
	return true
}

// execute dispatches one command through the hub and reports the result back.
// Errors are reported as failures — the bridge never silently drops a claimed
// command.
func (p *Poller) execute(ctx context.Context, cmd bridgeCommand) {
	dev := p.hub.GetDevice(cmd.ReportedID)
	if dev == nil {
		p.report(ctx, cmd.ID, nil, fmt.Errorf("device not present on this collector: %s", cmd.ReportedID))
		return
	}

	if cmd.Name == CommandReconnectName {
		_ = dev.Disconnect()
		if err := dev.Connect(ctx); err != nil {
			p.report(ctx, cmd.ID, nil, err)
			return
		}
		p.report(ctx, cmd.ID, &device.CommandResponse{Raw: "reconnected"}, nil)
		return
	}

	var args map[string]any
	if len(cmd.Args) > 0 {
		_ = json.Unmarshal(cmd.Args, &args)
	}
	resp, err := dev.SendCommand(ctx, device.CommandRequest{Name: cmd.Name, Args: args})
	if err != nil {
		p.report(ctx, cmd.ID, nil, err)
		return
	}
	p.report(ctx, cmd.ID, resp, nil)
}

func (p *Poller) report(ctx context.Context, commandID string, result *device.CommandResponse, execErr error) {
	body := map[string]any{"collector_id": p.collectorID}
	if execErr != nil {
		body["error"] = execErr.Error()
	} else if result != nil {
		body["result"] = result
	}
	b, _ := json.Marshal(body)
	resp, err := p.signedPost(ctx, "/bridge/commands/"+commandID+"/result", b)
	if err != nil {
		slog.Warn("command result post failed", "command_id", commandID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		drained, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("command result rejected", "command_id", commandID, "status", resp.StatusCode, "body", string(drained))
	}
}

func (p *Poller) signedPost(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	mac := hmac.New(sha256.New, []byte(p.cfg.HMACSecret))
	mac.Write(body)
	req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return p.http.Do(req)
}
