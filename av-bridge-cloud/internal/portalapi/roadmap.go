package portalapi

import (
	"context"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
)

// Roadmap - GET /api/v1/roadmap
//
// The product roadmap is Involve's own content, global rather than
// per-customer, so every signed-in principal may read it, through the admin
// pool. Dashboard display links (wall screens) may not.
func (h *Handler) Roadmap(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	if p.DisplayLinkID != "" {
		writeErr(w, http.StatusForbidden, "display links cannot read the roadmap")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		h.log.Error("roadmap read", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)
	rm, err := roadmap.Load(ctx, tx)
	if err != nil {
		h.log.Error("roadmap read", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, rm)
}
