package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Slice 9 — CMDB endpoints. An "asset" is a physical thing the tenant
// owns: monitored (paired with a devices row via devices.asset_id) or not
// (a mount, a cable, a stack of remotes in a cupboard).
//
// RLS + the RESTRICTIVE building_scope policy from migration 0022 handle
// tenant isolation and physical scope; handlers don't repeat customer_id
// filters. Vendor callers passing X-Customer-Scope pass through the
// existing withTenant helper.

// Category + status allowlists mirror what the portal offers. Keeping
// them in the handler layer (rather than a DB CHECK) means adding a new
// category doesn't need a migration.
var allowedAssetCategories = map[string]bool{
	"display":       true,
	"camera":        true,
	"audio":         true,
	"conferencing":  true,
	"control_panel": true,
	"touch_panel":   true,
	"cable":         true,
	"mount":         true,
	"rack":          true,
	"remote":        true,
	"microphone":    true,
	"speaker":       true,
	"projector":     true,
	"screen":        true,
	"computer":      true,
	"furniture":     true,
	"storage":       true,
	"other":         true,
}

var allowedAssetStatuses = map[string]bool{
	"in_service": true,
	"in_storage": true,
	"retired":    true,
	"in_repair":  true,
}

// assetRow is the shape returned by list + detail. Composed to include the
// hierarchy breadcrumb (region/location/building/room) so a single call
// gives the portal enough to render a list without follow-up joins, and
// device_id so a "monitored" badge can render without a second lookup.
type assetRow struct {
	ID           string  `json:"id"`
	AssetTag     string  `json:"asset_tag,omitempty"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	Model        string  `json:"model,omitempty"`
	SerialNumber string  `json:"serial_number,omitempty"`
	Status       string  `json:"status"`
	RoomID       *string `json:"room_id,omitempty"`
	Room         string  `json:"room,omitempty"`
	Building     string  `json:"building,omitempty"`
	Location     string  `json:"location,omitempty"`
	Region       string  `json:"region,omitempty"`
	PurchaseDate *string `json:"purchase_date,omitempty"`
	WarrantyEnd  *string `json:"warranty_end,omitempty"`
	Notes        string  `json:"notes,omitempty"`
	DeviceID     *string `json:"device_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// Format a nullable date column as "YYYY-MM-DD" (ISO date, no time). The
// portal renders these directly; keeping them as strings avoids timezone
// confusion for a date-only field.
func dateStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// ListAssets — GET /api/v1/assets
//
// Query params:
//   - room_id=<uuid>        — restrict to a single room
//   - building_id=<uuid>    — restrict to a building (joins through rooms)
//   - category=<name>       — one category
//   - status=<name>         — one status
//   - unplaced=true         — only assets with no room
//   - q=<search>            — ILIKE match against name / model / serial / asset_tag
//   - limit=N               — default 200, max 1000
//
// Combining filters is AND. All are optional.
func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(r, "limit", 200, 1000)

	sql := `
		SELECT a.id::text,
		       COALESCE(a.asset_tag, ''),
		       a.name,
		       a.category,
		       COALESCE(a.manufacturer, ''),
		       COALESCE(a.model, ''),
		       COALESCE(a.serial_number, ''),
		       a.status,
		       a.room_id::text,
		       COALESCE(rm.name, ''),
		       COALESCE(b.name, ''),
		       COALESCE(loc.name, ''),
		       COALESCE(reg.name, ''),
		       a.purchase_date,
		       a.warranty_end,
		       COALESCE(a.notes, ''),
		       d.id::text,
		       a.created_at,
		       a.updated_at
		  FROM assets a
		  LEFT JOIN rooms rm      ON rm.id  = a.room_id
		  LEFT JOIN buildings b   ON b.id   = rm.building_id
		  LEFT JOIN locations loc ON loc.id = b.location_id
		  LEFT JOIN regions reg   ON reg.id = loc.region_id
		  LEFT JOIN devices d     ON d.asset_id = a.id
	`
	conds := []string{}
	args := []any{}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, strings.Replace(cond, "$?", "$"+strconv.Itoa(len(args)), 1))
	}
	if v := q.Get("room_id"); v != "" {
		add("a.room_id = $?::uuid", v)
	}
	if v := q.Get("building_id"); v != "" {
		add("rm.building_id = $?::uuid", v)
	}
	if v := q.Get("category"); v != "" {
		add("a.category = $?", v)
	}
	if v := q.Get("status"); v != "" {
		add("a.status = $?", v)
	}
	if q.Get("unplaced") == "true" {
		conds = append(conds, "a.room_id IS NULL")
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		args = append(args, "%"+v+"%")
		idx := "$" + strconv.Itoa(len(args))
		conds = append(conds,
			"(a.name ILIKE "+idx+" OR a.model ILIKE "+idx+
				" OR a.serial_number ILIKE "+idx+
				" OR a.asset_tag ILIKE "+idx+")")
	}
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	sql += " ORDER BY a.name ASC LIMIT $" + strconv.Itoa(len(args))

	out := []assetRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				it       assetRow
				purchase *time.Time
				warranty *time.Time
				created  time.Time
				updated  time.Time
			)
			if err := rows.Scan(&it.ID, &it.AssetTag, &it.Name, &it.Category,
				&it.Manufacturer, &it.Model, &it.SerialNumber, &it.Status,
				&it.RoomID, &it.Room, &it.Building, &it.Location, &it.Region,
				&purchase, &warranty, &it.Notes, &it.DeviceID,
				&created, &updated); err != nil {
				return err
			}
			it.PurchaseDate = dateStr(purchase)
			it.WarrantyEnd = dateStr(warranty)
			it.CreatedAt = created.UTC().Format(time.RFC3339)
			it.UpdatedAt = updated.UTC().Format(time.RFC3339)
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetAsset — GET /api/v1/assets/{id}
func (h *Handler) GetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var (
		it       assetRow
		found    bool
		purchase *time.Time
		warranty *time.Time
		created  time.Time
		updated  time.Time
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT a.id::text,
			       COALESCE(a.asset_tag, ''),
			       a.name,
			       a.category,
			       COALESCE(a.manufacturer, ''),
			       COALESCE(a.model, ''),
			       COALESCE(a.serial_number, ''),
			       a.status,
			       a.room_id::text,
			       COALESCE(rm.name, ''),
			       COALESCE(b.name, ''),
			       COALESCE(loc.name, ''),
			       COALESCE(reg.name, ''),
			       a.purchase_date,
			       a.warranty_end,
			       COALESCE(a.notes, ''),
			       d.id::text,
			       a.created_at,
			       a.updated_at
			  FROM assets a
			  LEFT JOIN rooms rm      ON rm.id  = a.room_id
			  LEFT JOIN buildings b   ON b.id   = rm.building_id
			  LEFT JOIN locations loc ON loc.id = b.location_id
			  LEFT JOIN regions reg   ON reg.id = loc.region_id
			  LEFT JOIN devices d     ON d.asset_id = a.id
			 WHERE a.id = $1`, id).Scan(
			&it.ID, &it.AssetTag, &it.Name, &it.Category,
			&it.Manufacturer, &it.Model, &it.SerialNumber, &it.Status,
			&it.RoomID, &it.Room, &it.Building, &it.Location, &it.Region,
			&purchase, &warranty, &it.Notes, &it.DeviceID,
			&created, &updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		it.PurchaseDate = dateStr(purchase)
		it.WarrantyEnd = dateStr(warranty)
		it.CreatedAt = created.UTC().Format(time.RFC3339)
		it.UpdatedAt = updated.UTC().Format(time.RFC3339)
		return nil
	})
	if !ok {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// createAssetReq mirrors the fields the create form supplies. Date fields
// come in as "YYYY-MM-DD" strings — pointer so an empty payload leaves
// them NULL.
type createAssetReq struct {
	AssetTag     string  `json:"asset_tag,omitempty"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	Model        string  `json:"model,omitempty"`
	SerialNumber string  `json:"serial_number,omitempty"`
	Status       string  `json:"status,omitempty"`
	RoomID       string  `json:"room_id,omitempty"`
	PurchaseDate string  `json:"purchase_date,omitempty"`
	WarrantyEnd  string  `json:"warranty_end,omitempty"`
	Notes        string  `json:"notes,omitempty"`
}

