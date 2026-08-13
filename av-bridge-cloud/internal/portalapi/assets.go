package portalapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		  LEFT JOIN devices d     ON d.asset_id = a.id AND d.deleted_at IS NULL
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
			  LEFT JOIN devices d     ON d.asset_id = a.id AND d.deleted_at IS NULL
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

// ---- CSV export / import ---------------------------------------------------

// assetCSVColumns is the canonical column order emitted on export. Import
// accepts any subset that includes the required columns (see
// requiredImportColumns) — extra columns are ignored, missing optional
// columns default to blank. This means CSVs exported before region/
// location were added still round-trip cleanly.
//
// Region + location are informational: (building, room) alone identify
// placement in a tenant with unique building names. When multiple
// buildings share a name, the import uses (region, location, building)
// to disambiguate.
var assetCSVColumns = []string{
	"asset_tag", "name", "category", "manufacturer", "model", "serial_number",
	"status", "region", "location", "building", "room",
	"purchase_date", "warranty_end", "notes",
}

// requiredImportColumns is the minimal header set the parser insists on.
// Name + category are the only fields we can't sensibly default. Missing
// any other column just means the corresponding value defaults to blank.
var requiredImportColumns = []string{"name", "category"}

// ExportAssets — GET /api/v1/assets/export.csv
//
// Streams a CSV of every asset visible to the caller. The response is a
// direct download; the portal fetches it as a blob rather than parsing.
// RLS + physical-scope RESTRICTIVE policies are honoured — a scoped
// viewer exports only their allowed buildings.
func (h *Handler) ExportAssets(w http.ResponseWriter, r *http.Request) {
	// Content headers first so downstream errors still produce a valid
	// browser download (rather than a JSON error the browser will save as
	// export.csv). This mirrors the /firmware export handler's pattern.
	filename := fmt.Sprintf("assets-%s.csv", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	if err := cw.Write(assetCSVColumns); err != nil {
		h.log.Error("csv header write failed", "error", err)
		return
	}

	// Use the same query shape as ListAssets — customers expect export
	// order to match the UI. Region + location + building + room are all
	// joined so we can emit them by name; the CSV never leaks internal
	// UUIDs.
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT COALESCE(a.asset_tag, ''),
			       a.name,
			       a.category,
			       COALESCE(a.manufacturer, ''),
			       COALESCE(a.model, ''),
			       COALESCE(a.serial_number, ''),
			       a.status,
			       COALESCE(reg.name, ''),
			       COALESCE(loc.name, ''),
			       COALESCE(b.name, ''),
			       COALESCE(rm.name, ''),
			       a.purchase_date,
			       a.warranty_end,
			       COALESCE(a.notes, '')
			  FROM assets a
			  LEFT JOIN rooms rm     ON rm.id  = a.room_id
			  LEFT JOIN buildings b  ON b.id   = rm.building_id
			  LEFT JOIN locations loc ON loc.id = b.location_id
			  LEFT JOIN regions reg  ON reg.id = loc.region_id
			 ORDER BY a.name ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				assetTag, name, category, manufacturer, model, serial, status string
				region, location, building, room, notes                        string
				purchase, warranty                                             *time.Time
			)
			if err := rows.Scan(&assetTag, &name, &category, &manufacturer,
				&model, &serial, &status,
				&region, &location, &building, &room,
				&purchase, &warranty, &notes); err != nil {
				return err
			}
			purchaseStr := ""
			if purchase != nil {
				purchaseStr = purchase.Format("2006-01-02")
			}
			warrantyStr := ""
			if warranty != nil {
				warrantyStr = warranty.Format("2006-01-02")
			}
			if err := cw.Write([]string{
				assetTag, name, category, manufacturer, model, serial,
				status, region, location, building, room,
				purchaseStr, warrantyStr, notes,
			}); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	// withTenant already surfaced 500 on error, but for CSV streaming the
	// headers have already gone — best-effort: flush what we have.
	cw.Flush()
	_ = ok
}

