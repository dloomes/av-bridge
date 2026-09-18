package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Business Units — optional top-of-hierarchy tier introduced in migration
// 0043. Feature-flagged per tenant via customers.business_units_enabled;
// write endpoints reject when the flag is off so a tenant admin cannot
// accidentally start using the tier without vendor sign-off.
//
// The list endpoint is open to any authenticated user in the tenant — the
// portal's sidebar tree needs to know whether to render a BU column. When
// the flag is off, the list is always empty (no rows can exist yet).

type businessUnitRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type createBusinessUnitReq struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type updateBusinessUnitReq struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// ListBusinessUnits — GET /api/v1/business-units
//
// Any authenticated user in the tenant may list; the sidebar renders from
// this, and hiding it from viewers would create a "why is my region under
// nothing?" UX gap. RLS handles the tenant filter; BU-scoped users see
// only their assigned BUs via the migration 0043 tenant policy.
//
// Returns an empty list (200) when the flag is off — the portal treats
// that as "no BUs available", which is exactly the right rendering.
func (h *Handler) ListBusinessUnits(w http.ResponseWriter, r *http.Request) {
	out := []businessUnitRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, COALESCE(description, ''),
			       created_at, updated_at
			  FROM business_units
			 ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row      businessUnitRow
				created  string
				updated  string
			)
			if err := rows.Scan(&row.ID, &row.Name, &row.Description, &created, &updated); err != nil {
				return err
			}
			row.CreatedAt = created
			row.UpdatedAt = updated
			out = append(out, row)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateBusinessUnit — POST /api/v1/business-units
//
// Gated on business_unit.crud AND the tenant flag. Off-flag callers get
// a 400 with an actionable message rather than a mystery 500.
func (h *Handler) CreateBusinessUnit(w http.ResponseWriter, r *http.Request) {
	var req createBusinessUnitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var (
		id             string
		flagDisabled   bool
		conflictOnName bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx,
			`SELECT business_units_enabled FROM customers WHERE id = $1`,
			p.CustomerID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			flagDisabled = true
			return nil
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO business_units (customer_id, name, description)
			VALUES ($1, $2, NULLIF($3, ''))
			RETURNING id::text`,
			p.CustomerID, req.Name, req.Description).Scan(&id)
		if err != nil {
			// unique_violation on (customer_id, name) — 23505
			var pgErr interface{ SQLState() string }
			if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
				conflictOnName = true
				return nil
			}
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "business_units", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "business_unit.create", TargetKind: "business_unit",
			TargetID: id, After: after,
		}))
	})
	if !ok {
		return
	}
	if flagDisabled {
		writeErr(w, http.StatusBadRequest,
			"business units are not enabled for this tenant — contact your Involve Cloud administrator")
		return
	}
	if conflictOnName {
		writeErr(w, http.StatusConflict, "a business unit with that name already exists")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

// UpdateBusinessUnit — PATCH /api/v1/business-units/{id}
//
// Pointer-per-field PATCH: only fields present in the payload are touched.
// An explicit empty-string description clears it. Name cannot be empty.
func (h *Handler) UpdateBusinessUnit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	var req updateBusinessUnitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name cannot be empty")
		return
	}
	if req.Name == nil && req.Description == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	p, _ := portalauth.From(r.Context())

	var (
		flagDisabled bool
		notFound     bool
		conflict     bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx,
			`SELECT business_units_enabled FROM customers WHERE id = $1`,
			p.CustomerID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			flagDisabled = true
			return nil
		}
		before, err := audit.SnapshotByTable(ctx, tx, "business_units", id)
		if err != nil {
			return err
		}
		if before == nil {
			notFound = true
			return nil
		}
		// Build the dynamic SET clause from the fields that are present.
		set := []string{}
		args := []any{id}
		add := func(col string, v any) {
			args = append(args, v)
			set = append(set, col+" = $"+strconv.Itoa(len(args)))
		}
		if req.Name != nil {
			add("name", *req.Name)
		}
		if req.Description != nil {
			if *req.Description == "" {
				add("description", nil)
			} else {
				add("description", *req.Description)
			}
		}
		sql := "UPDATE business_units SET " + strings.Join(set, ", ") + " WHERE id = $1"
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			var pgErr interface{ SQLState() string }
			if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
				conflict = true
				return nil
			}
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "business_units", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "business_unit.update", TargetKind: "business_unit",
			TargetID: id, Before: before, After: after,
		}))
	})
	if !ok {
		return
	}
	if flagDisabled {
		writeErr(w, http.StatusBadRequest,
			"business units are not enabled for this tenant")
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "business unit not found")
		return
	}
	if conflict {
		writeErr(w, http.StatusConflict, "a business unit with that name already exists")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteBusinessUnit — DELETE /api/v1/business-units/{id}
//
// FK on regions.business_unit_id is ON DELETE SET NULL, so deletion never
// cascades into structural data. Contained regions revert to unassigned,
// visible again to any unscoped caller and hidden from BU-scoped users.
// A subsequent list will show no regions under the deleted BU.
func (h *Handler) DeleteBusinessUnit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var (
		flagDisabled bool
		notFound     bool
		orphaned     int
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx,
			`SELECT business_units_enabled FROM customers WHERE id = $1`,
			p.CustomerID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			flagDisabled = true
			return nil
		}
		before, err := audit.SnapshotByTable(ctx, tx, "business_units", id)
		if err != nil {
			return err
		}
		if before == nil {
			notFound = true
			return nil
		}
		// Record how many regions will be orphaned so the audit trail
		// captures the structural fallout, not just the row deletion.
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM regions WHERE business_unit_id = $1`, id).
			Scan(&orphaned); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM business_units WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "business_unit.delete", TargetKind: "business_unit",
			TargetID: id, Before: before,
			After: mustJSON(map[string]any{
				"regions_orphaned": orphaned,
			}),
		}))
	})
	if !ok {
		return
	}
	if flagDisabled {
		writeErr(w, http.StatusBadRequest,
			"business units are not enabled for this tenant")
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "business unit not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               id,
		"regions_orphaned": orphaned,
	})
}

