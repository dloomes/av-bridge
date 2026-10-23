package hub

import (
	"context"
	"errors"
	"fmt"
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
// differ only in cosmetic fields (Name, Location, Type), which don't affect
// the connection, polling or subscriptions. Tags do: adapters read settings
// from them.
//
// A cosmetic change doesn't reach the running adapter until its next
// restart (it holds Cfg by value), so its telemetry keeps the old name; the
// cloud ignores telemetry names for existing devices, so that's harmless.
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
	// Tags are settings too (outlet_count, outlet names, tls_skip_verify,
	// port_prefix, ...): a running adapter keeps the tags it started with,
	// so it must restart to use new ones.
	if !reflect.DeepEqual(a.Tags, b.Tags) {
		return true
	}
	// A new stamp means the tags were edited (even if back to what this
	// adapter has): restart so its telemetry carries the stamp the cloud
	// now expects, or the cloud would ignore its tags from then on.
	if a.ConfigStamp != b.ConfigStamp {
		return true
	}
	return false
}

// adapterConfig is a copy of dcfg for an adapter to own. Some adapters add
// what they discover to their Tags (Sony, Aurora); with a shared map that
// would change the hub's record of the config it was given, so every
// config pull would look like a tag change and restart the device.
func adapterConfig(dcfg config.DeviceConfig) config.DeviceConfig {
	c := dcfg
	if dcfg.Tags != nil {
		c.Tags = make(map[string]string, len(dcfg.Tags))
		for k, v := range dcfg.Tags {
			c.Tags[k] = v
		}
	}
	if dcfg.Commands != nil {
		c.Commands = make(map[string]string, len(dcfg.Commands))
		for k, v := range dcfg.Commands {
			c.Commands[k] = v
		}
	}
	c.Subscriptions = append([]config.SubscriptionSpec(nil), dcfg.Subscriptions...)
	return c
}

// DeviceEntry holds a device, the config that produced it, and the cancel
// for its manage-goroutine. Cfg is kept so Reconcile can diff incoming
// configs against what's running.
type DeviceEntry struct {
	Dev    device.Device
	Cfg    config.DeviceConfig
	Cancel context.CancelFunc
	// polls carries PollNow requests to the device's own goroutine, so an
	// on-demand poll never runs alongside a scheduled one.
	polls chan pollRequest
}

// pollRequest asks a device's goroutine to poll now and reply.
type pollRequest struct {
	reply chan *device.Telemetry
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

// DeviceConfig returns a device's configuration by ID (remote access uses
// it to find the device's address).
func (h *Hub) DeviceConfig(id string) (config.DeviceConfig, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.devices[id]
	if !ok {
		return config.DeviceConfig{}, false
	}
	return e.Cfg, true
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
			// Cosmetic-only edit (Name / Location / Type). Keep the
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
	if len(toSpawn) > 0 {
		slog.Info("reconcile: started devices", "count", len(toSpawn))
	}
}

// spawnDevice creates an adapter, registers it, and starts its manage
// goroutine. Caller must NOT hold h.mu.
func (h *Hub) spawnDevice(dcfg config.DeviceConfig) {
	dev, err := adapters.New(adapterConfig(dcfg), adapters.Deps{Lens: h.lens})
	if err != nil {
		slog.Error("failed to create adapter", "device", dcfg.ID, "error", err)
		return
	}
	ctx := h.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	devCtx, cancel := context.WithCancel(ctx)
	polls := make(chan pollRequest)
	h.mu.Lock()
	h.devices[dcfg.ID] = &DeviceEntry{Dev: dev, Cfg: dcfg, Cancel: cancel, polls: polls}
	h.mu.Unlock()
	slog.Debug("reconcile: starting device", "device", dcfg.ID, "protocol", dcfg.Protocol)
	go h.manageDevice(devCtx, dev, polls)
}

// configEqual is a value-equality test on DeviceConfig. reflect.DeepEqual
// handles the nested maps/slices correctly; we don't normalise PollRate or
// other zero-defaults because Reconcile sees configs as they came from the
// cloud — defaults are applied at YAML-load time only.
func configEqual(a, b config.DeviceConfig) bool {
	return reflect.DeepEqual(a, b)
}

// manageDevice handles connect-with-retry, polling, event draining, and state persistence.
func (h *Hub) manageDevice(ctx context.Context, dev device.Device, polls <-chan pollRequest) {
	info := dev.Info()
	log := slog.With("device", info.ID, "type", info.Type, "protocol", info.Protocol)

	// Restore persisted state
	state := h.store.GetDevice(info.ID)
	if state.LastStatus != "" {
		log.Debug("restored device state", "last_status", state.LastStatus, "last_seen", state.LastSeen)
	}

	// Connect with exponential backoff. On every failed attempt push a
	// synthetic offline telemetry heartbeat so the cloud sees the
	// device as offline within seconds — otherwise this goroutine loops
	// here indefinitely and never enters the poll loop that would
	// otherwise push telemetry, leaving devices.latest_status pinned at
	// whatever was written last (commonly an old 'online' from before
	// the device moved networks / was unplugged).
	//
	// The cloud has a 15-minute staleness safety net that would also
	// flip the pill eventually; this fixes it in seconds and lets
	// operators see the true state without waiting.
	// Repeated failures are logged once, then every 15 minutes, then on
	// recovery (see failureLog).
	connects := newFailureLog("connect")
	polling := newFailureLog("poll")
	backoff := 5 * time.Second
	for {
		log.Debug("connecting to device")
		if err := dev.Connect(ctx); err != nil {
			connects.failed(log, err)
			h.cloud.EnqueueTelemetry(newOfflineHeartbeat(dev, err))
			if !waitBackoff(ctx, backoff, polls, func() *device.Telemetry { return newOfflineHeartbeat(dev, err) }) {
				return
			}
			if backoff < 2*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
		if !connects.ok(log) {
			log.Debug("device connected")
		}
		break
	}

	// PollRateFor never returns zero: a device saved without a poll rate
	// reaches us as 0, and time.NewTicker(0) panics (crashing the whole
	// collector). It falls back to the adapter's default instead.
	pollTicker := time.NewTicker(device.PollRateFor(dev))
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
			h.pollOnce(ctx, dev, log, polling)

		case req := <-polls:
			// On demand (the cloud's _poll command): a fresh reading for
			// a Room Readiness check, without waiting for the next tick.
			tel := h.pollOnce(ctx, dev, log, polling)
			if tel == nil {
				tel = newOfflineHeartbeat(dev, errors.New("poll failed"))
			}
			req.reply <- tel

		case <-heartbeat.C:
			if dev.Status() == device.StatusOffline {
				log.Debug("device offline, attempting reconnect")
				if err := dev.Connect(ctx); err != nil {
					connects.failed(log, err)
				} else if !connects.ok(log) {
					// Debug: for adapters whose Connect can't fail (HTTP),
					// an unreachable device "reconnects" every heartbeat;
					// its polls going ok again is the recovery that's logged.
					log.Debug("device reconnected")
				}
			} else if hb, ok := dev.(device.Heartbeater); ok {
				// Proactive keep-alive for stateful adapters (Poly session,
				// persistent TCP). Short deadline so a slow adapter never
				// blocks the goroutine; failures are logged but don't flip
				// the device to offline — Poll is the source of truth.
				hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := hb.Heartbeat(hbCtx); err != nil {
					log.Debug("heartbeat failed", "error", err)
				}
				cancel()
			}
		}
	}
}

