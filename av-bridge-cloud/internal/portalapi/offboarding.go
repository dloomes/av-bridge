package portalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
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
// of the customer's users, marks its nightly runs' room alerts checked, ends
// its remote-access sessions, inserts the
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
	id, ok := pathUUID(r, "id")
	if !ok {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
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
			WHERE id = $1::uuid FOR UPDATE`, id).Scan(&name, &started)
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
			 WHERE id = $1::uuid
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
		// Room alerts skip an offboarding customer's runs (run_alerts.go), so
		// mark them reflected now: a cancel must not raise stale alerts.
		if _, err := tx.Exec(ctx, `UPDATE nightly_run SET alert_checked_at = now()
			 WHERE customer_id = $1::uuid AND alert_checked_at IS NULL`, out.CustomerID); err != nil {
			return err
		}
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
// Clears the offboarding columns, marks runs that finished during the
// offboarding as alert-checked (so they raise no stale room alerts), records
// cancelled_at/by on the open customer_deletions row and audits
// customer.offboard_cancel. Nothing was
// deleted, so everything works again; users sign in again (their sessions
// were revoked at offboarding).
func (h *Handler) CancelOffboarding(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	id, ok := pathUUID(r, "id")
	if !ok {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	ctx := r.Context()
	var customerID string
	err := pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		var started *time.Time
		err := tx.QueryRow(ctx, `SELECT id::text, offboarding_started_at FROM customers
			WHERE id = $1::uuid FOR UPDATE`, id).Scan(&customerID, &started)
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
		// Runs that finished while offboarding were never reflected as room
		// alerts; they are stale now, so they don't alert or notify.
		if _, err := tx.Exec(ctx, `UPDATE nightly_run SET alert_checked_at = now()
			 WHERE customer_id = $1::uuid AND alert_checked_at IS NULL AND completed_at >= $2`,
			customerID, *started); err != nil {
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
// The customer's whole dataset (privacy.WriteCustomerExport) as CSV + JSON,
// with secrets redacted. Generated on request and never kept: it is built in
// a temporary file, removed on every path, and sent only when complete.
//
// Two transactions:
//  1. a READ ONLY, REPEATABLE READ snapshot that builds the archive, so
//     every table is read from one point in time;
//  2. once the archive is complete, a short READ COMMITTED transaction that
//     appends the download to the open customer_deletions row, if any, and
//     audits customer.export.
//
// The archive is sent only after (2) commits, so every archive that leaves
// is recorded, and a failure anywhere sends no archive. Keeping the write out
// of the snapshot lets overlapping exports both succeed: two REPEATABLE READ
// transactions updating the same deletion row would fail one with a
// serialization error, while READ COMMITTED updates simply queue on the row
// lock and each append lands.
func (h *Handler) ExportCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	id, ok := pathUUID(r, "id")
	if !ok {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	f, err := os.CreateTemp("", "customer-export-*.zip")
	if err != nil {
		h.log.Error("customer export: temp file", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	var name string
	var counts []privacy.TableCount
	err = pgx.BeginTxFunc(ctx, h.store.AdminPool(),
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1::uuid`, id).Scan(&name)
			if errors.Is(err, pgx.ErrNoRows) {
				return &errStatus{http.StatusNotFound, "customer not found"}
			}
			if err != nil {
				return err
			}
			counts, err = privacy.WriteCustomerExport(ctx, tx, id, name, f)
			return err
		})
	if err != nil {
		h.writeTxErr(w, "customer export", err)
		return
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		h.log.Error("customer export: rewind", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	err = pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		// Recorded like started_by / cancelled_by on the same vendor-only row.
		entry, _ := json.Marshal([]map[string]string{{
			"by": operatorIdentity(p),
			"at": time.Now().UTC().Format(time.RFC3339),
		}})
		if _, err := tx.Exec(ctx, `
			UPDATE customer_deletions SET downloads = downloads || $2::jsonb
			 WHERE customer_id = $1::uuid AND cancelled_at IS NULL AND deleted_at IS NULL`,
			id, string(entry)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, id, stampActor(p, audit.Entry{
			Actor:      auditActorID(p),
			Action:     "customer.export",
			TargetKind: "customer", TargetID: id,
			Metadata: map[string]any{"tables": len(counts), "bytes": size},
		}))
	})
	if err != nil {
		h.writeTxErr(w, "customer export: record download", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="customer-export-`+id+`-`+time.Now().UTC().Format("2006-01-02")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		h.log.Warn("customer export: sending failed", "customer", id, "error", err)
	}
}

// pathUUID reads a path value that must be a UUID, lower-cased. Malformed
// ids are refused up front so lookups can compare as uuid and use the index.
func pathUUID(r *http.Request, key string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(r.PathValue(key)))
	return v, uuidRE.MatchString(v)
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
	for _, dl := range it.Downloads {
		by, _ := dl["by"].(string)
		at, _ := dl["at"].(string)
		d.Downloads = append(d.Downloads, privacy.Download{By: by, At: at})
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
	if !ok || !uuidRE.MatchString(id) {
		writeErr(w, http.StatusNotFound, "deletion not found")
		return
	}
	it, err := scanDeletion(h.store.AdminPool().QueryRow(r.Context(), deletionSelect+` WHERE d.id = $1::uuid`, id))
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
