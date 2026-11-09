package publicapi

import (
	"context"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/roadmap"
)

const (
	// roadmapRateLimit is the most GET /public/roadmap/{token} requests one
	// IP may make per minute.
	roadmapRateLimit     = 30
	roadmapLimiterMaxIPs = 50000
	roadmapLinkGone      = "This link isn't valid any more."
)

// roadmapPublicBody is the signed-in roadmap body plus the link's name.
type roadmapPublicBody struct {
	roadmap.Roadmap
	SharedWith string `json:"shared_with"`
}

// RoadmapPublic - GET /public/roadmap/{token}
//
// Serves the roadmap to the holder of a share link. Unknown, expired and
// revoked tokens get the same 404, so a caller can't tell which applies.
// Reads through the admin pool: share links are global and app_tenant has
// no access to them.
func (h *Handler) RoadmapPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !h.roadmapLimiter.Allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeJSONStatus(w, http.StatusTooManyRequests, map[string]string{
			"error": "Too many requests. Try again in a minute.",
		})
		return
	}
	token := r.PathValue("token")
	if !roadmap.LooksLikeToken(token) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": roadmapLinkGone})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		h.log.Error("roadmap.public_read", "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer tx.Rollback(ctx)
	link, ok, err := roadmap.OpenLink(ctx, tx, token)
	if err != nil {
		h.log.Error("roadmap.public_read", "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": roadmapLinkGone})
		return
	}
	rm, err := roadmap.Load(ctx, tx)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		h.log.Error("roadmap.public_read", "link_id", link.ID, "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSONStatus(w, http.StatusOK, roadmapPublicBody{Roadmap: rm, SharedWith: link.Name})
}