// pollOnce polls the device, sends the reading to the cloud and keeps the
// local state. Returns nil if the poll failed outright.
func (h *Hub) pollOnce(ctx context.Context, dev device.Device, log *slog.Logger, failures *failureLog) *device.Telemetry {
	tel, err := dev.Poll(ctx)
	if err != nil {
		failures.failed(log, err)
		return nil
	}
	failures.ok(log)
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
	state := h.store.GetDevice(dev.Info().ID)
	state.LastStatus = string(tel.Status)
	state.LastMetrics = tel.Metrics
	if tel.Status == device.StatusOnline {
		state.LastSeen = time.Now().UTC()
	}
	_ = h.store.UpsertDevice(state)
	return tel
}

// waitBackoff waits d between connection attempts, answering on-demand
// polls with offline() meanwhile. False when ctx is cancelled.
func waitBackoff(ctx context.Context, d time.Duration, polls <-chan pollRequest, offline func() *device.Telemetry) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		case req := <-polls:
			req.reply <- offline()
		}
	}
}

// PollNow polls one device straight away, on its own goroutine, and returns
// the reading. A device still trying to connect answers offline at once.
func (h *Hub) PollNow(ctx context.Context, id string) (*device.Telemetry, error) {
	h.mu.RLock()
	entry := h.devices[id]
	h.mu.RUnlock()
	if entry == nil || entry.polls == nil {
		return nil, fmt.Errorf("device not present on this collector: %s", id)
	}
	req := pollRequest{reply: make(chan *device.Telemetry, 1)}
	select {
	case entry.polls <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case tel := <-req.reply:
		return tel, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newOfflineHeartbeat builds a synthetic telemetry blob marking the
// device as offline, used when Connect() fails during the initial
// connect-with-retry loop so the cloud can flip the pill immediately
// rather than waiting for the 15-minute staleness safety net. Fields
// mirror what the adapter's BaseTelemetry() would produce so the
// ingest path stays uniform (same shape as any other telemetry push).
func newOfflineHeartbeat(dev device.Device, cause error) *device.Telemetry {
	info := dev.Info()
	tel := &device.Telemetry{
		DeviceID:   info.ID,
		DeviceName: info.Name,
		DeviceType: info.Type,
		Location:   info.Location,
		Protocol:   info.Protocol,
		Status:     device.StatusOffline,
		Timestamp:   time.Now().UTC(),
		Tags:        info.Tags,
		ConfigStamp: info.ConfigStamp,
	}
	if cause != nil {
		tel.Error = cause.Error()
	}
	caps := dev.Capabilities()
	tel.Capabilities = &caps
	return tel
}
