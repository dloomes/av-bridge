package portalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/privacy"
	"github.com/dloomes/av-bridge-cloud/internal/remote"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
)

// GDPR customer offboarding (helpdesk, vendor admins only). Day 0 starts a
// 30-day countdown to permanent deletion; until then the customer can be
// exported and the offboarding cancelled. db/offboarding.go refuses the
// customer everywhere while offboarding_started_at is set.

// offboardingPeriod is the time from offboarding to permanent deletion.
const offboardingPeriod = "30 days"

// SetLiveEvents wires the /ws/events hub so offboarding can close the
// customer's open event streams.
func (h *Handler) SetLiveEvents(hub *wsfanout.Hub) *Handler {
	h.events = hub
	return h
}

// operatorIdentity is what customer_deletions records as started_by /
// cancelled_by: the Involve operator's email (deletion evidence, see the
// register), or their user id when the principal has no email.
func operatorIdentity(p portalauth.Principal) string {
	if p.Email != "" {
		return p.Email
	}
	return auditActorID(p)
}

// auditActorID is the actor recorded on privacy and offboarding entries:
// the user id, never an email.
func auditActorID(p portalauth.Principal) string {
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	return p.Role
}

type offboardReq struct {
	Confirm string `json:"confirm"`
}

type offboardResp struct {
	CustomerID           string    `json:"customer_id"`
	OffboardingStartedAt time.Time `json:"offboarding_started_at"`
	DeleteAfter          time.Time `json:"delete_after"`
	SessionsRevoked      int64     `json:"sessions_revoked"`
	RemoteSessionsEnded  int       `json:"remote_sessions_ended"`
}

// errStatus carries an HTTP refusal out of a transaction.
type errStatus struct {
	code int
	msg  string
}

func (e *errStatus) Error() string { return e.msg }

// OffboardCustomer — POST /api/v1/helpdesk/customers/{id}/offboard {confirm}
//
// confirm must be the customer's name. In one transaction: sets the
// offboarding columns (delete_after = now + 30 days), revokes every session
// of the customer's users, ends its remote-access sessions, inserts the
// customer_deletions row and audits customer.offboard. After the commit it
// closes what is still connected: /ws/events streams and Collector tunnels.
func (h *Handler) OffboardCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	var req offboardReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	ctx := r.Context()

	var (
		out        offboardResp
		remoteIDs  []string
		collectors []string
	)
	err := pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		var name string
		var started *time.Time
		err := tx.QueryRow(ctx, `SELECT name, offboarding_started_at FROM customers
			WHERE id::text = $1 FOR UPDATE`, id).Scan(&name, &started)
		if errors.Is(err, pgx.ErrNoRows) {
			return &errStatus{http.StatusNotFound, "customer not found"}
		}
		if err != nil {
			return err
		}
		if started != nil {
			return &errStatus{http.StatusConflict, "customer is already offboarding"}
		}
		if strings.TrimSpace(req.Confirm) != name {
			return &errStatus{http.StatusBadRequest, "confirm must match the customer name"}
		}
		if err := tx.QueryRow(ctx, `
			UPDATE customers
			   SET offboarding_started_at = now(), offboarding_started_by = $2,
			       delete_after = now() + $3::interval
			 WHERE id::text = $1
			RETURNING id::text, offboarding_started_at, delete_after`,
			id, operatorIdentity(p), offboardingPeriod).Scan(
			&out.CustomerID, &out.OffboardingStartedAt, &out.DeleteAfter); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE user_sessions SET revoked_at = now()
			 WHERE revoked_at IS NULL
			   AND user_id IN (SELECT u.id FROM users u WHERE u.customer_id = $1::uuid)`, out.CustomerID)
		if err != nil {
			return err
		}
		out.SessionsRevoked = tag.RowsAffected()
		if remoteIDs, err = remote.EndCustomerSessions(ctx, tx, out.CustomerID, "customer offboarded"); err != nil {
			return err
		}
		out.RemoteSessionsEnded = len(remoteIDs)
		rows, err := tx.Query(ctx, `SELECT id::text FROM collectors WHERE customer_id = $1::uuid`, out.CustomerID)
		if err != nil {
			return err
		}
		if collectors, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO customer_deletions (customer_id, customer_name, started_at, started_by)
			VALUES ($1::uuid, $2, $3, $4)`,
			out.CustomerID, name, out.OffboardingStartedAt, operatorIdentity(p)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, out.CustomerID, stampActor(p, audit.Entry{
			Actor:      auditActorID(p),
			Action:     "customer.offboard",
			TargetKind: "customer", TargetID: out.CustomerID,
			Metadata: map[string]any{
				"delete_after":          out.DeleteAfter.UTC().Format(time.RFC3339),
				"sessions_revoked":      out.SessionsRevoked,
				"remote_sessions_ended": out.RemoteSessionsEnded,
			},
		}))
	})
	if err != nil {
		h.writeTxErr(w, "offboard customer", err)
		return
	}

	// Committed: every way back in is refused from here. Close what is
	// still connected; reconnects are authenticated again and refused.
	streams, tunnels := 0, 0
	if h.events != nil {
		streams = h.events.CloseCustomer(out.CustomerID)
	}
	if h.remote != nil {
		h.remote.Release(context.WithoutCancel(ctx), remoteIDs)
		if t := h.remote.Tunnels(); t != nil {
			tunnels = t.Drop(collectors...)
		}
	}
	h.log.Info("customer offboarding started", "customer", out.CustomerID,
		"actor", auditActorID(p), "delete_after", out.DeleteAfter,
		"sessions_revoked", out.SessionsRevoked, "remote_sessions_ended", out.RemoteSessionsEnded,
		"event_streams_closed", streams, "tunnels_dropped", tunnels)
	writeJSON(w, http.StatusOK, out)
}

