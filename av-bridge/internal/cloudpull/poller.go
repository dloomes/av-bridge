// Package cloudpull is the bridge's config-pull loop. The cloud is the source
// of truth for per-device protocol configuration; this poller fetches the
// device set on a slow tick (device_sync_interval, default 5 min) and hands it
// to the hub for reconciliation.
//
// On first run the cloud's set for this collector is empty. To avoid the
// chicken-and-egg, the bridge seeds its locally-loaded YAML up to the cloud
// once and then transitions to cloud-as-source-of-truth. The seed PUT is
// idempotent on the cloud side: subsequent calls 409 once the collector has
// devices, so a returning bridge can't overwrite portal-side edits.
//
// Lives outside the cloud package because it needs to dispatch through the
// hub, and hub already imports cloud — putting it in cloud would create an
// import cycle. Mirrors the cloudpoll package shape.
package cloudpull

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
	"github.com/dloomes/av-bridge/internal/hub"
)

// Subscription is the wire-side mirror of config.SubscriptionSpec. Duplicated
// (rather than imported) because the cloud's JSON shape is independent of the
// bridge's YAML shape — keeps both sides free to evolve without churning the
// other.
type Subscription struct {
	Tag       string `json:"tag"`
	Attribute string `json:"attribute"`
	Channel   int    `json:"channel"`
	Label     string `json:"label"`
	Rate      int    `json:"rate,omitempty"`
}