// importResult is the JSON response shape for /assets/import. errors
// carries row-level detail so the portal can render a table.
type importResult struct {
	Processed int              `json:"processed"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Errors    []importRowError `json:"errors"`
}

type importRowError struct {
	Row      int    `json:"row"`
	AssetTag string `json:"asset_tag,omitempty"`
	Message  string `json:"message"`
}

// ImportAssets — POST /api/v1/assets/import
//
// Multipart body with a single "file" field carrying the CSV. Columns
// must match assetCSVColumns (see export). Upsert semantics:
//
//   * blank asset_tag       → always CREATE (with fresh uuid, no tag).
//   * asset_tag not found   → CREATE with that tag.
//   * asset_tag exists      → UPDATE the existing row's fields.
//
// Validation runs on every row up front — if any row fails we return
// 400 + the error list and don't touch the DB. This is the least
// surprising failure mode for the "export, edit in Excel, re-upload"
// loop: either everything applies or nothing does.
func (h *Handler) ImportAssets(w http.ResponseWriter, r *http.Request) {
	// Cap the upload — a proper CMDB import is tiny (a few thousand rows
	// at 1KB each is ~2MB). Reject anything larger up front to avoid
	// runaway parsing.
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not parse multipart body")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()
	if header.Size > 4<<20 {
		writeErr(w, http.StatusBadRequest, "file exceeds 4MB")
		return
	}

	rows, err := parseAssetCSV(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, importResult{Errors: []importRowError{}})
		return
	}

	p, _ := portalauth.From(r.Context())
	res := importResult{Processed: len(rows), Errors: []importRowError{}}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Phase 1: build the lookup maps once. building name → id and
		// (building_id, room_name) → room_id. RLS scopes both to the
		// caller's tenant + physical scope, so a scoped viewer trying to
		// import an out-of-scope building will hit "building not found"
		// rather than silently placing rows they can't see back.
		buildingIDs, err := loadBuildingsByName(ctx, tx)
		if err != nil {
			return err
		}
		roomIDs, err := loadRoomsByBuildingAndName(ctx, tx)
		if err != nil {
			return err
		}
		// Existing asset_tag → id, so we know create vs update per row.
		existingTags, err := loadAssetsByTag(ctx, tx)
		if err != nil {
			return err
		}

		// Phase 2: validate every row. On any failure, accumulate into
		// res.Errors and DON'T write. Doing all validation before any
		// writes means the tx stays clean and we can 400-return with the
		// full picture instead of "row 42 broke halfway through".
		type resolvedRow struct {
			raw       csvAssetRow
			roomID    *string
			existing  string // asset id if this is an update
		}
		resolved := make([]resolvedRow, 0, len(rows))
		for _, row := range rows {
			rr := resolvedRow{raw: row}
			if row.Name == "" {
				res.Errors = append(res.Errors, importRowError{
					Row: row.Row, AssetTag: row.AssetTag,
					Message: "name is required",
				})
				continue
			}
			if !allowedAssetCategories[row.Category] {
				res.Errors = append(res.Errors, importRowError{
					Row: row.Row, AssetTag: row.AssetTag,
					Message: "category '" + row.Category + "' not in allowlist",
				})
				continue
			}
			if row.Status != "" && !allowedAssetStatuses[row.Status] {
				res.Errors = append(res.Errors, importRowError{
					Row: row.Row, AssetTag: row.AssetTag,
					Message: "status '" + row.Status + "' not in allowlist",
				})
				continue
			}
			if row.Building != "" || row.Room != "" {
				if row.Building == "" || row.Room == "" {
					res.Errors = append(res.Errors, importRowError{
						Row: row.Row, AssetTag: row.AssetTag,
						Message: "building and room must both be filled or both blank",
					})
					continue
				}
				matches := buildingIDs[strings.ToLower(row.Building)]
				if len(matches) == 0 {
					res.Errors = append(res.Errors, importRowError{
						Row: row.Row, AssetTag: row.AssetTag,
						Message: "building '" + row.Building + "' not found",
					})
					continue
				}
				buildingID, resolveErr := resolveBuilding(matches, row.Region, row.Location)
				if resolveErr != "" {
					res.Errors = append(res.Errors, importRowError{
						Row: row.Row, AssetTag: row.AssetTag,
						Message: "building '" + row.Building + "' " + resolveErr,
					})
					continue
				}
				rID, ok := roomIDs[buildingID+"|"+strings.ToLower(row.Room)]
				if !ok {
					res.Errors = append(res.Errors, importRowError{
						Row: row.Row, AssetTag: row.AssetTag,
						Message: "room '" + row.Room + "' not found in building '" + row.Building + "'",
					})
					continue
				}
				rr.roomID = &rID
			}
			if row.AssetTag != "" {
				if id, ok := existingTags[row.AssetTag]; ok {
					rr.existing = id
				}
			}
			resolved = append(resolved, rr)
		}
		if len(res.Errors) > 0 {
			// Bail before any writes. Nothing changed.
			return nil
		}

		// Phase 3: apply. All in one tx — either every row lands or none.
		for _, rr := range resolved {
			row := rr.raw
			status := row.Status
			if status == "" {
				status = "in_service"
			}
			purchase, _ := parseDate(row.PurchaseDate)
			warranty, _ := parseDate(row.WarrantyEnd)
			var roomParam any
			if rr.roomID != nil {
				roomParam = *rr.roomID
			}
			if rr.existing != "" {
				if _, err := tx.Exec(ctx, `
					UPDATE assets SET
					  room_id = $2::uuid,
					  name = $3, category = $4,
					  manufacturer = NULLIF($5,''), model = NULLIF($6,''), serial_number = NULLIF($7,''),
					  status = $8,
					  purchase_date = $9, warranty_end = $10,
					  notes = NULLIF($11,'')
					WHERE id = $1`,
					rr.existing, roomParam,
					row.Name, row.Category,
					row.Manufacturer, row.Model, row.Serial,
					status,
					purchase, warranty,
					row.Notes,
				); err != nil {
					return fmt.Errorf("update row %d: %w", row.Row, err)
				}
				res.Updated++
			} else {
				if _, err := tx.Exec(ctx, `
					INSERT INTO assets (
						customer_id, room_id, asset_tag,
						name, category, manufacturer, model, serial_number,
						status, purchase_date, warranty_end, notes
					) VALUES (
						$1, $2, NULLIF($3,''),
						$4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''),
						$9, $10, $11, NULLIF($12,'')
					)`,
					p.CustomerID, roomParam, row.AssetTag,
					row.Name, row.Category, row.Manufacturer, row.Model, row.Serial,
					status, purchase, warranty, row.Notes,
				); err != nil {
					return fmt.Errorf("insert row %d: %w", row.Row, err)
				}
				res.Created++
			}
		}

		// One audit row for the whole import — a per-row audit would spam
		// the trail. Metadata carries the totals so a review of the audit
		// log shows what happened without needing to reconstruct the CSV.
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "asset.import", TargetKind: "customer", TargetID: p.CustomerID,
			Metadata: map[string]any{
				"processed": res.Processed,
				"created":   res.Created,
				"updated":   res.Updated,
				"errors":    len(res.Errors),
			},
		}))
	})
	if !ok {
		return
	}
	// Row-level validation errors → 400 with the details so the operator
	// can fix the CSV. Nothing was written in this case.
	if len(res.Errors) > 0 {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// csvAssetRow is a parsed CSV row keyed by column name. Row is the 1-based
// line in the file (excluding the header) so error messages point at
// something the operator can locate in Excel.
//
// Region + Location are optional; they only affect resolution when the
// tenant has multiple buildings sharing a name, in which case they
// disambiguate. Otherwise they're carried through informationally so an
// operator glancing at a CSV knows which site each row belongs to.
type csvAssetRow struct {
	Row          int
	AssetTag     string
	Name         string
	Category     string
	Manufacturer string
	Model        string
	Serial       string
	Status       string
	Region       string
	Location     string
	Building     string
	Room         string
	PurchaseDate string
	WarrantyEnd  string
	Notes        string
}

// parseAssetCSV reads the file, validates the header, and returns each
// data row indexed 1-based (matching what Excel shows). Missing columns
// are an all-or-nothing failure — importing a truncated CSV would silently
// drop data.
func parseAssetCSV(r io.Reader) ([]csvAssetRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	// Allow the number of fields per row to vary — some editors emit
	// trailing-empty columns for blank cells and we want to accept those.
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, errors.New("CSV is empty or unreadable")
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	// Only name + category are strictly required. Every other column
	// (asset_tag, room hierarchy, dates, notes) defaults to blank when
	// absent, so CSVs exported before a column was added still import
	// cleanly.
	for _, want := range requiredImportColumns {
		if _, ok := idx[want]; !ok {
			return nil, errors.New("CSV missing required column: " + want)
		}
	}
	get := func(row []string, name string) string {
		i, ok := idx[name]
		if !ok || i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	out := []csvAssetRow{}
	rowNum := 0
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowNum+2, err) // +2: header + 1-based
		}
		rowNum++
		// Skip fully-blank rows — Excel often exports these at the tail.
		blank := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		out = append(out, csvAssetRow{
			Row:          rowNum + 1, // 1-based, +1 for the header
			AssetTag:     get(rec, "asset_tag"),
			Name:         get(rec, "name"),
			Category:     get(rec, "category"),
			Manufacturer: get(rec, "manufacturer"),
			Model:        get(rec, "model"),
			Serial:       get(rec, "serial_number"),
			Status:       get(rec, "status"),
			Region:       get(rec, "region"),
			Location:     get(rec, "location"),
			Building:     get(rec, "building"),
			Room:         get(rec, "room"),
			PurchaseDate: get(rec, "purchase_date"),
			WarrantyEnd:  get(rec, "warranty_end"),
			Notes:        get(rec, "notes"),
		})
	}
	return out, nil
}

// buildingLookup carries the hierarchy info needed to resolve a
// (region, location, building) tuple to a single id. Region + location
// are only consulted when a building name is ambiguous.
type buildingLookup struct {
	id       string
	location string
	region   string
}

// loadBuildingsByName maps a lower-cased building name to the list of
// matching entries in this tenant. Multiple matches (len > 1) are handled
// by the caller via region + location disambiguation.
func loadBuildingsByName(ctx context.Context, tx pgx.Tx) (map[string][]buildingLookup, error) {
	out := map[string][]buildingLookup{}
	rows, err := tx.Query(ctx, `
		SELECT b.id::text, b.name,
		       COALESCE(loc.name, ''),
		       COALESCE(reg.name, '')
		  FROM buildings b
		  LEFT JOIN locations loc ON loc.id = b.location_id
		  LEFT JOIN regions reg  ON reg.id = loc.region_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e buildingLookup
		var name string
		if err := rows.Scan(&e.id, &name, &e.location, &e.region); err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		out[key] = append(out[key], e)
	}
	return out, rows.Err()
}

