package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Roles CRUD — per-tenant role catalogue.
//
// Reads: any authed user in a tenant can list the roles in their tenant so
//   the /users page can show which roles a colleague holds. Gated by
//   view.users at the route.
// Writes: role.crud permission required. System defaults reject writes at
//   the handler level (see the is_system_default checks below).
//
// System-default roles (is_system_default=true) reject all writes at the
// API layer — they exist so a customer always has usable admin/operator/
// viewer bundles regardless of what an admin does to custom roles.
//
// Cross-tenant vendor access: vendor with X-Customer-Scope becomes a
// tenant admin for the scoped customer, so vendor helpdesk users can
// create roles on behalf of a customer. Same pattern as the /users CRUD.

// roleRow is the wire shape returned by list/get. Permissions come along
// on GET so the UI doesn't need a separate roundtrip to fill the matrix.
type roleRow struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	IsSystemDefault bool       `json:"is_system_default"`
	Permissions     []string   `json:"permissions"`
	AssignedUsers   int        `json:"assigned_users"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
}

// ListRoles — GET /api/v1/roles
// Any authed user in a tenant sees the tenant's role catalogue.
func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT r.id::text, r.name, COALESCE(r.description, ''), r.is_system_default,
		       COALESCE(array_agg(rp.permission) FILTER (WHERE rp.permission IS NOT NULL), '{}') AS perms,
		       (SELECT count(*) FROM user_roles ur WHERE ur.role_id = r.id)::int AS assigned,
		       r.created_at
		  FROM roles r
		  LEFT JOIN role_permissions rp ON rp.role_id = r.id
		 WHERE r.customer_id = $1
		 GROUP BY r.id
		 ORDER BY r.is_system_default DESC, r.name`,
		p.CustomerID)
	if err != nil {
		h.log.Error("list roles", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []roleRow{}
	for rows.Next() {
		var rr roleRow
		if err := rows.Scan(&rr.ID, &rr.Name, &rr.Description, &rr.IsSystemDefault,
			&rr.Permissions, &rr.AssignedUsers, &rr.CreatedAt); err != nil {
			h.log.Error("list roles scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, rr)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetRole — GET /api/v1/roles/{id}
// Any authed user in a tenant reads their tenant's roles.
func (h *Handler) GetRole(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var rr roleRow
	if err := h.store.AdminPool().QueryRow(r.Context(), `
		SELECT r.id::text, r.name, COALESCE(r.description, ''), r.is_system_default,
		       COALESCE(array_agg(rp.permission) FILTER (WHERE rp.permission IS NOT NULL), '{}'),
		       (SELECT count(*) FROM user_roles ur WHERE ur.role_id = r.id)::int,
		       r.created_at
		  FROM roles r
		  LEFT JOIN role_permissions rp ON rp.role_id = r.id
		 WHERE r.id = $1 AND r.customer_id = $2
		 GROUP BY r.id`,
		id, p.CustomerID).Scan(&rr.ID, &rr.Name, &rr.Description, &rr.IsSystemDefault,
		&rr.Permissions, &rr.AssignedUsers, &rr.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "role not found")
			return
		}
		h.log.Error("get role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, rr)
}

type createRoleReq struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
}

// CreateRole — POST /api/v1/roles
// Admin only. Custom (non-system-default) role, name is unique per tenant.
func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req createRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := validatePermissions(req.Permissions); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Prevent shadowing a system-default name — otherwise a custom "admin"
	// role would confuse the UI (which lists system defaults first).
	if isSystemDefaultName(req.Name) {
		writeErr(w, http.StatusBadRequest, "name conflicts with a system-default role — choose another")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO roles (customer_id, name, description, is_system_default)
		VALUES ($1, $2, NULLIF($3,''), false)
		RETURNING id::text`,
		p.CustomerID, req.Name, req.Description).Scan(&id); err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			writeErr(w, http.StatusConflict, "a role with that name already exists")
			return
		}
		h.log.Error("insert role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := insertRolePermissions(ctx, tx, id, req.Permissions); err != nil {
		h.log.Error("insert role permissions", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("commit role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "role.create",
			TargetKind: "role", TargetID: id,
			After: mustJSON(map[string]any{
				"name":        req.Name,
				"permissions": req.Permissions,
			}),
		})
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type updateRoleReq struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// UpdateRole — PATCH /api/v1/roles/{id}
// Admin only. Custom roles only — system defaults reject writes. Passing a
// non-nil permissions list REPLACES the role's permission set (a set is
// a set — additive edits get confusing when the client is unsure of the
// current state).
func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req updateRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	var existing struct {
		Name            string
		Description     string
		IsSystemDefault bool
	}
	if err := tx.QueryRow(ctx, `
		SELECT name, COALESCE(description,''), is_system_default
		  FROM roles
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID).Scan(&existing.Name, &existing.Description, &existing.IsSystemDefault); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "role not found")
			return
		}
		h.log.Error("update role lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing.IsSystemDefault {
		writeErr(w, http.StatusForbidden, "system-default roles cannot be edited")
		return
	}

	newName := existing.Name
	newDesc := existing.Description
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeErr(w, http.StatusBadRequest, "name cannot be blank")
			return
		}
		if isSystemDefaultName(trimmed) {
			writeErr(w, http.StatusBadRequest, "name conflicts with a system-default role")
			return
		}
		newName = trimmed
	}
	if req.Description != nil {
		newDesc = *req.Description
	}

	if _, err := tx.Exec(ctx, `
		UPDATE roles SET name = $3, description = NULLIF($4,'')
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID, newName, newDesc); err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			writeErr(w, http.StatusConflict, "a role with that name already exists")
			return
		}
		h.log.Error("update role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	if req.Permissions != nil {
		if err := validatePermissions(*req.Permissions); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM role_permissions WHERE role_id = $1`, id); err != nil {
			h.log.Error("clear role permissions", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := insertRolePermissions(ctx, tx, id, *req.Permissions); err != nil {
			h.log.Error("replace role permissions", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("commit update role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	auditPayload := map[string]any{
		"name":        newName,
		"description": newDesc,
	}
	if req.Permissions != nil {
		auditPayload["permissions"] = *req.Permissions
	}
	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "role.update",
			TargetKind: "role", TargetID: id,
			Before: mustJSON(existing),
			After:  mustJSON(auditPayload),
		})
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// DeleteRole — DELETE /api/v1/roles/{id}
// Admin only. System defaults reject. Rejects if any user still holds the
// role — the admin must reassign users first (avoid orphaning).
func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	var name string
	var isSystemDefault bool
	var assigned int
	if err := tx.QueryRow(ctx, `
		SELECT r.name, r.is_system_default,
		       (SELECT count(*)::int FROM user_roles ur WHERE ur.role_id = r.id)
		  FROM roles r
		 WHERE r.id = $1 AND r.customer_id = $2`,
		id, p.CustomerID).Scan(&name, &isSystemDefault, &assigned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "role not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if isSystemDefault {
		writeErr(w, http.StatusForbidden, "system-default roles cannot be deleted")
		return
	}
	if assigned > 0 {
		writeErr(w, http.StatusConflict, "role is still assigned to users — remove those assignments first")
		return
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM roles WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID); err != nil {
		h.log.Error("delete role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "role.delete",
			TargetKind: "role", TargetID: id,
			Before: mustJSON(map[string]any{"name": name}),
		})
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ---------------------------------------------------------------

func validatePermissions(perms []string) error {
	seen := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		if !portalauth.IsKnownPermission(p) {
			return errors.New("unknown permission key: " + p)
		}
		seen[p] = struct{}{}
	}
	return nil
}

func insertRolePermissions(ctx context.Context, tx pgx.Tx, roleID string, perms []string) error {
	// Dedup — a client sending "view.dashboard" twice would violate the PK.
	seen := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`,
			roleID, p); err != nil {
			return err
		}
	}
	return nil
}

// isSystemDefaultName reserves the three names the migration seeds so a
// custom role can't shadow them. Case-insensitive because the UI shouldn't
// let "Admin" through when "admin" is reserved.
func isSystemDefaultName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "admin", "operator", "viewer":
		return true
	}
	return false
}
