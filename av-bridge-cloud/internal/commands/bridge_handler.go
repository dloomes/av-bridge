package commands

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
	"github.com/dloomes/av-bridge-cloud/internal/collectorha"
	"github.com/dloomes/av-bridge-cloud/internal/collectorupdate"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

// BridgeHandler exposes the two endpoints the bridge calls during its outbound
// command loop:
//
//	POST /bridge/poll                          → claim up to N pending commands
//	POST /bridge/commands/{id}/result          → post the terminal result back
//
// Authentication delegates to bridgeauth so every bridge-facing endpoint
// (poll, result, config-pull) shares the same HMAC posture.
type BridgeHandler struct {
	store   *db.Store
	auth    *bridgeauth.Authenticator
	log     *slog.Logger
	maxHold time.Duration
	updates *collectorupdate.Service // nil: no self-update offers
	ha      *collectorha.Manager     // nil: every collector serves itself
}

// SetHA enables collector groups (warm standby) on /bridge/poll.
func (h *BridgeHandler) SetHA(mgr *collectorha.Manager) {
	h.ha = mgr
}

// SetUpdates enables collector self-update offers on /bridge/poll and the
// /bridge/update-status report endpoint.
func (h *BridgeHandler) SetUpdates(svc *collectorupdate.Service) {
	h.updates = svc
}

// DefaultBridgePollMaxHold is how long /bridge/poll may block waiting for a
// cmd_pending NOTIFY before returning an empty response. Kept well under the
// ALB idle default (60s) so a healthy long-poll never trips it.
const DefaultBridgePollMaxHold = 25 * time.Second

func NewBridgeHandler(store *db.Store, cipher secrets.Cipher, maxHold time.Duration, log *slog.Logger) *BridgeHandler {
	if maxHold <= 0 {
		maxHold = DefaultBridgePollMaxHold
	}
	return &BridgeHandler{
		store:   store,
		auth:    bridgeauth.New(store, cipher, log),
		log:     log,
		maxHold: maxHold,
	}
}

type pollReq struct {
	CollectorID string `json:"collector_id"`
	Max         int    `json:"max"`
	// Self-description from bridges that have the updater.
	collectorupdate.PollMeta
}

type pollResp struct {
	Commands []Command `json:"commands"`
	// Resync tells the bridge its device config changed since its last
	// /bridge/config pull (a device was added, edited, deleted or moved),
	// so it should pull now rather than on its next slow tick. Older
	// bridges ignore the field and catch up on that tick.
	Resync bool `json:"resync,omitempty"`
	// Update offers a new collector version (see collectorupdate).
	Update *collectorupdate.Instruction `json:"update,omitempty"`
	// Role is "active" or "standby" for a machine in a collector group
	// (warm standby); omitted for a collector on its own. A standby holds
	// no devices; an active machine must stop polling devices if it hasn't
	// had a successful poll within LeaseTTLSeconds minus a safety margin.
	Role            string `json:"role,omitempty"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds,omitempty"`
}

