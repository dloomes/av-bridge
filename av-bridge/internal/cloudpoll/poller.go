// Package cloudpoll runs the bridge's outbound command poller. It lives
// outside the `cloud` package because it needs to dispatch through the hub,
// and `hub` already imports `cloud` — putting the poller in `cloud` would
// create an import cycle.
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
}

func NewPoller(cfg config.CloudConfig, collectorID string, h *hub.Hub) *Poller {
	base := strings.TrimRight(cfg.PortalAPI, "/")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
	}
	return &Poller{
		cfg:         cfg,
		collectorID: collectorID,
		hub:         h,
		baseURL:     base,
		http:        &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}
}

// Run blocks until ctx is cancelled. No-ops if portal_api or hmac_secret are
// unset — both are required for the bridge to authenticate against the cloud.
func (p *Poller) Run(ctx context.Context) {
	if p.baseURL == "" || p.cfg.HMACSecret == "" {
		slog.Warn("command poller disabled (cloud.portal_api or cloud.hmac_secret missing)")
		return
	}
	ticker := time.NewTicker(p.cfg.CommandPollInterval)
	defer ticker.Stop()
	slog.Info("command poller started",
		"interval", p.cfg.CommandPollInterval, "base_url", p.baseURL, "collector_id", p.collectorID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
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
}

func (p *Poller) pollOnce(ctx context.Context) {
	body, _ := json.Marshal(map[string]any{
		"collector_id": p.collectorID,
		"max":          p.cfg.CommandMaxBatch,
	})
	resp, err := p.signedPost(ctx, "/bridge/poll", body)
	if err != nil {
		slog.Warn("command poll request failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("command poll rejected", "status", resp.StatusCode, "body", string(b))
		return
	}
	var out pollResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		slog.Warn("command poll decode failed", "error", err)
		return
	}
	for _, c := range out.Commands {
		p.execute(ctx, c)
	}
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
