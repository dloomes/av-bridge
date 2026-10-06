package portalapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/collectorupdate"
	"github.com/jackc/pgx/v5"
)

// Collector self-update, portal side. The release on offer is the one
// bundled with this cloud build; see internal/collectorupdate.

// SetCollectorUpdates wires the update service. Without it the update
// endpoints answer 503 and the collector list reports no updates.
func (h *Handler) SetCollectorUpdates(svc *collectorupdate.Service) *Handler {
	h.collectorUpdates = svc
	return h
}

// RequestCollectorUpdate — POST /api/v1/collectors/{id}/update
//
// Marks the collector to update to the bundled release. The collector's
// held poll is woken, so an online collector starts within a second.
func (h *Handler) RequestCollectorUpdate(w http.ResponseWriter, r *http.Request) {
	if h.collectorUpdates == nil {
		writeErr(w, http.StatusServiceUnavailable, "collector updates aren't available on this cloud")
		return
	}
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}
	var reqErr error
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := h.collectorUpdates.Request(ctx, tx, id)
		var ne collectorupdate.ErrNotEligible
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &ne) || errors.Is(err, collectorupdate.ErrInFlight) {
			reqErr = err
			return nil
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "collector.update_requested", TargetKind: "collector", TargetID: id,
			Metadata: map[string]any{"version": h.collectorUpdates.Release().Version},
		}))
	})
	if !ok {
		return
	}
	var ne collectorupdate.ErrNotEligible
	switch {
	case errors.Is(reqErr, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "collector not found")
	case errors.As(reqErr, &ne):
		writeErr(w, http.StatusConflict, "can't update: "+ne.Reason)
	case errors.Is(reqErr, collectorupdate.ErrInFlight):
		writeErr(w, http.StatusConflict, reqErr.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{
			"state": "requested", "version": h.collectorUpdates.Release().Version,
		})
	}
}

// RequestAllCollectorUpdates — POST /api/v1/collectors/update-all
//
// Requests the update for every visible collector that can take it;
// the rest are skipped (out of date but blocked, already current, or
// already updating).
func (h *Handler) RequestAllCollectorUpdates(w http.ResponseWriter, r *http.Request) {
	if h.collectorUpdates == nil {
		writeErr(w, http.StatusServiceUnavailable, "collector updates aren't available on this cloud")
		return
	}
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	requested := 0
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM collectors ORDER BY name`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			err := h.collectorUpdates.Request(ctx, tx, id)
			var ne collectorupdate.ErrNotEligible
			if errors.As(err, &ne) || errors.Is(err, collectorupdate.ErrInFlight) || errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			requested++
		}
		if requested == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "collector.update_all_requested", TargetKind: "collector",
			Metadata: map[string]any{"version": h.collectorUpdates.Release().Version, "requested": requested},
		}))
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"requested": requested})
}