// CancelOffboarding — POST /api/v1/helpdesk/customers/{id}/cancel-offboarding
//
// Clears the offboarding columns, records cancelled_at/by on the open
// customer_deletions row and audits customer.offboard_cancel. Nothing was
// deleted, so everything works again; users sign in again (their sessions
// were revoked at offboarding).
func (h *Handler) CancelOffboarding(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	ctx := r.Context()
	var customerID string
	err := pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		var started *time.Time
		err := tx.QueryRow(ctx, `SELECT id::text, offboarding_started_at FROM customers
			WHERE id::text = $1 FOR UPDATE`, id).Scan(&customerID, &started)
		if errors.Is(err, pgx.ErrNoRows) {
			return &errStatus{http.StatusNotFound, "customer not found"}
		}
		if err != nil {
			return err
		}
		if started == nil {
			return &errStatus{http.StatusConflict, "customer is not offboarding"}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE customers
			   SET offboarding_started_at = NULL, offboarding_started_by = NULL, delete_after = NULL
			 WHERE id = $1::uuid`, customerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE customer_deletions SET cancelled_at = now(), cancelled_by = $2
			 WHERE customer_id = $1::uuid AND cancelled_at IS NULL AND deleted_at IS NULL`,
			customerID, operatorIdentity(p)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, customerID, stampActor(p, audit.Entry{
			Actor:      auditActorID(p),
			Action:     "customer.offboard_cancel",
			TargetKind: "customer", TargetID: customerID,
		}))
	})
	if err != nil {
		h.writeTxErr(w, "cancel offboarding", err)
		return
	}
	h.log.Info("customer offboarding cancelled", "customer", customerID, "actor", auditActorID(p))
	writeJSON(w, http.StatusOK, map[string]string{"customer_id": customerID, "status": "active"})
}

