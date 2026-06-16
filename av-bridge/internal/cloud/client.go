package cloud

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Payload is the envelope sent to the cloud webhook
type Payload struct {
	Source      string              `json:"source"`
	Timestamp   time.Time           `json:"timestamp"`
	CollectorID string              `json:"collector_id,omitempty"`
	SiteID      string              `json:"site_id,omitempty"`
	Telemetry   []*device.Telemetry `json:"telemetry,omitempty"`
	Events      []*device.Event     `json:"events,omitempty"`
}

// Client batches telemetry and events and pushes them to the cloud portal.
type Client struct {
	cfg         config.CloudConfig
	collectorID string
	siteID      string
	http        *http.Client
	telemu      sync.Mutex
	pending     []*device.Telemetry
	eventsMu    sync.Mutex
	pendingEv   []*device.Event
}

func NewClient(cfg config.CloudConfig, collectorID, siteID string) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
	}
	return &Client{
		cfg:         cfg,
		collectorID: collectorID,
		siteID:      siteID,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
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

	slog.Info("cloud publisher started", "interval", c.cfg.PushInterval, "url", c.cfg.WebhookURL)

	for {
		select {
		case <-ctx.Done():
			// Final flush
			c.flush(context.Background())
			return
		case <-ticker.C:
			c.flush(ctx)
		}
	}
}

func (c *Client) flush(ctx context.Context) {
	c.telemu.Lock()
	tel := c.pending
	c.pending = nil
	c.telemu.Unlock()

	c.eventsMu.Lock()
	evs := c.pendingEv
	c.pendingEv = nil
	c.eventsMu.Unlock()

	if len(tel) == 0 && len(evs) == 0 {
		return
	}

	payload := Payload{
		Source:      "av-bridge",
		Timestamp:   time.Now().UTC(),
		CollectorID: c.collectorID,
		SiteID:      c.siteID,
		Telemetry:   tel,
		Events:      evs,
	}

	slog.Debug("flushing to cloud", "telemetry", len(tel), "events", len(evs))

	var lastErr error
	for attempt := 1; attempt <= c.cfg.RetryAttempts; attempt++ {
		if err := c.send(ctx, payload); err != nil {
			lastErr = err
			slog.Warn("cloud push failed", "attempt", attempt, "error", err)
			if attempt < c.cfg.RetryAttempts {
				select {
				case <-ctx.Done():
					return
				case <-time.After(c.cfg.RetryDelay):
				}
			}
			continue
		}
		slog.Info("cloud push succeeded", "telemetry", len(tel), "events", len(evs))
		return
	}

	slog.Error("cloud push permanently failed, data lost", "error", lastErr,
		"lost_telemetry", len(tel), "lost_events", len(evs))
}

func (c *Client) send(ctx context.Context, payload Payload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

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
		return fmt.Errorf("cloud returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// PushImmediate sends a single payload immediately without buffering.
func (c *Client) PushImmediate(ctx context.Context, payload Payload) error {
	return c.send(ctx, payload)
}
