package portalapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Guards on user management, so that holding user.* permissions doesn't
// let someone act beyond their own access:
//
//   - Scope: a caller restricted to part of the estate only sees and manages
//     users whose access lies entirely inside the caller's. Users with
//     whole-tenant access are outside every restricted caller's reach.
//   - Permissions: nobody can give a role carrying permissions they don't
//     hold, or edit, disable, reset or delete a user who holds permissions
//     they don't — otherwise a user-admin could reset an admin's password
//     and sign in as them.
//
// Vendor staff act as unscoped admins inside the customer they've picked,
// so the guards don't apply to them.

// guardError is a refusal to send to the caller as-is.
type guardError struct {
	status int
	msg    string
}

func (e *guardError) Error() string { return e.msg }

func writeGuardErr(w http.ResponseWriter, h *Handler, err error) {
	var ge *guardError
	if errors.As(err, &ge) {
		writeErr(w, ge.status, ge.msg)
		return
	}
	h.log.Error("user guard", "error", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// scopeCoverage is every hierarchy node a scope reaches: the granted nodes
// plus everything beneath them.
type scopeCoverage struct {
	bu, regions, locations, buildings, rooms map[string]bool
}

// coverage expands sc down the hierarchy within one tenant.
func (h *Handler) coverage(ctx context.Context, customerID string, sc db.Scope) (scopeCoverage, error) {
	set := func(ids []string) map[string]bool {
		m := make(map[string]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	c := scopeCoverage{bu: set(sc.BusinessUnits), regions: set(sc.Regions), locations: set(sc.Locations),
		buildings: set(sc.Buildings), rooms: set(sc.Rooms)}
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	// Each level adds the children of everything covered at the level above.
	for _, step := range []struct {
		sql       string
		from, add map[string]bool
	}{
		{`SELECT id::text FROM regions   WHERE customer_id = $1 AND business_unit_id::text = ANY($2)`, c.bu, c.regions},
		{`SELECT id::text FROM locations WHERE customer_id = $1 AND region_id::text = ANY($2)`, c.regions, c.locations},
		{`SELECT id::text FROM buildings WHERE customer_id = $1 AND location_id::text = ANY($2)`, c.locations, c.buildings},
		{`SELECT id::text FROM rooms     WHERE customer_id = $1 AND building_id::text = ANY($2)`, c.buildings, c.rooms},
	} {
		if len(step.from) == 0 {
			continue
		}
		rows, err := h.store.AdminPool().Query(ctx, step.sql, customerID, keys(step.from))
		if err != nil {
			return c, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return c, err
			}
			step.add[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return c, err
		}
	}
	return c, nil
}

// contains reports whether every node of sc is covered. A whole-tenant
// scope (empty) is never contained in a restricted one.
func (c scopeCoverage) contains(sc db.Scope) bool {
	if sc.Empty() {
		return false
	}
	for _, lvl := range []struct {
		ids []string
		in  map[string]bool
	}{
		{sc.BusinessUnits, c.bu}, {sc.Regions, c.regions}, {sc.Locations, c.locations},
		{sc.Buildings, c.buildings}, {sc.Rooms, c.rooms},
	} {
		for _, id := range lvl.ids {
			if !lvl.in[id] {
				return false
			}
		}
	}
	return true
}

// userScope reads a user's stored scope. ok=false when the user isn't in
// the tenant.
func (h *Handler) userScope(ctx context.Context, customerID, userID string) (db.Scope, bool, error) {
	var sc db.Scope
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT COALESCE(business_unit_scope_ids::text[], '{}'), COALESCE(region_scope_ids::text[], '{}'),
		       COALESCE(location_scope_ids::text[], '{}'), COALESCE(building_scope_ids::text[], '{}'),
		       COALESCE(room_scope_ids::text[], '{}')
		  FROM users WHERE id::text = $1 AND customer_id = $2`, userID, customerID).
		Scan(&sc.BusinessUnits, &sc.Regions, &sc.Locations, &sc.Buildings, &sc.Rooms)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sc, false, nil
		}
		return sc, false, err
	}
	return sc, true, nil
}

// permsOf returns the union of permissions granted by the given roles, or
// by the roles a user holds when userID is set.
func (h *Handler) permsOf(ctx context.Context, customerID string, roleIDs []string, userID string) (map[string]struct{}, error) {
	var q string
	var arg any
	if userID != "" {
		q = `SELECT DISTINCT rp.permission FROM user_roles ur
		       JOIN roles r ON r.id = ur.role_id AND r.customer_id = $1
		       JOIN role_permissions rp ON rp.role_id = ur.role_id
		      WHERE ur.user_id::text = $2`
		arg = userID
	} else {
		q = `SELECT DISTINCT rp.permission FROM role_permissions rp
		       JOIN roles r ON r.id = rp.role_id AND r.customer_id = $1
		      WHERE rp.role_id::text = ANY($2)`
		arg = roleIDs
	}
	rows, err := h.store.AdminPool().Query(ctx, q, customerID, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var perm string
		if err := rows.Scan(&perm); err != nil {
			return nil, err
		}
		out[perm] = struct{}{}
	}
	return out, rows.Err()
}

// missingPerms lists permissions in want that the caller doesn't hold.
func missingPerms(p portalauth.Principal, want map[string]struct{}) []string {
	var miss []string
	for perm := range want {
		if _, ok := p.Permissions[perm]; !ok {
			miss = append(miss, perm)
		}
	}
	sort.Strings(miss)
	return miss
}

// guardManageUser refuses when the target user is outside the caller's
// scope or holds permissions the caller doesn't. Unknown users get 404,
// the same as users outside a restricted caller's scope, so a restricted
// admin can't probe for users they can't see.
func (h *Handler) guardManageUser(ctx context.Context, p portalauth.Principal, userID string) error {
	if p.IsVendor {
		return nil
	}
	target, found, err := h.userScope(ctx, p.CustomerID, userID)
	if err != nil {
		return err
	}
	if !found {
		return &guardError{http.StatusNotFound, "user not found"}
	}
	if userID != p.UserID {
		if caller := principalScope(p); !caller.Empty() {
			cov, err := h.coverage(ctx, p.CustomerID, caller)
			if err != nil {
				return err
			}
			if !cov.contains(target) {
				return &guardError{http.StatusNotFound, "user not found"}
			}
		}
	}
	perms, err := h.permsOf(ctx, p.CustomerID, nil, userID)
	if err != nil {
		return err
	}
	if miss := missingPerms(p, perms); len(miss) > 0 {
		return &guardError{http.StatusForbidden,
			"this user has permissions you don't have (" + strings.Join(miss, ", ") + "), so you can't change their account"}
	}
	return nil
}

// guardAssignRoles refuses roles carrying permissions the caller doesn't
// hold. Only roles being added are checked, so editing a user's name or
// removing a role doesn't trip over a role they already had.
func (h *Handler) guardAssignRoles(ctx context.Context, p portalauth.Principal, roleIDs, current []string) error {
	if p.IsVendor {
		return nil
	}
	have := map[string]bool{}
	for _, id := range current {
		have[id] = true
	}
	var adding []string
	for _, id := range roleIDs {
		if !have[id] {
			adding = append(adding, id)
		}
	}
	if len(adding) == 0 {
		return nil
	}
	perms, err := h.permsOf(ctx, p.CustomerID, adding, "")
	if err != nil {
		return err
	}
	if miss := missingPerms(p, perms); len(miss) > 0 {
		return &guardError{http.StatusForbidden,
			"you can't give a role with permissions you don't have (" + strings.Join(miss, ", ") + ")"}
	}
	return nil
}

// currentRoleIDs returns the roles a user holds now.
func (h *Handler) currentRoleIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := h.store.AdminPool().Query(ctx, `SELECT role_id::text FROM user_roles WHERE user_id::text = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