// Poll is a long-poll: try to claim ready commands immediately; if none,
// LISTEN cmd_pending for up to maxHold and re-claim on wake. Held requests
// finish in well under the ALB idle timeout even in the worst case.
//
// LISTEN + re-claim order matters. Opening the listener BEFORE the
// re-check closes the missed-notify window: any NOTIFY that fires between
// the initial (fast-path) claim and the wait is buffered by pgx and
// returned by the next Wait call.
//
// Collector groups (warm standby, see collectorha): the poll takes or
// renews the group lease first. Only the lease holder claims commands —
// for the group, i.e. the primary's id — and the response tells the
// bridge its role so a standby holds no devices. The lease is renewed
// again at the end of the hold, which also picks up a failover or
// handover that happened while the poll was held.
func (h *BridgeHandler) Poll(w http.ResponseWriter, r *http.Request) {
	body, col, ok := h.auth.Authenticate(w, r)
	if !ok {
		return
	}
	var req pollReq
	if err := json.Unmarshal(body, &req); err != nil {
		bridgeauth.WriteErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	max := req.Max
	if max <= 0 {
		max = 10
	}
	if max > 100 {
		max = 100
	}
	ctx := r.Context()

	lease, err := h.lease(ctx, *col)
	if err != nil {
		// No role rather than a guessed one: the bridge treats a failed
		// poll as "no news", and a holder that can't renew fences itself.
		h.log.Error("collector lease failed", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Fast path — anything ready right now, ship it and skip the LISTEN.
	if lease.Active {
		claimed, err := h.claim(ctx, col.CustomerID, lease.GroupID, max)
		if err != nil {
			h.log.Error("claim commands failed", "collector", col.ID, "error", err)
			bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if len(claimed) > 0 {
			h.respond(w, r, *col, req, lease, claimed)
			return
		}
	}

	// Slow path — no work right now. Open the listener FIRST, then
	// re-check to close the race, then block up to maxHold.
	listener, err := h.store.Listen(ctx, ChannelPending)
	if err != nil {
		// Degrade to an empty response rather than 500 — the bridge
		// re-polls immediately and the queue advances on the next tick.
		h.log.Warn("listen cmd_pending failed", "collector", col.ID, "error", err)
		h.respond(w, r, *col, req, lease, []Command{})
		return
	}
	defer listener.Close()

	// Race-closer re-check.
	if lease.Active {
		claimed, err := h.claim(ctx, col.CustomerID, lease.GroupID, max)
		if err != nil {
			h.log.Error("claim commands failed (post-listen)", "collector", col.ID, "error", err)
			bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if len(claimed) > 0 {
			h.respond(w, r, *col, req, lease, claimed)
			return
		}
	}

	// Wake on this machine's id (config changes, updates) or the group's
	// (commands for the devices it may be serving).
	if err := WaitForPending(ctx, listener, h.maxHold, col.ID, lease.GroupID); err != nil {
		// Client cancel or listener death — return empty. The bridge
		// treats an empty 200 as "nothing to do" and re-polls immediately.
		h.log.Debug("wait for pending returned early", "collector", col.ID, "error", err)
		h.respond(w, r, *col, req, lease, []Command{})
		return
	}

	// Woken (or timed out): renew the lease, then a final claim attempt.
	// May still return empty if a competing cloud task claimed the same
	// batch first; the bridge re-polls immediately regardless.
	lease, err = h.lease(ctx, *col)
	if err != nil {
		h.log.Error("collector lease failed (post-wait)", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	claimed := []Command{}
	if lease.Active {
		claimed, err = h.claim(ctx, col.CustomerID, lease.GroupID, max)
		if err != nil {
			h.log.Error("claim commands failed (post-wait)", "collector", col.ID, "error", err)
			bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if claimed == nil {
			claimed = []Command{}
		}
	}
	h.respond(w, r, *col, req, lease, claimed)
}

// lease returns the machine's standing in its collector group. Without a
// lease manager wired (tests, pre-HA) every collector serves itself.
func (h *BridgeHandler) lease(ctx context.Context, col db.Collector) (collectorha.Lease, error) {
	if h.ha == nil {
		return collectorha.Lease{Active: true, GroupID: col.GroupID()}, nil
	}
	return h.ha.Acquire(ctx, col)
}

// respond writes the poll response, flagging resync when the collector's
// device config is newer than what its bridge last pulled. The device
// trigger NOTIFYs cmd_pending on config changes, so a held poll wakes and
// carries the flag back within a second of the change. A lookup failure
// only costs the hint, never the commands.
func (h *BridgeHandler) respond(w http.ResponseWriter, r *http.Request, col db.Collector, req pollReq, lease collectorha.Lease, cmds []Command) {
	resync, err := h.store.CollectorNeedsResync(r.Context(), col.ID)
	if err != nil {
		h.log.Debug("resync check failed", "collector", col.ID, "error", err)
	}
	var upd *collectorupdate.Instruction
	if h.updates != nil {
		upd = h.updates.Offer(r.Context(), col.ID, req.PollMeta)
	}
	resp := pollResp{Commands: cmds, Resync: resync, Update: upd, Role: lease.Role()}
	if lease.Grouped {
		resp.LeaseTTLSeconds = int(collectorha.TTL.Seconds())
	}
	bridgeauth.WriteJSON(w, http.StatusOK, resp)
}

type updateStatusReq struct {
	CollectorID string `json:"collector_id"`
	State       string `json:"state"`
	Version     string `json:"version"`
	Message     string `json:"message"`
}

// PostUpdateStatus — POST /bridge/update-status. The collector reports
// how a self-update went: restarting, succeeded, failed or rolled_back.
func (h *BridgeHandler) PostUpdateStatus(w http.ResponseWriter, r *http.Request) {
	body, col, ok := h.auth.Authenticate(w, r)
	if !ok {
		return
	}
	if h.updates == nil {
		bridgeauth.WriteErr(w, http.StatusNotFound, "updates not enabled")
		return
	}
	var req updateStatusReq
	if err := json.Unmarshal(body, &req); err != nil || req.State == "" || req.Version == "" {
		bridgeauth.WriteErr(w, http.StatusBadRequest, "state and version are required")
		return
	}
	if err := h.updates.Report(r.Context(), col.ID, req.State, req.Version, req.Message); err != nil {
		bridgeauth.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.log.Info("collector update status", "collector", col.ID,
		"state", req.State, "version", req.Version, "message", req.Message)
	w.WriteHeader(http.StatusNoContent)
}

// claim is a thin helper: run ClaimPending inside a per-tenant tx.
func (h *BridgeHandler) claim(ctx context.Context, customerID, collectorID string, max int) ([]Command, error) {
	var out []Command
	err := h.store.WithTenant(ctx, customerID, func(tx pgx.Tx) error {
		var e error
		out, e = ClaimPending(ctx, tx, collectorID, max)
		return e
	})
	return out, err
}

type resultReq struct {
	CollectorID string          `json:"collector_id"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (h *BridgeHandler) PostResult(w http.ResponseWriter, r *http.Request) {
	commandID := r.PathValue("id")
	if commandID == "" {
		bridgeauth.WriteErr(w, http.StatusBadRequest, "command id required in path")
		return
	}
	body, col, ok := h.auth.Authenticate(w, r)
	if !ok {
		return
	}
	var req resultReq
	if err := json.Unmarshal(body, &req); err != nil {
		bridgeauth.WriteErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	err := h.store.WithTenant(r.Context(), col.CustomerID, func(tx pgx.Tx) error {
		// Commands belong to the group (the primary's id); a standby that
		// claimed one while active completes it under that id.
		return Complete(r.Context(), tx, commandID, col.GroupID(), req.Result, req.Error)
	})
	if err != nil {
		h.log.Warn("command complete rejected", "command_id", commandID, "collector", col.ID, "error", err)
		bridgeauth.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
