// Package ha is the collector's side of warm-standby groups. The cloud
// tells each machine of a group, on every /bridge/poll, whether it holds
// the group lease ("active") or not ("standby"). This controller:
//
//   - drops every device the moment the machine is told it's standby;
//   - asks for a config pull when it becomes active (the cloud only gives
//     device lists to the active machine);
//   - fences: if the active machine hasn't had a successful poll for the
//     lease TTL minus SafetyMargin, it stops polling all devices, because
//     the cloud may hand the lease to a standby once the TTL passes.
//
// Fencing is what keeps two machines from ever polling the same devices,
// even when the active one is cut off from the cloud and can't be told.
// A collector that isn't in a group never sees a role and is unaffected.
package ha

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
)

// SafetyMargin is how long before its lease could expire the active
// machine stops polling. Covers the gap between the cloud granting the
// lease and the bridge receiving the response, plus clock-free slack.
const SafetyMargin = 20 * time.Second

// Reconciler is the hub's device set control (hub.Hub satisfies it).
type Reconciler interface {
	Reconcile(want []config.DeviceConfig)
}

type Controller struct {
	hub     Reconciler
	trigger func() // ask the config puller for an immediate pull
	now     func() time.Time

	mu       sync.Mutex
	grouped  bool
	active   bool
	fenced   bool
	deadline time.Time
}

func New(hub Reconciler, trigger func()) *Controller {
	return &Controller{hub: hub, trigger: trigger, now: time.Now}
}

// Observe is called after every successful poll with the role the cloud
// sent ("" when the collector isn't in a group) and the lease TTL.
func (c *Controller) Observe(role string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch role {
	case "":
		if c.grouped {
			slog.Info("collector is no longer in a standby group")
		}
		wasStopped := c.grouped && !c.active
		c.grouped, c.active, c.fenced = false, true, false
		if wasStopped && c.trigger != nil {
			c.trigger()
		}
	case "standby":
		if !c.grouped || c.active {
			slog.Info("collector is on standby: not polling devices")
			c.hub.Reconcile(nil)
		}
		c.grouped, c.active, c.fenced = true, false, false
	case "active":
		if ttl <= SafetyMargin {
			ttl = SafetyMargin + time.Second
		}
		c.deadline = c.now().Add(ttl - SafetyMargin)
		if !c.grouped || !c.active {
			slog.Info("collector is the active machine of its group", "was_fenced", c.fenced)
			if c.trigger != nil {
				c.trigger()
			}
		}
		c.grouped, c.active, c.fenced = true, true, false
	}
}

// Serving reports whether device lists from the cloud may be applied.
// False only while a grouped machine is standby or fenced — it stops a
// config pull that was already in flight from undoing a fence.
func (c *Controller) Serving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.grouped || c.active
}

// Check fences when the active machine's lease can no longer be trusted.
// Exported for tests; Run calls it on a short tick.
func (c *Controller) Check() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.grouped || !c.active || c.now().Before(c.deadline) {
		return
	}
	slog.Error("lost contact with the cloud: stopped polling devices so the standby can take over safely")
	c.hub.Reconcile(nil)
	c.active, c.fenced = false, true
}

// Run checks the fence every couple of seconds until ctx ends.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Check()
		}
	}
}
