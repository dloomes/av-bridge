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
// The users table lives outside RLS (login has to look up rows before a
// principal exists), so every query here filters on customer_id explicitly.
// Reviewer note: any change here must preserve that filter — dropping it
// would let a customer admin enumerate every tenant's users.

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
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	FullName    string     `json:"full_name,omitempty"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// ListUsers — GET /api/v1/users
// Any authenticated user in a tenant sees their tenant's user roster.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT id::text, email, COALESCE(full_name,''), role,
		       (disabled_at IS NOT NULL), created_at, last_login_at
		  FROM users
		 WHERE customer_id = $1
		 ORDER BY disabled_at NULLS FIRST, lower(email)`,
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
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.Role,
			&u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			h.log.Error("list users scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type createUserReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name,omitempty"`
	Role     string `json:"role"`
}

// CreateUser — POST /api/v1/users  (admin-only, wired at route mount)
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
	if !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator, or viewer")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// One tx: insert the user + hook them up to the matching system-default
	// role. Without the user_roles row the permission resolver would return
	// an empty set and the new account would be locked out until Slice 6's
	// multi-role picker landed a fix retrospectively.
	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, full_name, role, customer_id)
		VALUES ($1, $2, NULLIF($3,''), $4, $5)
		RETURNING id::text`,
		req.Email, string(hash), req.FullName, req.Role, p.CustomerID).Scan(&id); err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			writeErr(w, http.StatusConflict, "a user with that email already exists in this tenant")
			return
		}
		h.log.Error("create user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r
		 WHERE r.customer_id = $2 AND r.name = $3 AND r.is_system_default`,
		id, p.CustomerID, req.Role); err != nil {
		h.log.Error("create user: assign role", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("create user commit", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Audit inside the tenant's scope.
	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "user.create",
			TargetKind: "user", TargetID: id,
			After: mustJSON(map[string]any{
				"email":     req.Email,
				"role":      req.Role,
				"full_name": req.FullName,
			}),
		})
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type updateUserReq struct {
	FullName *string `json:"full_name,omitempty"`
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// UpdateUser — PATCH /api/v1/users/{id}  (admin-only)
// Prevents an admin from disabling themselves — an easy mistake that would
// leave the tenant without an admin.
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
	if req.Role != nil && !validRole(*req.Role) {
		writeErr(w, http.StatusBadRequest, "role must be admin, operator, or viewer")
		return
	}
	if req.Disabled != nil && *req.Disabled && id == p.UserID {
		writeErr(w, http.StatusBadRequest, "cannot disable yourself")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Build a partial UPDATE. Using COALESCE keeps the SQL flat instead of
	// stitching a query dynamically — safer, and only touches columns whose
	// argument the caller passed.
	var before, after userRow
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT id::text, email, COALESCE(full_name,''), role,
		       (disabled_at IS NOT NULL)
		  FROM users
		 WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID).Scan(&before.ID, &before.Email, &before.FullName, &before.Role, &before.Disabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Compute the target state before writing so the audit trail is clean.
	target := before
	if req.FullName != nil {
		target.FullName = *req.FullName
	}
	if req.Role != nil {
		target.Role = *req.Role
	}
	if req.Disabled != nil {
		target.Disabled = *req.Disabled
	}

	// Wrap the write in a tx so a role change syncs users.role +
	// user_roles atomically. Otherwise a request between the two writes
	// could observe an inconsistent state (users.role='admin' but no
	// user_roles rows).
	tx, err := h.store.AdminPool().Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE users SET
		  full_name  = NULLIF($3,''),
		  role       = $4,
		  disabled_at = CASE WHEN $5::bool THEN COALESCE(disabled_at, now()) ELSE NULL END
		WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID, target.FullName, target.Role, target.Disabled); err != nil {
		h.log.Error("update user", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Role change: swap the user_roles mapping. Uses DELETE + INSERT rather
	// than UPSERT because we may need to detach the old and attach the new,
	// and the PK is (user_id, role_id) so a straight UPSERT would leave
	// stale rows if the user had additional roles (which today they don't,
	// but Slice 6 makes possible).
	if req.Role != nil && *req.Role != before.Role {
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
			h.log.Error("update user: clear roles", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, r.id FROM roles r
			 WHERE r.customer_id = $2 AND r.name = $3 AND r.is_system_default`,
			id, p.CustomerID, *req.Role); err != nil {
			h.log.Error("update user: assign role", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.log.Error("update user commit", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// If we just disabled someone, kill their live sessions too — otherwise
	// their existing token stays valid until it expires. Skip on enable.
	if req.Disabled != nil && *req.Disabled {
		_, _ = h.store.AdminPool().Exec(ctx,
			`UPDATE user_sessions SET revoked_at = now()
			  WHERE user_id = $1 AND revoked_at IS NULL`, id)
	}

	after = target
	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "user.update",
			TargetKind: "user", TargetID: id,
			Before: mustJSON(before), After: mustJSON(after),
		})
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

type resetPasswordReq struct {
	NewPassword string `json:"new_password"`
}

// ResetUserPassword — POST /api/v1/users/{id}/reset-password  (admin-only)
//
// Admin-initiated password reset. Does NOT require knowing the old password
// (that's what /auth/change-password is for). Revokes every session the
// user has so their previous token dies immediately. The user must sign in
// again with the new password. Actor + target are audited so a rogue admin
// can't quietly hijack an account.
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
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

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
	// Kill any live sessions for that user — the new password must be used.
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		  WHERE user_id = $1 AND revoked_at IS NULL`, id)

	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "user.reset_password",
			TargetKind: "user", TargetID: id,
			// No password material in the audit body — just the fact.
			After: mustJSON(map[string]any{"note": "password reset by admin; sessions revoked"}),
		})
	})
	w.WriteHeader(http.StatusNoContent)
}

// DeleteUser — DELETE /api/v1/users/{id}  (admin-only)
// Hard delete. user_sessions cascade via FK. Self-delete is blocked to
// avoid the same admin-loses-access foot-gun as UpdateUser.
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

	// Snapshot the row before deleting so the audit trail is useful.
	var email, role string
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT email, role FROM users WHERE id = $1 AND customer_id = $2`,
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
	_ = h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "user.delete",
			TargetKind: "user", TargetID: id,
			Before: mustJSON(map[string]any{"email": email, "role": role}),
		})
	})
	w.WriteHeader(http.StatusNoContent)
}

func validRole(r string) bool {
	return r == "admin" || r == "operator" || r == "viewer"
}
