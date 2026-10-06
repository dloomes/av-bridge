package cloud

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/hostinfo"
)

// Payload is the envelope sent to the cloud webhook.
//
// BridgeVersion + BridgeBuildTime are what this binary reports about itself
// (set from -ldflags at build). The cloud persists them onto the collectors
// row so support can answer "what code is this site running?" without SSH.
type Payload struct {
	Source          string              `json:"source"`
	Timestamp       time.Time           `json:"timestamp"`
	CollectorID     string              `json:"collector_id,omitempty"`
	SiteID          string              `json:"site_id,omitempty"`
	BridgeVersion   string              `json:"bridge_version,omitempty"`
	BridgeBuildTime string              `json:"bridge_build_time,omitempty"`
	// BridgeOS is a compact human-readable OS+arch descriptor for the
	// collector host (e.g. "Ubuntu 22.04.4 LTS (linux/amd64)"). Same
	// motivation as BridgeVersion — surfaces on the /collectors page
	// so support can spot version-specific issues without SSH.
	BridgeOS  string              `json:"bridge_os,omitempty"`
	Telemetry []*device.Telemetry `json:"telemetry,omitempty"`
	Events    []*device.Event     `json:"events,omitempty"`
}

// Client batches telemetry and events and pushes them to the cloud portal.
// Payloads the cloud doesn't accept are spooled to disk and replayed in
// order once it's reachable again (see spool).
type Client struct {
	cfg         config.CloudConfig
	collectorID string
	siteID      string
	version     string
	buildTime   string
	http        *http.Client
	spool       *spool // nil when the spool dir couldn't be created
	telemu      sync.Mutex
	pending     []*device.Telemetry
	eventsMu    sync.Mutex
	pendingEv   []*device.Event
}

// NewClient returns a cloud publisher. version and buildTime are attached
// to every payload so the cloud can render "what version each site is on"
// without a separate control-plane call.
func NewClient(cfg config.CloudConfig, collectorID, siteID, version, buildTime string) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
	}
	c := &Client{
		cfg:         cfg,
		collectorID: collectorID,
		siteID:      siteID,
		version:     version,
		buildTime:   buildTime,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
	if cfg.SpoolDir != "" {
		sp, err := newSpool(cfg.SpoolDir, cfg.SpoolMaxBytes, cfg.SpoolMaxAge)
		if err != nil {
			slog.Warn("cloud spool unavailable — data will be dropped while the cloud is unreachable",
				"dir", cfg.SpoolDir, "error", err)
		} else {
			c.spool = sp
		}
	}
	return c
}

// EnqueueTelemetry adds a telemetry snapshot to the outbound buffer.
func (c *Client) EnqueueTelemetry(t *device.Telemetry) {
	c.telemu.Lock()
	defer c.telemu.Unlock()
	c.pending = append(c.pending, t)
}

// EnqueueEvent adds a device event to the outbound buffer.
func (c *Client) EnqueueEvent(e *device.Event) {
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()
	c.pendingEv = append(c.pendingEv, e)
}

// Run starts the periodic flush loop. Blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PushInterval)
	defer ticker.Stop()

	slog.Info("cloud publisher started", "interval", c.cfg.PushInterval, "url", c.cfg.WebhookURL,
		"spool", c.cfg.SpoolDir)

	for {
		select {
		case <-ctx.Done():
			c.finalFlush()
			return
		case <-ticker.C:
			c.flush(ctx)
		}
	}
}

// drainBudget caps how long one tick spends replaying the spool, so a
// large backlog drains over several ticks instead of blocking the loop.
const drainBudget = 20 * time.Second

