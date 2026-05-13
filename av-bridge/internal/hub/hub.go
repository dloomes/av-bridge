package hub

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/cloud/lens"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/device/adapters"
	"github.com/dloomes/av-bridge/internal/notify"
	"github.com/dloomes/av-bridge/internal/store"
)

// DeviceEntry holds a device and its managed goroutine cancel
type DeviceEntry struct {
	Dev    device.Device
	Cancel context.CancelFunc
}

// EventBroadcaster is satisfied by *api.Server (and by the notify engine via
// the hub) and lets us push device events out to WebSocket subscribers without
// the hub importing the api package.
type EventBroadcaster interface {
	BroadcastEvent(e *device.Event)
}

// Hub is the central coordinator. It manages device lifecycles,
// polls devices, drains async events, persists state, and fires alerts.
type Hub struct {
	cfg         *config.Config
	cloud       *cloud.Client
	lens        *lens.Client
	store       *store.Store
	alerts      *notify.Engine
	devices     map[string]*DeviceEntry
	mu          sync.RWMutex
	broadcaster EventBroadcaster
}

func New(cfg *config.Config, cloudClient *cloud.Client, lensClient *lens.Client, st *store.Store) *Hub {
	alertRules := notify.RuleConfig{
		OfflineAfter:   cfg.Alerts.OfflineAfter,
		DegradedAfter:  cfg.Alerts.DegradedAfter,
		RepeatInterval: cfg.Alerts.RepeatInterval,
	}
	h := &Hub{
		cfg:     cfg,
		cloud:   cloudClient,
		lens:    lensClient,
		store:   st,
		devices: map[string]*DeviceEntry{},
	}
	h.alerts = notify.New(alertRules, st, cloudClient, h, h)
	return h
}

// SetEventBroadcaster wires up the destination for live WebSocket fan-out.
// Set this after the api server is constructed and before Start runs.
func (h *Hub) SetEventBroadcaster(b EventBroadcaster) {
	h.broadcaster = b
}

// BroadcastEvent forwards a device event to the registered broadcaster, if any.
// Safe to call when no broadcaster is wired.
func (h *Hub) BroadcastEvent(e *device.Event) {
	if h.broadcaster != nil {
		h.broadcaster.BroadcastEvent(e)
	}
}

// Start initialises all devices and begins polling/event loops.
func (h *Hub) Start(ctx context.Context) error {
	go h.cloud.Run(ctx)
	go h.alerts.Run(ctx, 60*time.Second)

	for _, dcfg := range h.cfg.Devices {
		dev, err := adapters.New(dcfg, adapters.Deps{Lens: h.lens})
		if err != nil {
			slog.Error("failed to create adapter", "device", dcfg.ID, "error", err)
			continue
		}

		devCtx, cancel := context.WithCancel(ctx)
		h.mu.Lock()
		h.devices[dcfg.ID] = &DeviceEntry{Dev: dev, Cancel: cancel}
		h.mu.Unlock()

		go h.manageDevice(devCtx, dev)
	}

	return nil
}

// Stop disconnects all devices cleanly.
func (h *Hub) Stop() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, entry := range h.devices {
		entry.Cancel()
		_ = entry.Dev.Disconnect()
	}
}

// GetDevice returns a device by ID, or nil if not found.
func (h *Hub) GetDevice(id string) device.Device {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if e, ok := h.devices[id]; ok {
		return e.Dev
	}
	return nil
}

// Devices returns a snapshot of all registered devices.
func (h *Hub) Devices() []device.Device {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]device.Device, 0, len(h.devices))
	for _, e := range h.devices {
		out = append(out, e.Dev)
	}
	return out
}

// manageDevice handles connect-with-retry, polling, event draining, and state persistence.
func (h *Hub) manageDevice(ctx context.Context, dev device.Device) {
	info := dev.Info()
	log := slog.With("device", info.ID, "type", info.Type, "protocol", info.Protocol)

	// Restore persisted state
	state := h.store.GetDevice(info.ID)
	if state.LastStatus != "" {
		log.Info("restored device state", "last_status", state.LastStatus, "last_seen", state.LastSeen)
	}

	// Connect with exponential backoff
	backoff := 5 * time.Second
	for {
		log.Info("connecting to device")
		if err := dev.Connect(ctx); err != nil {
			log.Warn("connection failed, retrying", "error", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				if backoff < 2*time.Minute {
					backoff *= 2
				}
				continue
			}
		}
		backoff = 5 * time.Second
		log.Info("device connected")
		break
	}

	pollTicker := time.NewTicker(info.PollRate)
	defer pollTicker.Stop()

	heartbeat := time.NewTicker(h.cfg.Hub.HeartbeatPeriod)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("device context cancelled, disconnecting")
			return

		case e := <-dev.Events():
			h.cloud.EnqueueEvent(e)
			h.BroadcastEvent(e)
			log.Debug("event received", "type", e.EventType)

		case <-pollTicker.C:
			tel, err := dev.Poll(ctx)
			if err != nil {
				log.Warn("poll error", "error", err)
				continue
			}
			h.cloud.EnqueueTelemetry(tel)
			log.Debug("polled device", "status", tel.Status)

			// Persist updated state
			state = h.store.GetDevice(info.ID)
			state.LastStatus = string(tel.Status)
			state.LastMetrics = tel.Metrics
			if tel.Status == device.StatusOnline {
				state.LastSeen = time.Now().UTC()
			}
			_ = h.store.UpsertDevice(state)

		case <-heartbeat.C:
			if dev.Status() == device.StatusOffline {
				log.Info("device offline, attempting reconnect")
				if err := dev.Connect(ctx); err != nil {
					log.Warn("reconnect failed", "error", err)
				} else {
					log.Info("device reconnected")
				}
			}
		}
	}
}
