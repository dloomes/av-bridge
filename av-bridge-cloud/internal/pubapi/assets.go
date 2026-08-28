package pubapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Public asset shape — deliberately smaller than portalapi.assetRow.
// The public contract elides fields that only matter to portal editors
// (created_at UUID authors, edit hints) and surfaces the identity +
// placement + provenance fields an integrating CMDB actually needs.
//
// Every field here is a stable contract — renames become breaking
// changes. Anything experimental should land on /pub/v2.
//
// Assets differ from devices in two important ways:
//
//   - An asset row can exist without a corresponding monitored device
//     (a wall mount, a spare cable). Its Monitored flag reflects that;
//     DeviceID points at the paired device row when present.
//   - Assets can also be "unplaced" (room_id NULL) — decommissioned or
//     in-storage inventory. Scoped users don't see unplaced rows;
//     unscoped tokens (v1 default) do.
type publicAsset struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Category        string     `json:"category"`
	Status          string     `json:"status"`
	AssetTag        string     `json:"asset_tag,omitempty"`
	Manufacturer    string     `json:"manufacturer,omitempty"`
	Model           string     `json:"model,omitempty"`
	SerialNumber    string     `json:"serial_number,omitempty"`
	PurchaseDate    string     `json:"purchase_date,omitempty"` // YYYY-MM-DD, blank if unset
	WarrantyEndDate string     `json:"warranty_end_date,omitempty"`
	Notes           string     `json:"notes,omitempty"`
	RoomID          string     `json:"room_id,omitempty"`
	Room            string     `json:"room,omitempty"`
	BuildingID      string     `json:"building_id,omitempty"`
	Building        string     `json:"building,omitempty"`
	Monitored       bool       `json:"monitored"`
	DeviceID        string     `json:"device_id,omitempty"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

// scanAsset reads a single publicAsset off pgx.Row. Shared between the
// list and detail paths so field order stays locked in one place.
// Date columns are the DB `date` type; format as YYYY-MM-DD strings so
// integrators don't have to peel off a spurious time component.
func scanAsset(scan func(dest ...any) error) (publicAsset, error) {
	var (
		a             publicAsset
		purchase      *time.Time
		warranty      *time.Time
		hasDevice     *string
	)
	if err := scan(
		&a.ID, &a.Name, &a.Category, &a.Status,
		&a.AssetTag, &a.Manufacturer, &a.Model, &a.SerialNumber,
		&purchase, &warranty, &a.Notes,
		&a.RoomID, &a.Room, &a.BuildingID, &a.Building,
		&hasDevice,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return a, err
	}
	if purchase != nil {
		a.PurchaseDate = purchase.Format("2006-01-02")
	}
	if warranty != nil {
		a.WarrantyEndDate = warranty.Format("2006-01-02")
	}
	if hasDevice != nil && *hasDevice != "" {
		a.DeviceID = *hasDevice
		a.Monitored = true
	}
	return a, nil
}

// publicAssetBaseSelect is the shared SELECT projection. Kept as a
// package-level string so both ListAssets and GetAsset produce
// identical row shapes — the scanner above depends on the column
// order.
const publicAssetBaseSelect = `
	SELECT a.id::text,
	       a.name,
	       a.category,
	       a.status,
	       COALESCE(a.asset_tag, ''),
	       COALESCE(a.manufacturer, ''),
	       COALESCE(a.model, ''),
	       COALESCE(a.serial_number, ''),
	       a.purchase_date,
	       a.warranty_end,
	       COALESCE(a.notes, ''),
	       COALESCE(a.room_id::text, ''),
	       COALESCE(rm.name, ''),
	       COALESCE(b.id::text, ''),
	       COALESCE(b.name, ''),
	       d.id::text,
	       a.created_at,
	       a.updated_at
	  FROM assets a
	  LEFT JOIN rooms rm      ON rm.id  = a.room_id
	  LEFT JOIN buildings b   ON b.id   = rm.building_id
	  LEFT JOIN devices d     ON d.asset_id = a.id AND d.deleted_at IS NULL`

// ListAssets — GET /pub/v1/assets
//
// Filters (all optional, all query-string):
//
//	building_id = <uuid>           only assets in that building
//	room_id     = <uuid>           only assets in that room
//	category    = <string>         exact category match (display, mount, …)
//	status      = <string>         exact status match (in_service, in_storage, …)
//	monitored   = true|false       has / hasn't a linked device row
//	cursor + limit                 standard pagination
//
// Requires the view.assets scope. Ordered by (updated_at, id) so
// integrators polling for recently-changed CMDB entries can page in a
// predictable order.
func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)

	buildingFilter := strings.TrimSpace(r.URL.Query().Get("building_id"))
	roomFilter := strings.TrimSpace(r.URL.Query().Get("room_id"))
	categoryFilter := strings.TrimSpace(r.URL.Query().Get("category"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	monitoredFilter := strings.TrimSpace(r.URL.Query().Get("monitored"))

	sql := publicAssetBaseSelect + " WHERE 1=1"
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}

	if buildingFilter != "" {
		sql += " AND rm.building_id::text = " + arg(buildingFilter)
	}
	if roomFilter != "" {
		sql += " AND a.room_id::text = " + arg(roomFilter)
	}
	if categoryFilter != "" {
		sql += " AND a.category = " + arg(categoryFilter)
	}
	if statusFilter != "" {
		sql += " AND a.status = " + arg(statusFilter)
	}
	// monitored=true → only assets with a linked device row.
	// monitored=false → only assets WITHOUT one. Any other value is
	// silently ignored so a caller passing monitored=maybe gets the
	// unfiltered list rather than an error.
	switch monitoredFilter {
	case "true":
		sql += " AND d.id IS NOT NULL"
	case "false":
		sql += " AND d.id IS NULL"
	}
	if cursor.TS != nil {
		tsP := arg(*cursor.TS)
		idP := arg(cursor.ID)
		sql += " AND (a.updated_at, a.id::text) < (" + tsP + ", " + idP + ")"
	}
	sql += " ORDER BY a.updated_at DESC, a.id::text DESC LIMIT " + arg(limit+1) + "::int"

	out := []publicAsset{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	var nextCursor *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: last.UpdatedAt, ID: last.ID})
		nextCursor = &nc
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, Page(out, nextCursor))
}

// GetAsset — GET /pub/v1/assets/{id}
//
// Requires view.assets. Returns the same shape as list. 404 when the id
// isn't a UUID this tenant owns (RLS handles cross-tenant visibility;
// invalid UUIDs come back as 404 too rather than 400, so an integrator
// probing an unknown id doesn't leak "does this format exist" signal).
func (h *Handler) GetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "asset id required")
		return
	}
	var (
		a        publicAsset
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, publicAssetBaseSelect+" WHERE a.id::text = $1", id)
		aa, err := scanAsset(row.Scan)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			// Bad UUID cast surfaces as SQLSTATE 22P02; treat as
			// not-found rather than leaking a stack of "invalid input
			// syntax" back to the caller.
			if strings.Contains(err.Error(), "22P02") {
				notFound = true
				return nil
			}
			return err
		}
		a = aa
		return nil
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
}
