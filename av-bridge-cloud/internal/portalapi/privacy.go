package portalapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/privacy"
	"github.com/jackc/pgx/v5"
)

// GDPR data requests: access export.

type accessExportReq struct {
	Email string `json:"email"`
}

type helpdeskAccessExportReq struct {
	CustomerID string `json:"customer_id"`
	Vendor     bool   `json:"vendor"`
	Email      string `json:"email"`
}

// AccessExport — POST /api/v1/privacy/access-export {email}
//
// Returns a ZIP of the personal data held about the person with that email in
// the caller's customer. Gated by privacy.manage at the route.
func (h *Handler) AccessExport(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req accessExportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	h.exportAccess(w, r, p, p.CustomerID, "", req.Email)
}

// HelpdeskAccessExport — POST /api/v1/helpdesk/privacy/access-export
// {customer_id, email} or {vendor: true, email}
//
// Vendor admins only (RequireVendorAdmin at the route). {vendor: true} exports
// an Involve staff member (the vendor tenant); otherwise a customer's user.
func (h *Handler) HelpdeskAccessExport(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	var req helpdeskAccessExportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Vendor == (req.CustomerID != "") {
		writeErr(w, http.StatusBadRequest, "give either customer_id or vendor: true")
		return
	}
	if req.Vendor {
		h.exportAccess(w, r, p, "", db.PocVendorTenantUUID(), req.Email)
		return
	}
	cust, found, err := h.helpdeskCustomer(r.Context(), req.CustomerID)
	if err != nil {
		h.log.Error("access export: customer lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	h.exportAccess(w, r, p, cust, "", req.Email)
}

// GDPR data requests: erasure.

type eraseReq struct {
	Email   string `json:"email"`
	Confirm string `json:"confirm"`
	DryRun  bool   `json:"dry_run"`
}

type helpdeskEraseReq struct {
	CustomerID string `json:"customer_id"`
	Vendor     bool   `json:"vendor"`
	eraseReq
}

type eraseResp struct {
	StandIn string           `json:"stand_in"`
	Counts  map[string]int64 `json:"counts"`
	Actions []string         `json:"actions"`
}

// PrivacyErase — POST /api/v1/privacy/erase {email, confirm, dry_run}
//
// Anonymises the person with that email in the caller's customer (see
// privacy.Erase). Gated by privacy.manage at the route; the account, if one
// remains, must also pass the user-management guards DeleteUser applies.
func (h *Handler) PrivacyErase(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req eraseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	h.erase(w, r, p, p.CustomerID, "", req)
}

// HelpdeskPrivacyErase — POST /api/v1/helpdesk/privacy/erase
// {customer_id, email, confirm, dry_run} or {vendor: true, email, confirm, dry_run}
//
// Vendor admins only (RequireVendorAdmin at the route). {vendor: true}
// erases an Involve staff member (the vendor tenant).
func (h *Handler) HelpdeskPrivacyErase(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	var req helpdeskEraseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Vendor == (req.CustomerID != "") {
		writeErr(w, http.StatusBadRequest, "give either customer_id or vendor: true")
		return
	}
	if req.Vendor {
		h.erase(w, r, p, "", db.PocVendorTenantUUID(), req.eraseReq)
		return
	}
	cust, found, err := h.helpdeskCustomer(r.Context(), req.CustomerID)
	if err != nil {
		h.log.Error("erase: customer lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	h.erase(w, r, p, cust, "", req.eraseReq)
}

// helpdeskCustomer normalises a customer id from a helpdesk request and
// reports whether that customer exists.
func (h *Handler) helpdeskCustomer(ctx context.Context, id string) (string, bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	var exists bool
	err := h.store.AdminPool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM customers WHERE id::text = $1)`, id).Scan(&exists)
	return id, exists, err
}

// errNothingToErase rolls back an erasure that found nothing at all.
var errNothingToErase = errors.New("nothing to erase")

// erase guards, erases and audits in one admin transaction. A dry run makes
// no change and writes no audit entry.
func (h *Handler) erase(w http.ResponseWriter, r *http.Request, p portalauth.Principal, customerID, vendorTenantID string, req eraseReq) {
	email := strings.TrimSpace(req.Email)
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(req.Confirm), email) {
		writeErr(w, http.StatusBadRequest, "confirm must match email")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var res privacy.Result
	err := pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		s, err := privacy.FindSubject(ctx, tx, customerID, vendorTenantID, email)
		if err != nil {
			return err
		}
		if s.UserID != "" {
			if err := h.guardErase(ctx, p, s); err != nil {
				return err
			}
		}
		res, err = privacy.Erase(ctx, tx, s, req.DryRun)
		if err != nil {
			return err
		}
		if s.UserID == "" && len(res.Counts) == 0 && len(res.Actions) == 0 {
			return errNothingToErase
		}
		if req.DryRun || customerID == "" {
			// Vendor staff have no customer to hold the audit entry; it is
			// logged after the commit instead.
			return nil
		}
		// Actor by user id, never ActorLabel() (the admin's email): this
		// entry must carry no email at all.
		actor := p.Role
		if p.UserID != "" {
			actor = "user:" + p.UserID
		}
		return audit.Record(ctx, tx, customerID, stampActor(p, audit.Entry{
			Actor:      actor,
			Action:     "privacy.erase",
			TargetKind: "user",
			TargetID:   res.StandIn,
			Metadata:   map[string]any{"counts": res.Counts},
		}))
	})
	if err != nil {
		var ge *guardError
		switch {
		case errors.As(err, &ge):
			writeErr(w, ge.status, ge.msg)
		case errors.Is(err, errNothingToErase):
			writeErr(w, http.StatusNotFound, "no data found for that email")
		default:
			h.log.Error("privacy erase", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	if customerID == "" && !req.DryRun {
		h.log.Info("privacy erase (vendor staff)", "action", "privacy.erase",
			"actor", "user:"+p.UserID, "target_id", res.StandIn, "counts", res.Counts)
	}
	if res.Counts == nil {
		res.Counts = map[string]int64{}
	}
	if res.Actions == nil {
		res.Actions = []string{}
	}
	writeJSON(w, http.StatusOK, eraseResp{StandIn: res.StandIn, Counts: res.Counts, Actions: res.Actions})
}

// guardErase applies DeleteUser's refusals to erasing an account: not
// yourself, not someone outside your scope or with permissions you lack
// (guardManageUser; vendor staff pass it), and not a customer's last
// administrator, who would leave the organisation with nobody to run it.
func (h *Handler) guardErase(ctx context.Context, p portalauth.Principal, s privacy.Subject) error {
	if p.UserID != "" && strings.EqualFold(p.UserID, s.UserID) {
		return &guardError{http.StatusBadRequest, "cannot erase yourself"}
	}
	if s.CustomerID == "" {
		return nil
	}
	if !p.IsVendor {
		if err := h.guardManageUser(ctx, p, s.UserID); err != nil {
			return err
		}
	}
	var last bool
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT EXISTS (
		         SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		          WHERE ur.user_id = $1::uuid AND r.customer_id = $2::uuid
		            AND r.name = 'admin' AND r.is_system_default)
		   AND NOT EXISTS (
		         SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		                  JOIN users u ON u.id = ur.user_id
		          WHERE ur.user_id <> $1::uuid AND r.customer_id = $2::uuid
		            AND r.name = 'admin' AND r.is_system_default
		            AND u.disabled_at IS NULL)`, s.UserID, s.CustomerID).Scan(&last); err != nil {
		return err
	}
	if last {
		return &guardError{http.StatusConflict, "this is the organisation's last administrator; give someone else the admin role first"}
	}
	return nil
}

// exportAccess collects, zips and audits in one admin transaction (RLS is
// bypassed, Collect filters on scope itself). The whole ZIP is built in memory
// first so any failure is a clean error response, never a partial download.
func (h *Handler) exportAccess(w http.ResponseWriter, r *http.Request, p portalauth.Principal, customerID, vendorTenantID, email string) {
	email = strings.TrimSpace(email)
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
		return
	}
	sum := sha256.Sum256([]byte(strings.ToLower(email)))
	emailHash := hex.EncodeToString(sum[:])

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var buf bytes.Buffer
	found := false
	err := pgx.BeginFunc(ctx, h.store.AdminPool(), func(tx pgx.Tx) error {
		s, err := privacy.FindSubject(ctx, tx, customerID, vendorTenantID, email)
		if err != nil {
			return err
		}
		tables, err := privacy.Collect(ctx, tx, s)
		if err != nil {
			return err
		}
		if len(tables) == 0 {
			return nil
		}
		if err := privacy.WriteZIP(&buf, tables, privacy.AccessREADME(s, tables)); err != nil {
			return err
		}
		found = true
		if customerID == "" {
			// audit_log rows belong to a customer; a vendor-staff export has
			// none, so it is logged (hash only) after the commit instead.
			return nil
		}
		// The actor is recorded by user id, not ActorLabel(): the usual label
		// is the admin's email, and this row must carry no email at all.
		actor := p.Role
		if p.UserID != "" {
			actor = "user:" + p.UserID
		}
		e := stampActor(p, audit.Entry{
			Actor:    actor,
			Action:   "privacy.access_export",
			Metadata: map[string]any{"email_sha256": emailHash, "tables": len(tables)},
		})
		return audit.Record(ctx, tx, customerID, e)
	})
	if err != nil {
		h.log.Error("access export", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no data found for that email")
		return
	}
	if customerID == "" {
		h.log.Info("privacy access export (vendor staff)", "action", "privacy.access_export",
			"actor_user_id", p.UserID, "email_sha256", emailHash)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="access-request-`+time.Now().UTC().Format("2006-01-02")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
