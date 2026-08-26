package pubapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

// Handler owns the /pub/v1 read endpoints. Every handler composes
// inside Resolver.Middleware + its scope gate; the DB scope for
// tenant-bound reads runs through store.WithTenant just like portalapi.
type Handler struct {
	store *db.Store
	log   *slog.Logger
}

func New(store *db.Store, log *slog.Logger) *Handler {
	return &Handler{store: store, log: log}
}

// withTenant runs fn under the caller's customer scope. Mirrors the
// portalapi helper but reads a pubapi.Principal from context instead
// of a portalauth.Principal — the two auth surfaces don't share a
// Principal type on purpose.
//
// A missing principal in ctx is a wiring bug (a handler mounted
// outside Middleware): 500 rather than a silent tenant leak. An RLS
// query failure is also 500 with the underlying error logged.
func (h *Handler) withTenant(w http.ResponseWriter, r *http.Request, fn func(context.Context, pgx.Tx) error) bool {
	p, ok := From(r.Context())
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no principal in context")
		return false
	}
	err := h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return fn(r.Context(), tx)
	})
	if err != nil {
		h.log.Error("pubapi query failed", "path", r.URL.Path, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return false
	}
	return true
}

// Ping — GET /pub/v1/ping
//
// Public-API smoke test. Returns the authenticated token's tenant id
// and its name so an integrator can confirm they've wired the right
// key against the right tenant. No scope gate — any valid token
// authenticates against Ping (it discloses only what the token
// implicitly already knows about itself).
func (h *Handler) Ping(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no principal in context")
		return
	}
	scopes := make([]string, 0, len(p.Scopes))
	for s := range p.Scopes {
		scopes = append(scopes, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":  p.CustomerID,
		"token_name": p.TokenName,
		"scopes":     scopes,
		"time":       time.Now().UTC().Format(time.RFC3339),
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
