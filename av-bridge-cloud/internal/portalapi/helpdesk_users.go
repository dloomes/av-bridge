package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Vendor-tenant user management (M3.1).
//
// /users is customer-scoped: requireCustomerScope() 400s a vendor caller
// without X-Customer-Scope. Vendors need their own management surface for
// the helpdesk-tenant users (their own team) — that's this file.
//
// Only the current single vendor tenant (db.PocVendorTenantUUID) is
// visible; a future migration to multiple vendor tenants would filter by
// the caller's vendor_tenant_id claim instead.

// vendorUserRow is the wire shape. role_source flags whether the row's
// role came from an Entra mapping (auto-syncs on every sign-in) or from
// a manual admin promotion (survives group churn).
type vendorUserRow struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	FullName    string     `json:"full_name,omitempty"`
	Role        string     `json:"role"`
	RoleSource  string     `json:"role_source"` // 'entra' | 'manual'
	Provider    string     `json:"provider"`    // 'local' | 'entra'
	Disabled    bool       `json:"disabled"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// HelpdeskListUsers — GET /api/v1/helpdesk/users
//
// Vendor-only (RequireVendor at the route). Returns every user row for
// the vendor tenant. Uses the admin pool since the users table lives
// outside RLS by design (see 0015).
func (h *Handler) HelpdeskListUsers(w http.ResponseWriter, r *http.Request) {
	vendorID := db.PocVendorTenantUUID()
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT id::text,
		       lower(email),
		       COALESCE(full_name,''),
		       role,
		       role_source,
		       provider,
		       disabled_at IS NOT NULL,
		       created_at,
		       last_login_at
		  FROM users
		 WHERE vendor_tenant_id = $1
		 ORDER BY lower(email)`,
		vendorID)
	if err != nil {
		h.log.Error("list vendor users", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []vendorUserRow{}
	for rows.Next() {
		var u vendorUserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName,
			&u.Role, &u.RoleSource, &u.Provider,
			&u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			h.log.Error("list vendor users scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type updateVendorUserReq struct {
	FullName *string `json:"full_name,omitempty"`
	Role     *string `json:"role,omitempty"`     // one of admin/operator/viewer
	Disabled *bool   `json:"disabled,omitempty"`
}

// HelpdeskUpdateUser — PATCH /api/v1/helpdesk/users/{id}
//
// Vendor-only. Setting `role` flips role_source to 'manual' so subsequent
// Entra sign-ins won't clobber the admin override (see syncVendorRole in
// entra.go). Clearing full_name (empty string) stores NULL. Toggling
// `disabled` sets/clears disabled_at using the current server clock.
//
// Cannot delete-by-PATCH — /helpdesk/users/{id} DELETE handles that.
func (h *Handler) HelpdeskUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateVendorUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate role early so the DB round-trip only happens on a good
	// payload. Same three legacy values the vendor path knows.
	if req.Role != nil {
		normalized := strings.TrimSpace(strings.ToLower(*req.Role))
		if !isVendorRole(normalized) {
			writeErr(w, http.StatusBadRequest, "role must be one of admin, operator, viewer")
			return
		}
		req.Role = &normalized
	}

	vendorID := db.PocVendorTenantUUID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Build the UPDATE dynamically so omitted fields don't overwrite
	// stored values. disabled_at is handled with a SQL literal (now() /
	// NULL) rather than a bound arg so we don't have to pass a Go
	// time.Time; role_source is likewise a static literal since manual
	// edits always flip it to 'manual'.
	set := []string{}
	args := []any{id, vendorID}
	if req.FullName != nil {
		v := strings.TrimSpace(*req.FullName)
		args = append(args, nullIfEmpty(v))
		set = append(set, "full_name = $"+intToStr(len(args)))
	}
	if req.Role != nil {
		args = append(args, *req.Role)
		set = append(set, "role = $"+intToStr(len(args)))
		set = append(set, "role_source = 'manual'")
	}
	if req.Disabled != nil {
		if *req.Disabled {
			set = append(set, "disabled_at = now()")
		} else {
			set = append(set, "disabled_at = NULL")
		}
	}
	if len(set) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	tag, err := h.store.AdminPool().Exec(ctx,
		"UPDATE users SET "+strings.Join(set, ", ")+
			" WHERE id = $1 AND vendor_tenant_id = $2",
		args...)
	if err != nil {
		h.log.Error("update vendor user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}

	// Disabling a user should also revoke every active session so the
	// change takes effect immediately rather than at TTL expiry.
	if req.Disabled != nil && *req.Disabled {
		_, _ = h.store.AdminPool().Exec(ctx,
			`UPDATE user_sessions SET revoked_at = now()
			  WHERE user_id = $1 AND revoked_at IS NULL`, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// HelpdeskDeleteUser — DELETE /api/v1/helpdesk/users/{id}
//
// Vendor-only. Refuses to delete the caller themselves (same footgun
// prevention the customer /users path applies). Sessions are dropped by
// the users→user_sessions ON DELETE CASCADE in 0015.
func (h *Handler) HelpdeskDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())
	if p.UserID != "" && p.UserID == id {
		writeErr(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	vendorID := db.PocVendorTenantUUID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := h.store.AdminPool().Exec(ctx,
		`DELETE FROM users WHERE id = $1 AND vendor_tenant_id = $2`,
		id, vendorID)
	if err != nil {
		h.log.Error("delete vendor user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// intToStr is a small helper for building parameterised SQL. Kept local
// so callers don't reach for strconv just to number placeholders.
func intToStr(n int) string {
	// Two-digit ceiling is fine — SET lists here never exceed a handful.
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// unused imports guard for pgx — future error branches may want ErrNoRows.
var _ = errors.Is
var _ = pgx.ErrNoRows