// resolveBuilding picks a single building id from a set of same-named
// matches, using region + location to disambiguate when supplied. Returns
// an empty id + an error message suitable for a row-level import error.
func resolveBuilding(matches []buildingLookup, region, location string) (string, string) {
	if len(matches) == 0 {
		return "", "not found"
	}
	if len(matches) == 1 {
		return matches[0].id, ""
	}
	// Ambiguous — try filtering by the region/location hints. Compare
	// case-insensitively and skip empty hints so blank region/location
	// columns don't accidentally over-filter.
	filtered := matches[:0:0]
	for _, m := range matches {
		if region != "" && !strings.EqualFold(m.region, region) {
			continue
		}
		if location != "" && !strings.EqualFold(m.location, location) {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 1 {
		return filtered[0].id, ""
	}
	if len(filtered) == 0 {
		return "", "ambiguous — no match under region/location hints"
	}
	return "", "ambiguous — fill both region and location to disambiguate"
}

// loadRoomsByBuildingAndName maps "<building_id>|<lowercase room name>" to
// the room id. Composite key keeps rooms with the same name in different
// buildings distinguishable.
func loadRoomsByBuildingAndName(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	out := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT id::text, building_id::text, name FROM rooms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, buildingID, name string
		if err := rows.Scan(&id, &buildingID, &name); err != nil {
			return nil, err
		}
		out[buildingID+"|"+strings.ToLower(name)] = id
	}
	return out, rows.Err()
}

// loadAssetsByTag maps asset_tag → id for existing assets in this tenant.
// Only rows with a non-null tag; blank-tag rows can never be upserted
// (each blank-tag import row creates a fresh asset).
func loadAssetsByTag(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	out := map[string]string{}
	rows, err := tx.Query(ctx,
		`SELECT id::text, asset_tag FROM assets WHERE asset_tag IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		out[tag] = id
	}
	return out, rows.Err()
}