// flush replays any spooled payloads, then sends what's been buffered
// since the last tick. Order is preserved: while the spool has a backlog,
// new data joins the back of it rather than overtaking it.
func (c *Client) flush(ctx context.Context) {
	body, nTel, nEv, err := c.takePayload()
	if err != nil {
		slog.Error("cloud payload could not be built, data lost", "error", err)
		return
	}

	backlog := false
	if c.spool != nil {
		backlog = !c.drainSpool(ctx)
	}
	if body == nil {
		return
	}
	if backlog {
		c.spoolOrDrop(body, nTel, nEv, "earlier payloads not yet replayed")
		return
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.RetryAttempts; attempt++ {
		err := c.send(ctx, body)
		if err == nil {
			slog.Info("cloud push succeeded", "telemetry", nTel, "events", nEv)
			return
		}
		lastErr = err
		if isPermanent(err) {
			slog.Error("cloud rejected payload, data dropped", "error", err,
				"telemetry", nTel, "events", nEv)
			return
		}
		slog.Warn("cloud push failed", "attempt", attempt, "error", err)
		if attempt < c.cfg.RetryAttempts {
			select {
			case <-ctx.Done():
				c.spoolOrDrop(body, nTel, nEv, "shutting down")
				return
			case <-time.After(c.cfg.RetryDelay):
			}
		}
	}
	reason := "no attempts configured"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	c.spoolOrDrop(body, nTel, nEv, reason)
}

// finalFlush runs on shutdown: one quick attempt, then spool, so stopping
// the service never waits long on a dead cloud and never loses the last
// batch.
func (c *Client) finalFlush() {
	body, nTel, nEv, err := c.takePayload()
	if err != nil || body == nil {
		return
	}
	if c.spool != nil && !c.spool.empty() {
		c.spoolOrDrop(body, nTel, nEv, "shutting down with a backlog")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.send(ctx, body); err != nil && !isPermanent(err) {
		c.spoolOrDrop(body, nTel, nEv, err.Error())
	}
}

// takePayload swaps out the buffers and marshals them. body is nil when
// there was nothing to send.
func (c *Client) takePayload() (body []byte, nTel, nEv int, err error) {
	c.telemu.Lock()
	tel := c.pending
	c.pending = nil
	c.telemu.Unlock()

	c.eventsMu.Lock()
	evs := c.pendingEv
	c.pendingEv = nil
	c.eventsMu.Unlock()

	if len(tel) == 0 && len(evs) == 0 {
		return nil, 0, 0, nil
	}

	payload := Payload{
		Source:          "av-bridge",
		Timestamp:       time.Now().UTC(),
		CollectorID:     c.collectorID,
		SiteID:          c.siteID,
		BridgeVersion:   c.version,
		BridgeBuildTime: buildTimeRFC3339(c.buildTime),
		BridgeOS:        hostinfo.OS(),
		Telemetry:       tel,
		Events:          evs,
	}
	body, err = json.Marshal(payload)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("marshalling payload: %w", err)
	}
	slog.Debug("flushing to cloud", "telemetry", len(tel), "events", len(evs))
	return body, len(tel), len(evs), nil
}

// drainSpool replays spooled payloads oldest-first. Returns true when the
// spool is empty afterwards. Stops at the first retryable failure (the
// cloud is still unreachable) or when the per-tick budget runs out.
func (c *Client) drainSpool(ctx context.Context) bool {
	entries := c.spool.entries()
	if len(entries) == 0 {
		return true
	}
	deadline := time.Now().Add(drainBudget)
	done := 0
	defer func() {
		if done > 0 {
			slog.Info("replayed spooled cloud payloads", "replayed", done, "remaining", len(entries)-done)
		}
	}()
	for _, e := range entries {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		body, err := c.spool.read(e)
		if err != nil {
			slog.Warn("unreadable spool file dropped", "file", e.name, "error", err)
			c.spool.remove(e)
			done++
			continue
		}
		if err := c.send(ctx, body); err != nil {
			if isPermanent(err) {
				slog.Error("cloud rejected spooled payload, dropped", "file", e.name, "error", err)
				c.spool.remove(e)
				done++
				continue
			}
			slog.Debug("spool replay paused, cloud unreachable", "error", err)
			return false
		}
		c.spool.remove(e)
		done++
	}
	return true
}

func (c *Client) spoolOrDrop(body []byte, nTel, nEv int, reason string) {
	if c.spool == nil {
		slog.Error("cloud push permanently failed, data lost", "reason", reason,
			"lost_telemetry", nTel, "lost_events", nEv)
		return
	}
	if err := c.spool.put(body); err != nil {
		slog.Error("cloud push failed and spooling failed, data lost", "reason", reason,
			"spool_error", err, "lost_telemetry", nTel, "lost_events", nEv)
		return
	}
	slog.Warn("cloud push deferred, payload spooled for replay", "reason", reason,
		"telemetry", nTel, "events", nEv)
}

// buildTimeRFC3339 returns bt unchanged if it parses as RFC3339, otherwise
// the empty string. The cloud stores this as timestamptz; sending the
// placeholder "unknown" (what -ldflags default to on a plain `go build`
// without the Makefile) would fail the SQL cast. Empty is fine — cloud
// preserves the previously-recorded value via COALESCE.
func buildTimeRFC3339(bt string) string {
	if _, err := time.Parse(time.RFC3339, bt); err != nil {
		return ""
	}
	return bt
}

// statusError is a non-2xx response from the cloud.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("cloud returned HTTP %d", e.code) }

// isPermanent reports whether resending the same payload can't succeed
// because the cloud judged the payload itself bad. Auth failures (401/403)
// are not permanent — the secret may be mid-rotation — and neither are
// timeouts, throttling or 5xx.
func isPermanent(err error) bool {
	var se statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func (c *Client) send(ctx context.Context, b []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	// Sign the body so the cloud can verify which collector sent it. Matches the
	// "sha256=<hex>" scheme the cloud and the bridge's own webhook verifier use.
	if c.cfg.HMACSecret != "" {
		mac := hmac.New(sha256.New, []byte(c.cfg.HMACSecret))
		mac.Write(b)
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return statusError{code: resp.StatusCode}
	}
	return nil
}

// PushImmediate sends a single payload immediately without buffering.
func (c *Client) PushImmediate(ctx context.Context, payload Payload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}
	return c.send(ctx, b)
}
