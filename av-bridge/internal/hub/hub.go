package hub

import (
	"context"
	"log/slog"
	"reflect"
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

// adapterRelevantChange reports whether the difference between a and b would
// require a restart of the running adapter. Returns false when the configs
// differ only in cosmetic fields (Name, Location, Type, Tags) — those don't
// affect the connection, polling cadence, or pre-registered subscriptions.
//
// Limitation: cosmetic field updates don't propagate to telemetry until the
// next real restart, since the adapter caches Cfg by value. A separate live-
// update path on device.Base would be needed if/when that matters.
func adapterRelevantChange(a, b config.DeviceConfig) bool {
	if a.Protocol != b.Protocol ||
		a.Address != b.Address ||
		a.BaudRate != b.BaudRate ||
		a.Username != b.Username ||
		a.Password != b.Password ||
		a.PollRate != b.PollRate {
		return true
	}
	if !reflect.DeepEqual(a.Commands, b.Commands) {
		return true
	}
	if !reflect.DeepEqual(a.Subscriptions, b.Subscriptions) {
		return true
	}
	return false
}

// DeviceEntry holds a device, the config that produced it, and the cancel
// for its manage-goroutine. Cfg is kept so Reconcile can diff incoming
// configs against what's running.
type DeviceEntry struct {
	Dev    device.Device
	Cfg    config.DeviceConfig
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
	// runCtx is the lifetime context captured in Start. New device goroutines
	// spawned by Reconcile derive from this, not from any tick-scoped context,
	// so they outlive the puller's call.
	runCtx context.Context
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
	h.runCtx = ctx
	go h.cloud.Run(ctx)
	go h.alerts.Run(ctx, 60*time.Second)

	for _, dcfg := range h.cfg.Devices {
		h.spawnDevice(dcfg)
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

// LocalDeviceConfigs returns the configs of every device currently running.
// Used by the config puller to seed the cloud on first run.
func (h *Hub) LocalDeviceConfigs() []config.DeviceConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]config.DeviceConfig, 0, len(h.devices))
	for _, e := range h.devices {
		out = append(out, e.Cfg)
	}
	return out
}

// Reconcile drives the running device set toward the desired list. Devices
// missing from want are stopped; devices present in both with the same config
// are left alone; devices whose config differs are torn down and respawned;
// new devices are spawned. Idempotent — calling with the current set is a
// no-op.
func (h *Hub) Reconcile(want []config.DeviceConfig) {
	wantByID := make(map[string]config.DeviceConfig, len(want))
	for _, d := range want {
		wantByID[d.ID] = d
	}

	h.mu.Lock()
	for id, entry := range h.devices {
		wanted, kept := wantByID[id]
		if !kept {
			slog.Info("reconcile: removing device", "device", id)
			entry.Cancel()
			_ = entry.Dev.Disconnect()
			delete(h.devices, id)
			continue
		}
		if configEqual(entry.Cfg, wanted) {
			continue
		}
		if !adapterRelevantChange(entry.Cfg, wanted) {
			// Cosmetic-only edit (Name / Location / Type / Tags). Keep the
			// running adapter — don't drop a live session for a label change.
			// We still refresh entry.Cfg so the next reconcile compares
			// against the desired state, not against a fossilised earlier
			// snapshot.
			slog.Info("reconcile: cosmetic-only change, keeping device running", "device", id)
			entry.Cfg = wanted
			continue
		}
		slog.Info("reconcile: restarting device with updated config", "device", id)
		entry.Cancel()
		_ = entry.Dev.Disconnect()
		delete(h.devices, id)
		// Fall through to the add-loop below — same id is no longer in
		// h.devices, so it gets spawned with the updated config.
	}
	var toSpawn []config.DeviceConfig
	for id, dcfg := range wantByID {
		if _, exists := h.devices[id]; exists {
			continue
		}
		toSpawn = append(toSpawn, dcfg)
	}
	h.mu.Unlock()

	for _, dcfg := range toSpawn {
		h.spawnDevice(dcfg)
	}
}

// spawnDevice creates an adapter, registers it, and starts its manage
// goroutine. Caller must NOT hold h.mu.
func (h *Hub) spawnDevice(dcfg config.DeviceConfig) {
	dev, err := adapters.New(dcfg, adapters.Deps{Lens: h.lens})
	if err != nil {
		slog.Error("failed to create adapter", "device", dcfg.ID, "error", err)
		return
	}
	ctx := h.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	devCtx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.devices[dcfg.ID] = &DeviceEntry{Dev: dev, Cfg: dcfg, Cancel: cancel}
	h.mu.Unlock()
	slog.Info("reconcile: starting device", "device", dcfg.ID, "protocol", dcfg.Protocol)
	go h.manageDevice(devCtx, dev)
}

// configEqual is a value-equality test on DeviceConfig. reflect.DeepEqual
// handles the nested maps/slices correctly; we don't normalise PollRate or
// other zero-defaults because Reconcile sees configs as they came from the
// cloud — defaults are applied at YAML-load time only.
func configEqual(a, b config.DeviceConfig) bool {
	return reflect.DeepEqual(a, b)
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
			// Attach static capabilities from the adapter — done here
			// (once per poll) rather than in every adapter's Poll so
			// each vendor implementation stays focused on state, not
			// declaration. Cloud writes this to devices.capabilities
			// on ingest; the portal routine builder + executor read
			// it to know what actions the device can accept.
			if tel.Capabilities == nil {
				caps := dev.Capabilities()
				tel.Capabilities = &caps
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
