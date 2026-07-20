package portalapi

import (
	"errors"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/nightly"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Nightly Room Readiness — morning digest send-now.
//
// Slice 5. The digest sender runs on its own goroutine (see
// internal/nightly/digest.go) and normally fires once per customer per
// morning. This endpoint lets an operator trigger the same pipeline on
// demand, so they can verify their notification setup + preview the
// email without waiting until tomorrow.
//
// Manual sends bypass the "already sent today" guard and don't stamp
// digest_last_sent_for — the automatic morning digest still fires as
// scheduled.

// SendNightlyDigest — POST /api/v1/nightly/digest/send-now
//
// Sends the digest for the caller's customer immediately using the same
// pipeline the morning goroutine uses. Requires nightly.manage.
func (h *Handler) SendNightlyDigest(w http.ResponseWriter, r *http.Request) {
	if h.digest == nil {
		writeErr(w, http.StatusServiceUnavailable, "nightly digest sender not configured")
		return
	}
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no principal")
		return
	}

	if err := h.digest.SendForCustomer(r.Context(), p.CustomerID); err != nil {
		if errors.Is(err, nightly.ErrNoSchedule) {
			writeErr(w, http.StatusNotFound, "nightly schedule not configured yet")
			return
		}
		h.log.Error("nightly digest send-now failed",
			"customer", p.CustomerID, "error", err)
		writeErr(w, http.StatusInternalServerError, "send failed")
		return
	}

	// Fire-and-forget audit — the operator triggered a real email send, so
	// it goes in the log. We swallow audit errors: the send already
	// happened.
	if err := h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.digest.send-now",
			TargetKind: "customer", TargetID: p.CustomerID,
		}))
	}); err != nil {
		h.log.Warn("nightly digest send-now: audit failed",
			"customer", p.CustomerID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
