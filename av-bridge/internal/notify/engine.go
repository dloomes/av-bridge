// Package notify evaluates alert rules against device state and fires
// notifications to the cloud portal when thresholds are breached.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/store"
)

// Severity levels for alerts
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Alert is a notification fired to the cloud portal
type Alert struct {
	AlertKey   string            `json:"alert_key"`
	DeviceID   string            `json:"device_id"`
	DeviceName string            `json:"device_name"`
	DeviceType string            `json:"device_type"`
	Severity   Severity          `json:"severity"`
	Message    string            `json:"message"`
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

// RuleConfig defines a single alert rule from config
type RuleConfig struct {
	// OfflineAfter fires a critical alert if a device has been offline longer than this
	OfflineAfter time.Duration `yaml:"offline_after"`
	// DegradedAfter fires a warning if a device has been degraded longer than this
	DegradedAfter time.Duration `yaml:"degraded_after"`
	// RepeatInterval prevents alert spam — same alert won't fire again until this elapses
	RepeatInterval time.Duration `yaml:"repeat_interval"`
}

// Engine evaluates alert rules on a ticker and fires alerts via the cloud client.
type Engine struct {
	rules       RuleConfig
	store       *store.Store
	cloud       *cloud.Client
	hub         DeviceSource
	broadcaster EventBroadcaster
	ticker      *time.Ticker
}

// DeviceSource is satisfied by *hub.Hub — avoids an import cycle.
type DeviceSource interface {
	Devices() []device.Device
}

// EventBroadcaster pushes alert events to live WebSocket subscribers. nil-safe:
// pass nil in tests or when no WS fan-out is wired.
type EventBroadcaster interface {
	BroadcastEvent(e *device.Event)
}

func New(rules RuleConfig, st *store.Store, cloudClient *cloud.Client, hub DeviceSource, broadcaster EventBroadcaster) *Engine {
	if rules.OfflineAfter == 0 {
		rules.OfflineAfter = 5 * time.Minute
	}
	if rules.DegradedAfter == 0 {
		rules.DegradedAfter = 2 * time.Minute
	}
	if rules.RepeatInterval == 0 {
		rules.RepeatInterval = 30 * time.Minute
	}
	return &Engine{
		rules:       rules,
		store:       st,
		cloud:       cloudClient,
		hub:         hub,
		broadcaster: broadcaster,
	}
}

// Run starts the evaluation loop. Blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		interval = 60 * time.Second
	}
	e.ticker = time.NewTicker(interval)
	defer e.ticker.Stop()

	slog.Info("alert engine started", "eval_interval", interval,
		"offline_after", e.rules.OfflineAfter,
		"repeat_interval", e.rules.RepeatInterval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.ticker.C:
			e.evaluate(ctx)
		}
	}
}

func (e *Engine) evaluate(ctx context.Context) {
	devs := e.hub.Devices()
	for _, d := range devs {
		info := d.Info()
		state := e.store.GetDevice(info.ID)

		switch d.Status() {
		case device.StatusOffline:
			// Update last seen in store
			e.maybeUpdateLastSeen(state, d)

			if state.LastSeen.IsZero() {
				continue
			}
			offlineDuration := time.Since(state.LastSeen)
			if offlineDuration >= e.rules.OfflineAfter {
				e.maybeFireAlert(ctx, Alert{
					AlertKey:   "device_offline",
					DeviceID:   info.ID,
					DeviceName: info.Name,
					DeviceType: info.Type,
					Severity:   SeverityCritical,
					Message:    fmt.Sprintf("Device %q has been offline for %s", info.Name, offlineDuration.Round(time.Second)),
					Timestamp:  time.Now().UTC(),
					Metadata: map[string]any{
						"offline_duration_seconds": offlineDuration.Seconds(),
						"last_seen":                state.LastSeen.Format(time.RFC3339),
						"location":                 info.Location,
					},
				})
			}

		case device.StatusDegraded:
			e.maybeUpdateLastSeen(state, d)
			degradedSince := time.Since(state.LastSeen)
			if degradedSince >= e.rules.DegradedAfter {
				e.maybeFireAlert(ctx, Alert{
					AlertKey:   "device_degraded",
					DeviceID:   info.ID,
					DeviceName: info.Name,
					DeviceType: info.Type,
					Severity:   SeverityWarning,
					Message:    fmt.Sprintf("Device %q has been in degraded state for %s", info.Name, degradedSince.Round(time.Second)),
					Timestamp:  time.Now().UTC(),
					Metadata: map[string]any{
						"degraded_duration_seconds": degradedSince.Seconds(),
						"location":                  info.Location,
					},
				})
			}

		case device.StatusOnline:
			// Device came back online — fire a recovery alert if it was previously offline
			if state.LastStatus == string(device.StatusOffline) || state.LastStatus == string(device.StatusDegraded) {
				e.maybeFireAlert(ctx, Alert{
					AlertKey:   "device_recovered",
					DeviceID:   info.ID,
					DeviceName: info.Name,
					DeviceType: info.Type,
					Severity:   SeverityInfo,
					Message:    fmt.Sprintf("Device %q has recovered and is back online", info.Name),
					Timestamp:  time.Now().UTC(),
					Metadata:   map[string]any{"location": info.Location},
				})
			}
			e.maybeUpdateLastSeen(state, d)
		}

		// Persist updated status
		state.LastStatus = string(d.Status())
		_ = e.store.UpsertDevice(state)
	}
}

// maybeFireAlert fires an alert only if the repeat interval has elapsed since the last send.
func (e *Engine) maybeFireAlert(ctx context.Context, alert Alert) {
	lastSent := e.store.LastAlertSent(alert.DeviceID, alert.AlertKey)
	if !lastSent.IsZero() && time.Since(lastSent) < e.rules.RepeatInterval {
		slog.Debug("suppressing repeat alert",
			"device", alert.DeviceID,
			"alert", alert.AlertKey,
			"next_in", e.rules.RepeatInterval-time.Since(lastSent))
		return
	}

	b, _ := json.Marshal(alert)
	event := &device.Event{
		DeviceID:   alert.DeviceID,
		DeviceName: alert.DeviceName,
		DeviceType: alert.DeviceType,
		EventType:  "alert:" + alert.AlertKey,
		Payload: map[string]any{
			"severity": string(alert.Severity),
			"message":  alert.Message,
			"metadata": alert.Metadata,
			"raw":      string(b),
		},
		Timestamp: alert.Timestamp,
	}

	e.cloud.EnqueueEvent(event)
	if e.broadcaster != nil {
		e.broadcaster.BroadcastEvent(event)
	}
	_ = e.store.MarkAlertSent(alert.DeviceID, alert.AlertKey)

	slog.Warn("alert fired",
		"device", alert.DeviceID,
		"alert", alert.AlertKey,
		"severity", alert.Severity,
		"message", alert.Message)
}

func (e *Engine) maybeUpdateLastSeen(state *store.DeviceState, d device.Device) {
	if d.Status() == device.StatusOnline {
		state.LastSeen = time.Now().UTC()
	}
}
