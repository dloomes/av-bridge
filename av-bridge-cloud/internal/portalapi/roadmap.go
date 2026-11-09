package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

// RoadmapRoute is one editor endpoint. server.go mounts each behind
// Middleware + RequireVendor; the handlers then require roadmap.CanEdit.
type RoadmapRoute struct {
	Pattern string
	Handler http.HandlerFunc
}

const (
	roadmapEditorOnly  = "Only roadmap editors can change the roadmap."
	roadmapMaxBodySize = 256 << 10
)

// RoadmapEditorRoutes lists the roadmap editor APIs, shared by server.go and
// the tests so both mount the same table.
func (h *Handler) RoadmapEditorRoutes() []RoadmapRoute {
	const base = "/api/v1/helpdesk/roadmap"
	return []RoadmapRoute{
		{"PUT " + base + "/settings", h.RoadmapSaveSettings},
		{"POST " + base + "/items", h.RoadmapCreateItem},
		{"PATCH " + base + "/items/{id}", h.RoadmapUpdateItem},
		{"DELETE " + base + "/items/{id}", h.RoadmapDeleteItem},
		{"PUT " + base + "/items/order", h.RoadmapOrderItems},
		{"PUT " + base + "/periods", h.RoadmapSavePeriods},
		{"PUT " + base + "/themes", h.RoadmapSaveThemes},
		{"PUT " + base + "/shipped", h.RoadmapSaveShipped},
		{"GET " + base + "/links", h.RoadmapListLinks},
		{"POST " + base + "/links", h.RoadmapCreateLink},
		{"POST " + base + "/links/{id}/revoke", h.RoadmapRevokeLink},
		{"DELETE " + base + "/links/{id}", h.RoadmapDeleteLink},
	}
}

// roadmapWrite is the shared shape of every editor write: require an editor,
// decode the body into in (when non-nil), run fn in one admin-pool
// transaction, log roadmap.<action> with the attrs fn returns (the ids it
// touched), and answer with the whole roadmap.
func (h *Handler) roadmapWrite(w http.ResponseWriter, r *http.Request, action string, in any,
	fn func(ctx context.Context, tx pgx.Tx, by string) ([]any, error)) {
	h.roadmapEditorDo(w, r, action, in, fn, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return roadmap.Load(ctx, tx)
	})
}

// roadmapEditorDo is roadmapWrite with a chosen response: result runs in the
// same transaction after fn and its value is the 200 body. A nil fn is a
// read: nothing runs before result and nothing is logged.
func (h *Handler) roadmapEditorDo(w http.ResponseWriter, r *http.Request, action string, in any,
	fn func(ctx context.Context, tx pgx.Tx, by string) ([]any, error),
	result func(ctx context.Context, tx pgx.Tx) (any, error)) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	if !roadmap.CanEdit(p) {
		writeErr(w, http.StatusForbidden, roadmapEditorOnly)
		return
	}
	if in != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, roadmapMaxBodySize)).Decode(in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		h.log.Error("roadmap."+action, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)
	var attrs []any
	if fn != nil {
		attrs, err = fn(ctx, tx, operatorIdentity(p))
	}
	if err != nil {
		var ve *roadmap.ValidationError
		switch {
		case errors.As(err, &ve):
			writeErr(w, http.StatusBadRequest, ve.Msg)
		case errors.Is(err, roadmap.ErrNotFound):
			writeErr(w, http.StatusNotFound, "Roadmap item not found.")
		case errors.Is(err, roadmap.ErrLinkNotFound):
			writeErr(w, http.StatusNotFound, "Share link not found.")
		case errors.Is(err, roadmap.ErrThemeHasItems):
			writeErr(w, http.StatusConflict, roadmap.ErrThemeHasItems.Error())
		default:
			h.log.Error("roadmap."+action, "actor", auditActorID(p), "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	out, err := result(ctx, tx)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		h.log.Error("roadmap."+action, "actor", auditActorID(p), "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if fn != nil {
		h.log.Info("roadmap."+action, append([]any{"actor", auditActorID(p)}, attrs...)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// RoadmapSaveSettings - PUT /api/v1/helpdesk/roadmap/settings
func (h *Handler) RoadmapSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in roadmap.SettingsInput
	h.roadmapWrite(w, r, "settings_update", &in, func(ctx context.Context, tx pgx.Tx, by string) ([]any, error) {
		return nil, roadmap.SaveSettings(ctx, tx, in, by)
	})
}

// RoadmapCreateItem - POST /api/v1/helpdesk/roadmap/items
func (h *Handler) RoadmapCreateItem(w http.ResponseWriter, r *http.Request) {
	var in roadmap.ItemInput
	h.roadmapWrite(w, r, "item_create", &in, func(ctx context.Context, tx pgx.Tx, by string) ([]any, error) {
		id, err := roadmap.CreateItem(ctx, tx, in, by)
		return []any{"item_id", id}, err
	})
}

// RoadmapUpdateItem - PATCH /api/v1/helpdesk/roadmap/items/{id}
func (h *Handler) RoadmapUpdateItem(w http.ResponseWriter, r *http.Request) {
	var in roadmap.ItemInput
	id := r.PathValue("id")
	h.roadmapWrite(w, r, "item_update", &in, func(ctx context.Context, tx pgx.Tx, by string) ([]any, error) {
		return []any{"item_id", id}, roadmap.UpdateItem(ctx, tx, id, in, by)
	})
}

// RoadmapDeleteItem - DELETE /api/v1/helpdesk/roadmap/items/{id}
func (h *Handler) RoadmapDeleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.roadmapWrite(w, r, "item_delete", nil, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"item_id", id}, roadmap.DeleteItem(ctx, tx, id)
	})
}

// RoadmapOrderItems - PUT /api/v1/helpdesk/roadmap/items/order
func (h *Handler) RoadmapOrderItems(w http.ResponseWriter, r *http.Request) {
	var in roadmap.OrderInput
	h.roadmapWrite(w, r, "items_reorder", &in, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"theme_id", in.ThemeID, "period", in.Period, "items", len(in.IDs)},
			roadmap.OrderItems(ctx, tx, in)
	})
}

