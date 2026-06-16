package commands

import (
	"encoding/json"
	"log/slog"
	"net/http"

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
	store *db.Store
	auth  *bridgeauth.Authenticator
	log   *slog.Logger
}

func NewBridgeHandler(store *db.Store, cipher secrets.Cipher, log *slog.Logger) *BridgeHandler {
	return &BridgeHandler{
		store: store,
		auth:  bridgeauth.New(store, cipher, log),
		log:   log,
	}
}

type pollReq struct {
	CollectorID string `json:"collector_id"`
	Max         int    `json:"max"`
}

type pollResp struct {
	Commands []Command `json:"commands"`
}

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

	var claimed []Command
	err := h.store.WithTenant(r.Context(), col.CustomerID, func(tx pgx.Tx) error {
		var e error
		claimed, e = ClaimPending(r.Context(), tx, col.ID, max)
		return e
	})
	if err != nil {
		h.log.Error("claim commands failed", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if claimed == nil {
		claimed = []Command{}
	}
	bridgeauth.WriteJSON(w, http.StatusOK, pollResp{Commands: claimed})
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