// wireDevice mirrors bridgecfg.Device on the cloud side. Conversion to/from
// config.DeviceConfig is explicit because PollRate is time.Duration locally
// and seconds-as-int on the wire.
type wireDevice struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Type          string            `json:"type,omitempty"`
	Protocol      string            `json:"protocol,omitempty"`
	Address       string            `json:"address,omitempty"`
	BaudRate      int               `json:"baud_rate,omitempty"`
	Username      string            `json:"username,omitempty"`
	Password      string            `json:"password,omitempty"`
	PollRate      int               `json:"poll_rate_seconds,omitempty"`
	Commands      map[string]string `json:"commands,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	Subscriptions []Subscription    `json:"subscriptions,omitempty"`
}

func (w wireDevice) toConfig() config.DeviceConfig {
	// Preserve nil-vs-empty distinction so reflect.DeepEqual matches the
	// YAML-loaded local config, which leaves absent slices nil rather than
	// allocating an empty one.
	var subs []config.SubscriptionSpec
	if len(w.Subscriptions) > 0 {
		subs = make([]config.SubscriptionSpec, len(w.Subscriptions))
		for i, s := range w.Subscriptions {
			subs[i] = config.SubscriptionSpec{
				Tag: s.Tag, Attribute: s.Attribute, Channel: s.Channel,
				Label: s.Label, Rate: s.Rate,
			}
		}
	}
	return config.DeviceConfig{
		ID:            w.ID,
		Name:          w.Name,
		Type:          w.Type,
		Protocol:      w.Protocol,
		Address:       w.Address,
		BaudRate:      w.BaudRate,
		Username:      w.Username,
		Password:      w.Password,
		PollRate:      time.Duration(w.PollRate) * time.Second,
		Commands:      w.Commands,
		Tags:          w.Tags,
		Subscriptions: subs,
	}
}

func fromConfig(c config.DeviceConfig) wireDevice {
	var subs []Subscription
	if len(c.Subscriptions) > 0 {
		subs = make([]Subscription, len(c.Subscriptions))
		for i, s := range c.Subscriptions {
			subs[i] = Subscription{
				Tag: s.Tag, Attribute: s.Attribute, Channel: s.Channel,
				Label: s.Label, Rate: s.Rate,
			}
		}
	}
	return wireDevice{
		ID:            c.ID,
		Name:          c.Name,
		Type:          c.Type,
		Protocol:      c.Protocol,
		Address:       c.Address,
		BaudRate:      c.BaudRate,
		Username:      c.Username,
		Password:      c.Password,
		PollRate:      int(c.PollRate.Seconds()),
		Commands:      c.Commands,
		Tags:          c.Tags,
		Subscriptions: subs,
	}
}

type getResp struct {
	Devices []wireDevice `json:"devices"`
}

type putReq struct {
	CollectorID string       `json:"collector_id"`
	Devices     []wireDevice `json:"devices"`
}

// Poller fetches the device set from the cloud on a tick and reconciles the
// hub. Disabled (no-op Run) when portal_api or hmac_secret are unset — same
// gating posture as the command poller.
//
// version and buildTime piggy-back on the config-pull request body so the
// cloud can refresh the collector's reported version even for silent
// collectors (no devices → no /ingest push). Empty strings are safe.
type Poller struct {
	cfg         config.CloudConfig
	interval    time.Duration
	collectorID string
	version     string
	buildTime   string
	hub         *hub.Hub
	http        *http.Client
	baseURL     string
	seeded      bool // first-run seed only runs once
}

func NewPoller(cloudCfg config.CloudConfig, interval time.Duration, collectorID, version, buildTime string, h *hub.Hub) *Poller {
	base := strings.TrimRight(cloudCfg.PortalAPI, "/")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cloudCfg.TLSSkipVerify},
	}
	return &Poller{
		cfg:         cloudCfg,
		interval:    interval,
		collectorID: collectorID,
		version:     version,
		buildTime:   buildTime,
		hub:         h,
		baseURL:     base,
		http:        &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// Run blocks until ctx is cancelled. Does an initial sync immediately, then
// ticks on the configured interval.
func (p *Poller) Run(ctx context.Context) {
	if p.baseURL == "" || p.cfg.HMACSecret == "" {
		slog.Warn("config puller disabled (cloud.portal_api or cloud.hmac_secret missing)")
		return
	}
	if p.interval <= 0 {
		p.interval = 5 * time.Minute
	}
	slog.Info("config puller started",
		"interval", p.interval, "base_url", p.baseURL, "collector_id", p.collectorID)

	p.syncOnce(ctx)

	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.syncOnce(ctx)
		}
	}
}

func (p *Poller) syncOnce(ctx context.Context) {
	devices, err := p.fetch(ctx)
	if err != nil {
		slog.Warn("config fetch failed", "error", err)
		return
	}

	if len(devices) == 0 && !p.seeded {
		local := p.hub.LocalDeviceConfigs()
		if len(local) > 0 {
			if err := p.seed(ctx, local); err != nil {
				slog.Warn("config seed failed", "error", err)
				return
			}
			slog.Info("config seeded to cloud from local YAML", "count", len(local))
			p.seeded = true
			// The just-seeded devices are already running locally from YAML — no
			// reconcile needed this tick. Next tick will fetch them back and the
			// diff will be a no-op.
			return
		}
		// Neither cloud nor local has devices — nothing to do.
		return
	}

	// Any non-empty cloud response is authoritative. Mark seeded so a later
	// transient-empty response doesn't re-seed and overwrite portal edits.
	if len(devices) > 0 {
		p.seeded = true
	}

	cfgs := make([]config.DeviceConfig, len(devices))
	for i, d := range devices {
		cfgs[i] = d.toConfig()
	}
	p.hub.Reconcile(cfgs)
}

func (p *Poller) fetch(ctx context.Context) ([]wireDevice, error) {
	body, _ := json.Marshal(map[string]any{
		"collector_id":      p.collectorID,
		"bridge_version":    p.version,
		"bridge_build_time": p.buildTime,
	})
	resp, err := p.signedRequest(ctx, http.MethodPost, "/bridge/config", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetch status %d: %s", resp.StatusCode, string(b))
	}
	var out getResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out.Devices, nil
}

func (p *Poller) seed(ctx context.Context, local []config.DeviceConfig) error {
	wire := make([]wireDevice, len(local))
	for i, c := range local {
		wire[i] = fromConfig(c)
	}
	body, _ := json.Marshal(putReq{CollectorID: p.collectorID, Devices: wire})
	resp, err := p.signedRequest(ctx, http.MethodPut, "/bridge/config", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// Cloud already has devices for this collector — portal edits exist.
		// Treat this as "seeded" so we stop trying.
		slog.Info("config seed skipped: cloud already has devices for this collector")
		return nil
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("seed status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (p *Poller) signedRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	mac := hmac.New(sha256.New, []byte(p.cfg.HMACSecret))
	mac.Write(body)
	req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return p.http.Do(req)
}
