// Package admin exposes operator-only management endpoints. Today: collector
// registration. The bearer-token gate is a placeholder for proper Customer
// Admin auth once the portal lands.
package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/registration"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CollectorHandler struct {
	admin  *pgxpool.Pool
	cipher secrets.Cipher
	token  string
	log    *slog.Logger
}

func NewCollectorHandler(adminPool *pgxpool.Pool, cipher secrets.Cipher, token string, log *slog.Logger) *CollectorHandler {
	return &CollectorHandler{admin: adminPool, cipher: cipher, token: token, log: log}
}

func (h *CollectorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authed(r) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req registration.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	res, err := registration.Register(r.Context(), h.admin, h.cipher, req)
	if err != nil {
		switch {
		case errors.Is(err, registration.ErrCustomerUnknown):
			writeErr(w, http.StatusBadRequest, "unknown customer_id")
		case errors.Is(err, registration.ErrAlreadyExists):
			writeErr(w, http.StatusConflict, "bridge_collector_id already registered")
		default:
			h.log.Error("collector registration failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	h.log.Info("collector registered",
		"id", res.ID, "bridge_collector_id", res.BridgeCollectorID, "customer_id", req.CustomerID)
	writeJSON(w, http.StatusCreated, res)
}

func (h *CollectorHandler) authed(r *http.Request) bool {
	if h.token == "" {
		// Refuse to operate without a configured admin token rather than open up.
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(h.token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
