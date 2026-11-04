package portalapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	var exists bool
	if err := h.store.AdminPool().QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM customers WHERE id::text = lower($1))`, strings.TrimSpace(req.CustomerID)).Scan(&exists); err != nil {
		h.log.Error("access export: customer lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !exists {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	h.exportAccess(w, r, p, strings.ToLower(strings.TrimSpace(req.CustomerID)), "", req.Email)
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
			// none, so it is logged (hash only) instead.
			h.log.Info("privacy access export (vendor staff)", "actor_user_id", p.UserID, "email_sha256", emailHash)
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
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="access-request-`+time.Now().UTC().Format("2006-01-02")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
