package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	}
}

// roadmapWrite is the shared shape of every editor write: require an editor,
// decode the body into in (when non-nil), run fn in one admin-pool
// transaction, log roadmap.<action>, and answer with the whole roadmap.
func (h *Handler) roadmapWrite(w http.ResponseWriter, r *http.Request, action string, in any,
	fn func(ctx context.Context, tx pgx.Tx, by string) error) {
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
	if err := fn(ctx, tx, operatorIdentity(p)); err != nil {
		var ve *roadmap.ValidationError
		switch {
		case errors.As(err, &ve):
			writeErr(w, http.StatusBadRequest, ve.Msg)
		case errors.Is(err, roadmap.ErrNotFound):
			writeErr(w, http.StatusNotFound, "Roadmap item not found.")
		case errors.Is(err, roadmap.ErrThemeHasItems):
			writeErr(w, http.StatusConflict, roadmap.ErrThemeHasItems.Error())
		default:
			h.log.Error("roadmap."+action, "actor", auditActorID(p), "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	rm, err := roadmap.Load(ctx, tx)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		h.log.Error("roadmap."+action, "actor", auditActorID(p), "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.log.Info("roadmap."+action, "actor", auditActorID(p))
	writeJSON(w, http.StatusOK, rm)
}

// RoadmapSaveSettings - PUT /api/v1/helpdesk/roadmap/settings
func (h *Handler) RoadmapSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in roadmap.SettingsInput
	h.roadmapWrite(w, r, "settings_update", &in, func(ctx context.Context, tx pgx.Tx, by string) error {
		return roadmap.SaveSettings(ctx, tx, in, by)
	})
}

// RoadmapCreateItem - POST /api/v1/helpdesk/roadmap/items
func (h *Handler) RoadmapCreateItem(w http.ResponseWriter, r *http.Request) {
	var in roadmap.ItemInput
	h.roadmapWrite(w, r, "item_create", &in, func(ctx context.Context, tx pgx.Tx, by string) error {
		_, err := roadmap.CreateItem(ctx, tx, in, by)
		return err
	})
}

// RoadmapUpdateItem - PATCH /api/v1/helpdesk/roadmap/items/{id}
func (h *Handler) RoadmapUpdateItem(w http.ResponseWriter, r *http.Request) {
	var in roadmap.ItemInput
	id := r.PathValue("id")
	h.roadmapWrite(w, r, "item_update", &in, func(ctx context.Context, tx pgx.Tx, by string) error {
		return roadmap.UpdateItem(ctx, tx, id, in, by)
	})
}

// RoadmapDeleteItem - DELETE /api/v1/helpdesk/roadmap/items/{id}
func (h *Handler) RoadmapDeleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.roadmapWrite(w, r, "item_delete", nil, func(ctx context.Context, tx pgx.Tx, _ string) error {
		return roadmap.DeleteItem(ctx, tx, id)
	})
}

// RoadmapOrderItems - PUT /api/v1/helpdesk/roadmap/items/order
func (h *Handler) RoadmapOrderItems(w http.ResponseWriter, r *http.Request) {
	var in roadmap.OrderInput
	h.roadmapWrite(w, r, "items_reorder", &in, func(ctx context.Context, tx pgx.Tx, _ string) error {
		return roadmap.OrderItems(ctx, tx, in)
	})
}

// RoadmapSavePeriods - PUT /api/v1/helpdesk/roadmap/periods (exactly 4)
func (h *Handler) RoadmapSavePeriods(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.PeriodInput
	h.roadmapWrite(w, r, "periods_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) error {
		return roadmap.SavePeriods(ctx, tx, in)
	})
}

// RoadmapSaveThemes - PUT /api/v1/helpdesk/roadmap/themes (full list)
func (h *Handler) RoadmapSaveThemes(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.ThemeInput
	h.roadmapWrite(w, r, "themes_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) error {
		return roadmap.SaveThemes(ctx, tx, in)
	})
}

// RoadmapSaveShipped - PUT /api/v1/helpdesk/roadmap/shipped (full list)
func (h *Handler) RoadmapSaveShipped(w http.ResponseWriter, r *http.Request) {
	var in []roadmap.ShippedInput
	h.roadmapWrite(w, r, "shipped_update", &in, func(ctx context.Context, tx pgx.Tx, _ string) error {
		return roadmap.SaveShipped(ctx, tx, in)
	})
}