// ExportCustomer — GET /api/v1/helpdesk/customers/{id}/export.zip
//
// The customer's whole dataset (privacy.CollectCustomer) as CSV + JSON,
// built in memory and never stored. The download is appended to the open
// customer_deletions row, if any, and audited as customer.export, in the
// same transaction as the read: if anything fails, no archive is sent.
func (h *Handler) ExportCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	var buf bytes.Buffer
	var customerID, name string
	// Repeatable read: every table is read from one snapshot.
	err := pgx.BeginTxFunc(ctx, h.store.AdminPool(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text, name FROM customers WHERE id::text = $1`, id).Scan(&customerID, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return &errStatus{http.StatusNotFound, "customer not found"}
		}
		if err != nil {
			return err
		}
		tables, err := privacy.CollectCustomer(ctx, tx, customerID)
		if err != nil {
			return err
		}
		if err := privacy.WriteZIP(&buf, tables, privacy.CustomerREADME(name, tables)); err != nil {
			return err
		}
		entry, _ := json.Marshal([]map[string]string{{
			"by": auditActorID(p),
			"at": time.Now().UTC().Format(time.RFC3339),
		}})
		if _, err := tx.Exec(ctx, `
			UPDATE customer_deletions SET downloads = downloads || $2::jsonb
			 WHERE customer_id = $1::uuid AND cancelled_at IS NULL AND deleted_at IS NULL`,
			customerID, string(entry)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, customerID, stampActor(p, audit.Entry{
			Actor:      auditActorID(p),
			Action:     "customer.export",
			TargetKind: "customer", TargetID: customerID,
			Metadata: map[string]any{"tables": len(tables), "bytes": buf.Len()},
		}))
	})
	if err != nil {
		h.writeTxErr(w, "customer export", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="customer-export-`+customerID+`-`+time.Now().UTC().Format("2006-01-02")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

type deletionItem struct {
	ID           string           `json:"id"`
	CustomerID   string           `json:"customer_id"`
	CustomerName string           `json:"customer_name"`
	Status       string           `json:"status"`
	StartedAt    time.Time        `json:"started_at"`
	StartedBy    string           `json:"started_by"`
	CancelledAt  *time.Time       `json:"cancelled_at"`
	CancelledBy  *string          `json:"cancelled_by"`
	DeletedAt    *time.Time       `json:"deleted_at"`
	DeleteAfter  *time.Time       `json:"delete_after"`
	RowCounts    map[string]int64 `json:"row_counts"`
	Downloads    []map[string]any `json:"downloads"`
}

const deletionSelect = `
	SELECT d.id::text, d.customer_id::text, d.customer_name, d.started_at, d.started_by,
	       d.cancelled_at, d.cancelled_by, d.deleted_at,
	       CASE WHEN d.cancelled_at IS NULL AND d.deleted_at IS NULL THEN c.delete_after END,
	       d.row_counts, d.downloads
	  FROM customer_deletions d
	  LEFT JOIN customers c ON c.id = d.customer_id`

func scanDeletion(row pgx.Row) (deletionItem, error) {
	var it deletionItem
	err := row.Scan(&it.ID, &it.CustomerID, &it.CustomerName, &it.StartedAt, &it.StartedBy,
		&it.CancelledAt, &it.CancelledBy, &it.DeletedAt, &it.DeleteAfter, &it.RowCounts, &it.Downloads)
	if it.Downloads == nil {
		it.Downloads = []map[string]any{}
	}
	it.Status = it.deletion().Status()
	return it, err
}

func (it deletionItem) deletion() privacy.Deletion {
	d := privacy.Deletion{
		ID: it.ID, CustomerID: it.CustomerID, CustomerName: it.CustomerName,
		StartedAt: it.StartedAt, StartedBy: it.StartedBy,
		CancelledAt: it.CancelledAt, DeletedAt: it.DeletedAt, RowCounts: it.RowCounts,
	}
	if it.CancelledBy != nil {
		d.CancelledBy = *it.CancelledBy
	}
	return d
}

// ListDeletions — GET /api/v1/helpdesk/deletions
//
// Every offboarding, newest first: open, cancelled and completed.
func (h *Handler) ListDeletions(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.AdminPool().Query(r.Context(), deletionSelect+` ORDER BY d.started_at DESC`)
	if err != nil {
		h.log.Error("list deletions", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []deletionItem{}
	for rows.Next() {
		it, err := scanDeletion(rows)
		if err != nil {
			h.log.Error("list deletions scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		h.log.Error("list deletions rows", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeletionCertificate — GET /api/v1/helpdesk/deletions/{id}.csv
//
// The deletion certificate as CSV (privacy.WriteDeletionCertificate). The
// route captures the whole last segment; it must end in .csv.
func (h *Handler) DeletionCertificate(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(strings.ToLower(r.PathValue("file")), ".csv")
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	it, err := scanDeletion(h.store.AdminPool().QueryRow(r.Context(), deletionSelect+` WHERE d.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "deletion not found")
		return
	}
	if err != nil {
		h.log.Error("deletion certificate", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	var buf bytes.Buffer
	if err := privacy.WriteDeletionCertificate(&buf, it.deletion()); err != nil {
		h.log.Error("deletion certificate csv", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="deletion-certificate-`+it.ID+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// writeTxErr answers a failed transaction: an errStatus as itself, anything
// else as a logged 500.
func (h *Handler) writeTxErr(w http.ResponseWriter, what string, err error) {
	var es *errStatus
	if errors.As(err, &es) {
		writeErr(w, es.code, es.msg)
		return
	}
	h.log.Error(what, "error", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}
