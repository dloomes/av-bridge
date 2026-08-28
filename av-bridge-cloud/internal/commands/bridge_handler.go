package commands

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
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
}

type pollResp struct {
	Commands []Command `json:"commands"`
}

// Poll is a long-poll: try to claim ready commands immediately; if none,
// LISTEN cmd_pending for up to maxHold and re-claim on wake. Held requests
// finish in well under the ALB idle timeout even in the worst case.
//
// LISTEN + re-claim order matters. Opening the listener BEFORE the
// re-check closes the missed-notify window: any NOTIFY that fires between
// the initial (fast-path) claim and the wait is buffered by pgx and
// returned by the next Wait call.
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

	// Fast path — anything ready right now, ship it and skip the LISTEN.
	claimed, err := h.claim(r.Context(), col.CustomerID, col.ID, max)
	if err != nil {
		h.log.Error("claim commands failed", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(claimed) > 0 {
		bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: claimed})
		return
	}

	// Slow path — no work right now. Open the listener FIRST, then
	// re-check to close the race, then block up to maxHold.
	listener, err := h.store.Listen(r.Context(), ChannelPending)
	if err != nil {
		// Degrade to an empty response rather than 500 — the bridge
		// re-polls immediately and the queue advances on the next tick.
		h.log.Warn("listen cmd_pending failed", "collector", col.ID, "error", err)
		bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: []Command{}})
		return
	}
	defer listener.Close()

	// Race-closer re-check.
	claimed, err = h.claim(r.Context(), col.CustomerID, col.ID, max)
	if err != nil {
		h.log.Error("claim commands failed (post-listen)", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(claimed) > 0 {
		bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: claimed})
		return
	}

	if err := WaitForPending(r.Context(), listener, col.ID, h.maxHold); err != nil {
		// Client cancel or listener death — return empty. The bridge
		// treats an empty 200 as "nothing to do" and re-polls immediately.
		h.log.Debug("wait for pending returned early", "collector", col.ID, "error", err)
		bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: []Command{}})
		return
	}

	// Woken (or timed out) — final claim attempt. May still return empty
	// if a competing cloud task claimed the same batch first; the bridge
	// re-polls immediately regardless.
	claimed, err = h.claim(r.Context(), col.CustomerID, col.ID, max)
	if err != nil {
		h.log.Error("claim commands failed (post-wait)", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if claimed == nil {
		claimed = []Command{}
	}
	bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: claimed})
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
		return Complete(r.Context(), tx, commandID, col.ID, req.Result, req.Error)
	})
	if err != nil {
		h.log.Warn("command complete rejected", "command_id", commandID, "collector", col.ID, "error", err)
		bridgeauth.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
