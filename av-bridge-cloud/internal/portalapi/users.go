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
	"golang.org/x/crypto/bcrypt"
)

// User CRUD for customer admins (and vendor admins acting as a customer via
// X-Customer-Scope). Every endpoint scopes by Principal.CustomerID so a
// customer admin can never touch another customer's users, and a vendor
// admin with no scope gets a 400.
//
// Users hold ROLES (multi-role, via user_roles) and an optional PHYSICAL
// SCOPE (a list of building_ids they're restricted to; empty = full
// tenant). users.role text is legacy — kept nullable + populated with a
// derived "primary role" name for display consumers, no longer
// authoritative for authz.

// requireCustomerScope confirms Principal has a CustomerID. Vendor admins
// without X-Customer-Scope hit this and get a 400 telling them to pick a
// customer first.
func (h *Handler) requireCustomerScope(w http.ResponseWriter, r *http.Request) (portalauth.Principal, bool) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return p, false
	}
	if p.CustomerID == "" {
		writeErr(w, http.StatusBadRequest, "customer scope required — vendor callers must set X-Customer-Scope")
		return p, false
	}
	return p, true
}

type userRow struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	FullName         string     `json:"full_name,omitempty"`
	Role             string     `json:"role"` // legacy — derived primary role for display
	RoleIDs          []string   `json:"role_ids"`
	RoleNames        []string   `json:"role_names"`
	BuildingScopeIDs []string   `json:"building_scope_ids"`
	Disabled         bool       `json:"disabled"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	LastLoginAt      *time.Time `json:"last_login_at,omitempty"`
}

// ListUsers — GET /api/v1/users
// Any authenticated user in a tenant with view.users sees the roster.
// Aggregates roles + scope into arrays so the /users page needs one call.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT u.id::text, u.email, COALESCE(u.full_name,''),
		       (u.disabled_at IS NOT NULL), u.created_at, u.last_login_at,
		       COALESCE(u.role, ''),
		       COALESCE(array_agg(DISTINCT r.id::text) FILTER (WHERE r.id IS NOT NULL), '{}'),
		       COALESCE(array_agg(DISTINCT r.name)     FILTER (WHERE r.name IS NOT NULL), '{}'),
		       COALESCE(u.building_scope_ids::text[], '{}')
		  FROM users u
		  LEFT JOIN user_roles ur ON ur.user_id = u.id
		  LEFT JOIN roles r        ON r.id = ur.role_id
		 WHERE u.customer_id = $1
		 GROUP BY u.id
		 ORDER BY u.disabled_at NULLS FIRST, lower(u.email)`,
		p.CustomerID)
	if err != nil {
		h.log.Error("list users", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []userRow{}
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.Disabled,
			&u.CreatedAt, &u.LastLoginAt, &u.Role,
			&u.RoleIDs, &u.RoleNames, &u.BuildingScopeIDs); err != nil {
			h.log.Error("list users scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type createUserReq struct {
	Email            string   `json:"email"`
	Password         string   `json:"password"`
	FullName         string   `json:"full_name,omitempty"`
	RoleIDs          []string `json:"role_ids"`
	BuildingScopeIDs []string `json:"building_scope_ids,omitempty"`
}

// CreateUser — POST /api/v1/users  (needs user.create)
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if len(req.Password) < 12 {
		writeErr(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	if len(req.RoleIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one role is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// SSO-only tenants can't take a locally-authored password. Reject
	// before the bcrypt work — no point burning CPU on a hash we'd throw
	// away. Users on those tenants get provisioned by Entra JIT on their
	// first sign-in instead.
	var ssoRequired bool
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT COALESCE(sso_required, false) FROM customers WHERE id = $1`, p.CustomerID,
	).Scan(&ssoRequired); err != nil {
		h.log.Error("create user: sso lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ssoRequired {
		writeErr(w, http.StatusForbidden,
			"local user creation is disabled for this tenant — users are provisioned on first Entra sign-in")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.validateRoleIDsInTenant(ctx, p.CustomerID, req.RoleIDs); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.validateBuildingIDsInTenant(ctx, p.CustomerID, req.BuildingScopeIDs); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	primaryRole, err := h.derivePrimaryRoleName(ctx, p.CustomerID, req.RoleIDs)
	if err != nil {
		h.log.Error("derive primary role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, full_name, role, customer_id, building_scope_ids)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5, NULLIF($6::uuid[], '{}'::uuid[]))
		RETURNING id::text`,
		req.Email, string(hash), req.FullName, primaryRole, p.CustomerID, req.BuildingScopeIDs).Scan(&id); err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			writeErr(w, http.StatusConflict, "a user with that email already exists in this tenant")
			return
		}
		h.log.Error("create user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := assignRoles(ctx, tx, id, req.RoleIDs); err != nil {
		h.log.Error("create user: assign roles", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("create user commit", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "user.create",
			TargetKind: "user", TargetID: id,
			After: mustJSON(map[string]any{
				"email":              req.Email,
				"role_ids":           req.RoleIDs,
				"building_scope_ids": req.BuildingScopeIDs,
				"full_name":          req.FullName,
			}),
		}))
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type updateUserReq struct {
	FullName         *string   `json:"full_name,omitempty"`
	RoleIDs          *[]string `json:"role_ids,omitempty"`
	BuildingScopeIDs *[]string `json:"building_scope_ids,omitempty"`
	Disabled         *bool     `json:"disabled,omitempty"`
}

// UpdateUser — PATCH /api/v1/users/{id}  (needs user.update)
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req updateUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RoleIDs != nil && len(*req.RoleIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "role_ids cannot be empty — a user must hold at least one role")
		return
	}
	if req.Disabled != nil && *req.Disabled && id == p.UserID {
		writeErr(w, http.StatusBadRequest, "cannot disable yourself")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Snapshot before-state for audit and to preserve unchanged fields.
	var before userRow
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT id::text, email, COALESCE(full_name,''), COALESCE(role,''),
		       (disabled_at IS NOT NULL),
		       COALESCE(building_scope_ids::text[], '{}')
		  FROM users
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID).Scan(&before.ID, &before.Email, &before.FullName,
		&before.Role, &before.Disabled, &before.BuildingScopeIDs); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	if req.RoleIDs != nil {
		if err := h.validateRoleIDsInTenant(ctx, p.CustomerID, *req.RoleIDs); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.BuildingScopeIDs != nil {
		if err := h.validateBuildingIDsInTenant(ctx, p.CustomerID, *req.BuildingScopeIDs); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Compute target state so the write is single-shot.
	targetFullName := before.FullName
	targetDisabled := before.Disabled
	targetScope := before.BuildingScopeIDs
	targetPrimaryRole := before.Role
	if req.FullName != nil {
		targetFullName = *req.FullName
	}
	if req.Disabled != nil {
		targetDisabled = *req.Disabled
	}
	if req.BuildingScopeIDs != nil {
		targetScope = *req.BuildingScopeIDs
	}
	if req.RoleIDs != nil {
		derived, err := h.derivePrimaryRoleName(ctx, p.CustomerID, *req.RoleIDs)
		if err != nil {
			h.log.Error("derive primary role", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		targetPrimaryRole = derived
	}

	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE users SET
		  full_name          = NULLIF($3,''),
		  role               = NULLIF($4,''),
		  disabled_at        = CASE WHEN $5::bool THEN COALESCE(disabled_at, now()) ELSE NULL END,
		  building_scope_ids = NULLIF($6::uuid[], '{}'::uuid[])
		WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID, targetFullName, targetPrimaryRole, targetDisabled, targetScope); err != nil {
		h.log.Error("update user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Role change: swap the user_roles mapping wholesale. Uses DELETE + INSERT
	// so a role removed from the new list is also removed from the mapping.
	if req.RoleIDs != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
			h.log.Error("update user: clear roles", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := assignRoles(ctx, tx, id, *req.RoleIDs); err != nil {
			h.log.Error("update user: assign roles", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("update user commit", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// If we just disabled someone, kill their live sessions.
	if req.Disabled != nil && *req.Disabled {
		_, _ = h.store.AdminPool().Exec(ctx,
			`UPDATE user_sessions SET revoked_at = now()
			  WHERE user_id = $1 AND revoked_at IS NULL`, id)
	}

	auditPayload := map[string]any{
		"full_name":          targetFullName,
		"disabled":           targetDisabled,
		"building_scope_ids": targetScope,
	}
	if req.RoleIDs != nil {
		auditPayload["role_ids"] = *req.RoleIDs
	}
	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "user.update",
			TargetKind: "user", TargetID: id,
			Before: mustJSON(before), After: mustJSON(auditPayload),
		}))
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type resetPasswordReq struct {
	NewPassword string `json:"new_password"`
}

// ResetUserPassword — POST /api/v1/users/{id}/reset-password  (needs user.reset_password)
//
// Admin-initiated password reset. Revokes every session the user has so
// their previous token dies immediately.
func (h *Handler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req resetPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.NewPassword) < 12 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 12 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Refuse if the tenant has flipped SSO-only. Setting a password on a
	// user whose sign-in path is Entra-only just leaves inert bytes on
	// the row — better to reject explicitly so the admin doesn't think
	// they've done something useful.
	var ssoRequired bool
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT COALESCE(sso_required, false) FROM customers WHERE id = $1`, p.CustomerID,
	).Scan(&ssoRequired); err != nil {
		h.log.Error("reset password: sso lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ssoRequired {
		writeErr(w, http.StatusForbidden,
			"password reset is disabled for this tenant — users sign in with SSO")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	tag, err := h.store.AdminPool().Exec(ctx, `
		UPDATE users SET password_hash = $3
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID, string(hash))
	if err != nil {
		h.log.Error("reset password", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		  WHERE user_id = $1 AND revoked_at IS NULL`, id)

	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "user.reset_password",
			TargetKind: "user", TargetID: id,
			After: mustJSON(map[string]any{"note": "password reset by admin; sessions revoked"}),
		}))
	})
	w.WriteHeader(http.StatusNoContent)
}

// DeleteUser — DELETE /api/v1/users/{id}  (needs user.delete)
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == p.UserID {
		writeErr(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var email, role string
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT email, COALESCE(role,'') FROM users WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID).Scan(&email, &role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := h.store.AdminPool().Exec(ctx,
		`DELETE FROM users WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID); err != nil {
		h.log.Error("delete user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "user.delete",
			TargetKind: "user", TargetID: id,
			Before: mustJSON(map[string]any{"email": email, "role": role}),
		}))
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ---------------------------------------------------------------

// validateRoleIDsInTenant confirms every id belongs to the caller's tenant.
// Returns nil on success or a user-facing 400 message on mismatch.
func (h *Handler) validateRoleIDsInTenant(ctx context.Context, customerID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var count int
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT count(*)::int FROM roles WHERE customer_id = $1 AND id = ANY($2::uuid[])`,
		customerID, ids).Scan(&count); err != nil {
		return errors.New("could not validate roles")
	}
	if count != len(ids) {
		return errors.New("one or more role_ids don't belong to this tenant")
	}
	return nil
}

func (h *Handler) validateBuildingIDsInTenant(ctx context.Context, customerID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var count int
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT count(*)::int FROM buildings WHERE customer_id = $1 AND id = ANY($2::uuid[])`,
		customerID, ids).Scan(&count); err != nil {
		return errors.New("could not validate buildings")
	}
	if count != len(ids) {
		return errors.New("one or more building_scope_ids don't belong to this tenant")
	}
	return nil
}

// derivePrimaryRoleName returns a "primary role" name for the legacy
// users.role column: the most privileged system-default role in the list,
// falling back to the first custom role's name if none are system defaults.
// The result is display-only — Slice 2's permission engine reads from
// user_roles, not this field. Order: admin > operator > viewer > (custom).
func (h *Handler) derivePrimaryRoleName(ctx context.Context, customerID string, roleIDs []string) (string, error) {
	if len(roleIDs) == 0 {
		return "", nil
	}
	rows, err := h.store.AdminPool().Query(ctx, `
		SELECT name, is_system_default
		  FROM roles
		 WHERE customer_id = $1 AND id = ANY($2::uuid[])`,
		customerID, roleIDs)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var hasAdmin, hasOperator, hasViewer bool
	var firstCustom string
	for rows.Next() {
		var name string
		var isDefault bool
		if err := rows.Scan(&name, &isDefault); err != nil {
			return "", err
		}
		if isDefault {
			switch name {
			case "admin":
				hasAdmin = true
			case "operator":
				hasOperator = true
			case "viewer":
				hasViewer = true
			}
		} else if firstCustom == "" {
			firstCustom = name
		}
	}
	switch {
	case hasAdmin:
		return "admin", nil
	case hasOperator:
		return "operator", nil
	case hasViewer:
		return "viewer", nil
	default:
		return firstCustom, nil
	}
}

// assignRoles inserts the user_roles rows for a given user. Caller supplies
// the tx so it runs alongside the users insert/update atomically.
func assignRoles(ctx context.Context, tx pgx.Tx, userID string, roleIDs []string) error {
	for _, rid := range roleIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`,
			userID, rid); err != nil {
			return err
		}
	}
	return nil
}
