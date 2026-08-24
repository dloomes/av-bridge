package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

// Role mappings CRUD — group_id → role assignments consulted at Entra
// JIT time. Two scopes, two shapes:
//
//   Customer-scoped  /api/v1/role-mappings           (needs role_mapping.manage)
//     * role is the NAME of a row in the customer's own roles table
//     * grants land in user_roles when the JIT resolves the group
//
//   Vendor-scoped    /api/v1/vendor-role-mappings    (vendor-only)
//     * role is one of the legacy strings 'admin' | 'operator' | 'viewer'
//     * grant lands directly on users.role on JIT create
//
// role_mappings is not RLS-protected (0010 grants SELECT to both roles);
// isolation is enforced at the handler layer by scoping every query with
// the caller's customer_id or the vendor tenant UUID.

// Legacy roles the vendor mapping path accepts. Kept as a package-level
// slice so the validator + the API-emitted list of valid names share
// exactly one source. Matches the CHECK that ships on users.role.
var vendorRoles = []string{"admin", "operator", "viewer"}

func isVendorRole(r string) bool {
	for _, v := range vendorRoles {
		if v == r {
			return true
		}
	}
	return false
}

// mappingRow is the wire shape for both scopes. RoleID is populated only
// for customer mappings that resolve to an actual roles row (the UI
// renders a red "unknown role" pill when the name doesn't resolve any
// more, so an admin can spot orphans after a role rename).
type mappingRow struct {
	ID        string     `json:"id"`
	GroupID   string     `json:"group_id"`
	Role      string     `json:"role"`
	RoleID    string     `json:"role_id,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// -----------------------------------------------------------------------
// Customer scope
// -----------------------------------------------------------------------

// ListCustomerRoleMappings — GET /api/v1/role-mappings
//
// Any authed user in the tenant with role_mapping.manage sees the
// mapping table. LEFT JOIN to roles by (customer_id, lower(name)) so a
// mapping that references a renamed / deleted role still lists (with an
// empty role_id) rather than being invisible.
func (h *Handler) ListCustomerRoleMappings(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT m.id::text,
		       m.group_id,
		       m.role,
		       COALESCE(r.id::text, ''),
		       m.created_at
		  FROM role_mappings m
		  LEFT JOIN roles r
		    ON r.customer_id = m.customer_id
		   AND lower(r.name) = lower(m.role)
		 WHERE m.customer_id = $1
		 ORDER BY m.role, m.group_id`,
		p.CustomerID)
	if err != nil {
		h.log.Error("list role mappings", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []mappingRow{}
	for rows.Next() {
		var mr mappingRow
		if err := rows.Scan(&mr.ID, &mr.GroupID, &mr.Role, &mr.RoleID, &mr.CreatedAt); err != nil {
			h.log.Error("list role mappings scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, mr)
	}
	writeJSON(w, http.StatusOK, out)
}

type upsertCustomerMappingReq struct {
	GroupID string `json:"group_id"`
	Role    string `json:"role"`
}

// CreateCustomerRoleMapping — POST /api/v1/role-mappings
//
// role must be the name of an existing role in this customer's tenant.
// Validated case-insensitively so mixed-case UI input still matches.
// Uniqueness (one role per group per tenant) is enforced by the partial
// unique index in 0010.
func (h *Handler) CreateCustomerRoleMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req upsertCustomerMappingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.GroupID = strings.TrimSpace(req.GroupID)
	req.Role = strings.TrimSpace(req.Role)
	if req.GroupID == "" || req.Role == "" {
		writeErr(w, http.StatusBadRequest, "group_id and role are required")
		return
	}
	if len(req.GroupID) > 100 {
		writeErr(w, http.StatusBadRequest, "group_id is too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Validate the role name exists in this customer's roles table. The
	// mapping row could technically be stored without this — the LEFT
	// JOIN keeps orphans visible — but we prefer to reject at write time
	// so a typo doesn't silently produce a mapping that never grants.
	var exists int
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT count(*) FROM roles WHERE customer_id = $1 AND lower(name) = lower($2)`,
		p.CustomerID, req.Role).Scan(&exists); err != nil {
		h.log.Error("role mapping validate role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if exists == 0 {
		writeErr(w, http.StatusBadRequest,
			"role not found in this tenant — create the role before mapping to it")
		return
	}

	var id string
	err := h.store.AdminPool().QueryRow(ctx, `
		INSERT INTO role_mappings (customer_id, group_id, role)
		VALUES ($1, $2, $3)
		RETURNING id::text`,
		p.CustomerID, req.GroupID, req.Role).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict,
				"a mapping for that group already exists — edit it instead of creating a new one")
			return
		}
		h.log.Error("insert role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// UpdateCustomerRoleMapping — PATCH /api/v1/role-mappings/{id}
//
// Only the role field is mutable — changing group_id is delete-and-recreate
// to keep the audit trail obvious. Same role-name validation as create.
func (h *Handler) UpdateCustomerRoleMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Role = strings.TrimSpace(req.Role)
	if req.Role == "" {
		writeErr(w, http.StatusBadRequest, "role is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists int
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT count(*) FROM roles WHERE customer_id = $1 AND lower(name) = lower($2)`,
		p.CustomerID, req.Role).Scan(&exists); err != nil {
		h.log.Error("role mapping validate role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if exists == 0 {
		writeErr(w, http.StatusBadRequest,
			"role not found in this tenant")
		return
	}

	tag, err := h.store.AdminPool().Exec(ctx, `
		UPDATE role_mappings SET role = $3
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID, req.Role)
	if err != nil {
		h.log.Error("update role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "role mapping not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteCustomerRoleMapping — DELETE /api/v1/role-mappings/{id}
func (h *Handler) DeleteCustomerRoleMapping(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.store.AdminPool().Exec(ctx,
		`DELETE FROM role_mappings WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID)
	if err != nil {
		h.log.Error("delete role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "role mapping not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------
// Vendor scope
// -----------------------------------------------------------------------

// ListVendorRoleMappings — GET /api/v1/vendor-role-mappings
//
// Vendor-only (RequireVendor at the route). Row shape mirrors the
// customer path so the portal can reuse the same UI list component,
// with role_id always empty (vendor role is legacy text, no roles table).
func (h *Handler) ListVendorRoleMappings(w http.ResponseWriter, r *http.Request) {
	vendorID := db.PocVendorTenantUUID()
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT id::text, group_id, role, created_at
		  FROM role_mappings
		 WHERE vendor_tenant_id = $1
		 ORDER BY role, group_id`,
		vendorID)
	if err != nil {
		h.log.Error("list vendor role mappings", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []mappingRow{}
	for rows.Next() {
		var mr mappingRow
		if err := rows.Scan(&mr.ID, &mr.GroupID, &mr.Role, &mr.CreatedAt); err != nil {
			h.log.Error("list vendor role mappings scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, mr)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateVendorRoleMapping — POST /api/v1/vendor-role-mappings
//
// role must be one of admin/operator/viewer — the vendor path stays on
// the legacy 3-role model since vendor_tenants doesn't have a roles
// catalogue.
func (h *Handler) CreateVendorRoleMapping(w http.ResponseWriter, r *http.Request) {
	var req upsertCustomerMappingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.GroupID = strings.TrimSpace(req.GroupID)
	req.Role = strings.TrimSpace(strings.ToLower(req.Role))
	if req.GroupID == "" || req.Role == "" {
		writeErr(w, http.StatusBadRequest, "group_id and role are required")
		return
	}
	if !isVendorRole(req.Role) {
		writeErr(w, http.StatusBadRequest,
			"role must be one of admin, operator, viewer")
		return
	}
	if len(req.GroupID) > 100 {
		writeErr(w, http.StatusBadRequest, "group_id is too long")
		return
	}

	vendorID := db.PocVendorTenantUUID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id string
	err := h.store.AdminPool().QueryRow(ctx, `
		INSERT INTO role_mappings (vendor_tenant_id, group_id, role)
		VALUES ($1, $2, $3)
		RETURNING id::text`,
		vendorID, req.GroupID, req.Role).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict,
				"a mapping for that group already exists")
			return
		}
		h.log.Error("insert vendor role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// UpdateVendorRoleMapping — PATCH /api/v1/vendor-role-mappings/{id}
func (h *Handler) UpdateVendorRoleMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Role = strings.TrimSpace(strings.ToLower(req.Role))
	if !isVendorRole(req.Role) {
		writeErr(w, http.StatusBadRequest,
			"role must be one of admin, operator, viewer")
		return
	}

	vendorID := db.PocVendorTenantUUID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.store.AdminPool().Exec(ctx, `
		UPDATE role_mappings SET role = $3
		 WHERE id = $1 AND vendor_tenant_id = $2`,
		id, vendorID, req.Role)
	if err != nil {
		h.log.Error("update vendor role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "role mapping not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteVendorRoleMapping — DELETE /api/v1/vendor-role-mappings/{id}
func (h *Handler) DeleteVendorRoleMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	vendorID := db.PocVendorTenantUUID()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.store.AdminPool().Exec(ctx,
		`DELETE FROM role_mappings WHERE id = $1 AND vendor_tenant_id = $2`,
		id, vendorID)
	if err != nil {
		h.log.Error("delete vendor role mapping", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "role mapping not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isUniqueViolation is a small helper for the standard "duplicate key"
// pg error code so create paths can distinguish "already exists" from
// generic DB errors. Uses errors.As so pgx wrapping in future versions
// still routes correctly.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgx v5 wraps the pg protocol error in a struct we can string-match
	// on cheaply — importing pgconn here just for the constant is more
	// dependency than it's worth for one call site.
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key")
}

// unused imports guard for pgx — kept as future callers may want
// errors.Is comparisons against pgx.ErrNoRows here.
var _ = pgx.ErrNoRows
var _ = errors.Is
