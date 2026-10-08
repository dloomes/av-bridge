package portalapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/collectorha"
	"github.com/jackc/pgx/v5"
)

// MakeCollectorActive — POST /api/v1/collectors/{id}/make-active
//
// Hands a collector group's devices to machine {id} (a primary or one of
// its standbys). There's no automatic failback, so this is how devices go
// back to the primary after a failover. The current holder stops at its
// next poll; {id} starts collectorha.HandoverGrace later, so the two never
// poll the same devices.
func (h *Handler) MakeCollectorActive(w http.ResponseWriter, r *http.Request) {
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
		groupID, err := collectorha.RequestHandover(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, collectorha.ErrNotInGroup) || errors.Is(err, collectorha.ErrMachineOffline) {
			reqErr = err
			return nil
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "collector.make_active", TargetKind: "collector", TargetID: groupID,
			RelatedTargetKind: "collector", RelatedTargetID: id,
		}))
	})
	if !ok {
		return
	}
	switch {
	case errors.Is(reqErr, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "collector not found")
	case reqErr != nil:
		writeErr(w, http.StatusConflict, reqErr.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "handover_requested"})
	}
}