// SetCustomerBusinessUnitsEnabled — POST /api/v1/admin/customers/{id}/business-units-enabled
//
// Vendor-only flag toggle. Enables (or disables) the Business Unit tier
// for a tenant. Disable is safe: existing BU rows and regions.business_unit_id
// assignments stay in place — the flag only controls whether the portal
// surfaces the concept and whether write endpoints accept BU-touching
// requests. A subsequent re-enable brings the existing data back into view.
func (h *Handler) SetCustomerBusinessUnitsEnabled(w http.ResponseWriter, r *http.Request) {
	customerID := r.PathValue("id")
	if customerID == "" {
		writeErr(w, http.StatusBadRequest, "customer id required")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	p, _ := portalauth.From(r.Context())

	// Vendor callers act cross-tenant; use WithTenant scoped to the
	// target customer so the audit row lands in that tenant's log.
	//
	// Audit records the flag delta only — the full customers row carries
	// unrelated fields (branding secrets, entra config) that don't belong
	// in this audit context, so we do not use SnapshotByTable here.
	var notFound bool
	ctx := r.Context()
	err := h.store.WithTenant(ctx, customerID, func(tx pgx.Tx) error {
		var prevEnabled bool
		err := tx.QueryRow(ctx,
			`SELECT business_units_enabled FROM customers WHERE id = $1`,
			customerID).Scan(&prevEnabled)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE customers SET business_units_enabled = $2 WHERE id = $1`,
			customerID, req.Enabled); err != nil {
			return err
		}
		return audit.Record(ctx, tx, customerID, stampActor(p, audit.Entry{
			Action:     "customer.business_units_enabled",
			TargetKind: "customer",
			TargetID:   customerID,
			Before:     mustJSON(map[string]any{"business_units_enabled": prevEnabled}),
			After:      mustJSON(map[string]any{"business_units_enabled": req.Enabled}),
		}))
	})
	if err != nil {
		h.log.Error("set business_units_enabled failed", "customer", customerID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer_id":            customerID,
		"business_units_enabled": req.Enabled,
	})
}