// RoadmapSavePeriods - PUT /api/v1/helpdesk/roadmap/periods (exactly 4)
func (h *Handler) RoadmapSavePeriods(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.PeriodInput
	h.roadmapWrite(w, r, "periods_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"periods", len(in)}, roadmap.SavePeriods(ctx, tx, in)
	})
}

// RoadmapSaveThemes - PUT /api/v1/helpdesk/roadmap/themes (full list)
func (h *Handler) RoadmapSaveThemes(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.ThemeInput
	h.roadmapWrite(w, r, "themes_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"themes", len(in)}, roadmap.SaveThemes(ctx, tx, in)
	})
}

// RoadmapSaveShipped - PUT /api/v1/helpdesk/roadmap/shipped (full list)
func (h *Handler) RoadmapSaveShipped(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.ShippedInput
	h.roadmapWrite(w, r, "shipped_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"shipped", len(in)}, roadmap.SaveShipped(ctx, tx, in)
	})
}

// roadmapLinksBody answers the link list, revoke and delete: every link,
// newest first. Tokens are never included.
func roadmapLinksBody(ctx context.Context, tx pgx.Tx) (any, error) {
	links, err := roadmap.ListLinks(ctx, tx)
	return map[string]any{"links": links}, err
}

// RoadmapListLinks - GET /api/v1/helpdesk/roadmap/links
func (h *Handler) RoadmapListLinks(w http.ResponseWriter, r *http.Request) {
	h.roadmapEditorDo(w, r, "links_list", nil, nil, roadmapLinksBody)
}

// RoadmapCreateLink - POST /api/v1/helpdesk/roadmap/links
//
// Answers {link, url}. The URL holds the token, which is shown only here:
// the database keeps its SHA-256. It is built on the same portal base URL
// as the magic-link and password-reset emails (ENTRA_PORTAL_BASE_URL);
// when that is unset (dev) the URL is a bare path.
func (h *Handler) RoadmapCreateLink(w http.ResponseWriter, r *http.Request) {
	var in roadmap.LinkInput
	var link roadmap.Link
	var token string
	h.roadmapEditorDo(w, r, "link_create", &in,
		func(ctx context.Context, tx pgx.Tx, by string) ([]any, error) {
			var err error
			link, token, err = roadmap.CreateLink(ctx, tx, in, by)
			return []any{"link_id", link.ID, "expires_at", link.ExpiresAt}, err
		},
		func(context.Context, pgx.Tx) (any, error) {
			base := strings.TrimRight(h.magicPortalBaseURL(), "/")
			return map[string]any{"link": link, "url": base + "/roadmap/s/" + token}, nil
		})
}

// RoadmapRevokeLink - POST /api/v1/helpdesk/roadmap/links/{id}/revoke
func (h *Handler) RoadmapRevokeLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.roadmapEditorDo(w, r, "link_revoke", nil, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"link_id", id}, roadmap.RevokeLink(ctx, tx, id)
	}, roadmapLinksBody)
}

// RoadmapDeleteLink - DELETE /api/v1/helpdesk/roadmap/links/{id}
func (h *Handler) RoadmapDeleteLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.roadmapEditorDo(w, r, "link_delete", nil, func(ctx context.Context, tx pgx.Tx, _ string) ([]any, error) {
		return []any{"link_id", id}, roadmap.DeleteLink(ctx, tx, id)
	}, roadmapLinksBody)
}