// parseDate returns a *time.Time for a YYYY-MM-DD string, nil for empty.
// A malformed date is a user error (400) — the caller reports it.
func parseDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, errors.New("date must be YYYY-MM-DD")
	}
	return &t, nil
}

// CreateAsset — POST /api/v1/assets
func (h *Handler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	var req createAssetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Category = strings.TrimSpace(req.Category)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if !allowedAssetCategories[req.Category] {
		writeErr(w, http.StatusBadRequest, "unsupported category")
		return
	}
	if req.Status == "" {
		req.Status = "in_service"
	}
	if !allowedAssetStatuses[req.Status] {
		writeErr(w, http.StatusBadRequest, "unsupported status")
		return
	}
	purchase, err := parseDate(req.PurchaseDate)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "purchase_date: "+err.Error())
		return
	}
	warranty, err := parseDate(req.WarrantyEnd)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "warranty_end: "+err.Error())
		return
	}

	p, _ := portalauth.From(r.Context())
	var id string
	var roomBad, duplicateTag bool

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var roomParam any
		if req.RoomID != "" {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, req.RoomID,
			).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				roomBad = true
				return nil
			}
			roomParam = req.RoomID
		}
		// Pre-check asset_tag uniqueness. Doing this ahead of the INSERT
		// keeps the transaction healthy on a conflict; if we let the
		// unique-violation fire from the DB, the tx enters the aborted
		// state and the subsequent audit write / commit blow up.
		if req.AssetTag != "" {
			var taken bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM assets WHERE asset_tag = $1)`,
				req.AssetTag,
			).Scan(&taken); err != nil {
				return err
			}
			if taken {
				duplicateTag = true
				return nil
			}
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO assets (
				customer_id, room_id, asset_tag,
				name, category, manufacturer, model, serial_number,
				status, purchase_date, warranty_end, notes
			) VALUES (
				$1, $2, NULLIF($3,''),
				$4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''),
				$9, $10, $11, NULLIF($12,'')
			) RETURNING id::text`,
			p.CustomerID, roomParam, req.AssetTag,
			req.Name, req.Category, req.Manufacturer, req.Model, req.SerialNumber,
			req.Status, purchase, warranty, req.Notes,
		).Scan(&id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "asset.create", TargetKind: "asset", TargetID: id,
			After: mustJSON(map[string]any{
				"name":     req.Name,
				"category": req.Category,
				"room_id":  req.RoomID,
				"status":   req.Status,
			}),
		}))
	})
	if !ok {
		return
	}
	if roomBad {
		writeErr(w, http.StatusBadRequest, "room_id not found in this customer")
		return
	}
	if duplicateTag {
		writeErr(w, http.StatusConflict, "asset_tag already exists in this tenant")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// updateAssetReq — pointer per field so we can distinguish "not in payload"
// from "clear to null". Same pattern as the device / user update flows.
type updateAssetReq struct {
	AssetTag     *string `json:"asset_tag,omitempty"`
	Name         *string `json:"name,omitempty"`
	Category     *string `json:"category,omitempty"`
	Manufacturer *string `json:"manufacturer,omitempty"`
	Model        *string `json:"model,omitempty"`
	SerialNumber *string `json:"serial_number,omitempty"`
	Status       *string `json:"status,omitempty"`
	RoomID       *string `json:"room_id,omitempty"`
	PurchaseDate *string `json:"purchase_date,omitempty"`
	WarrantyEnd  *string `json:"warranty_end,omitempty"`
	Notes        *string `json:"notes,omitempty"`
}

// UpdateAsset — PATCH /api/v1/assets/{id}
func (h *Handler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateAssetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name cannot be empty")
		return
	}
	if req.Category != nil && !allowedAssetCategories[*req.Category] {
		writeErr(w, http.StatusBadRequest, "unsupported category")
		return
	}
	if req.Status != nil && !allowedAssetStatuses[*req.Status] {
		writeErr(w, http.StatusBadRequest, "unsupported status")
		return
	}
	var purchase, warranty *time.Time
	if req.PurchaseDate != nil {
		if *req.PurchaseDate == "" {
			// explicit clear
			purchase = nil
		} else {
			t, err := parseDate(*req.PurchaseDate)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "purchase_date: "+err.Error())
				return
			}
			purchase = t
		}
	}
	if req.WarrantyEnd != nil {
		if *req.WarrantyEnd == "" {
			warranty = nil
		} else {
			t, err := parseDate(*req.WarrantyEnd)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "warranty_end: "+err.Error())
				return
			}
			warranty = t
		}
	}

	set := []string{}
	args := []any{id}
	add := func(col string, val any) {
		args = append(args, val)
		set = append(set, col+" = $"+strconv.Itoa(len(args)))
	}
	if req.AssetTag != nil {
		if strings.TrimSpace(*req.AssetTag) == "" {
			add("asset_tag", nil)
		} else {
			add("asset_tag", *req.AssetTag)
		}
	}
	if req.Name != nil {
		add("name", strings.TrimSpace(*req.Name))
	}
	if req.Category != nil {
		add("category", *req.Category)
	}
	if req.Manufacturer != nil {
		add("manufacturer", nullIfEmpty(*req.Manufacturer))
	}
	if req.Model != nil {
		add("model", nullIfEmpty(*req.Model))
	}
	if req.SerialNumber != nil {
		add("serial_number", nullIfEmpty(*req.SerialNumber))
	}
	if req.Status != nil {
		add("status", *req.Status)
	}
	if req.RoomID != nil {
		if *req.RoomID == "" {
			add("room_id", nil)
		} else {
			add("room_id", *req.RoomID)
		}
	}
	if req.PurchaseDate != nil {
		add("purchase_date", purchase)
	}
	if req.WarrantyEnd != nil {
		add("warranty_end", warranty)
	}
	if req.Notes != nil {
		add("notes", nullIfEmpty(*req.Notes))
	}
	if len(set) == 0 {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	sql := "UPDATE assets SET " + strings.Join(set, ", ") + " WHERE id = $1"
	p, _ := portalauth.From(r.Context())
	var rowsAffected int64
	var roomBad, duplicateTag bool

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if req.RoomID != nil && *req.RoomID != "" {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, *req.RoomID,
			).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				roomBad = true
				return nil
			}
		}
		// Pre-check asset_tag uniqueness against OTHER rows. Same rationale
		// as CreateAsset — letting the unique-violation fire from the DB
		// poisons the tx and the audit write below breaks.
		if req.AssetTag != nil && strings.TrimSpace(*req.AssetTag) != "" {
			var taken bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM assets WHERE asset_tag = $1 AND id <> $2)`,
				*req.AssetTag, id,
			).Scan(&taken); err != nil {
				return err
			}
			if taken {
				duplicateTag = true
				return nil
			}
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "asset.update", TargetKind: "asset", TargetID: id,
			After: mustJSON(map[string]any{"fields_changed": len(set)}),
		}))
	})
	if !ok {
		return
	}
	if roomBad {
		writeErr(w, http.StatusBadRequest, "room_id not found in this customer")
		return
	}
	if duplicateTag {
		writeErr(w, http.StatusConflict, "asset_tag already exists in this tenant")
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// DeleteAsset — DELETE /api/v1/assets/{id}
//
// The devices FK is ON DELETE SET NULL — a device paired with this asset
// stays monitored, just loses the CMDB link. That's the right default:
// the AV team can rebuild the asset row later without disrupting live
// monitoring.
func (h *Handler) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())
	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM assets WHERE id = $1`, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM assets WHERE id = $1`, id)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "asset.delete", TargetKind: "asset", TargetID: id,
			Before: mustJSON(map[string]any{"name": name}),
		}))
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
