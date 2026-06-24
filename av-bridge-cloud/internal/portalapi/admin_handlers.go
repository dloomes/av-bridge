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

// ----- hierarchy: regions / locations / buildings / rooms ---------------------
//
// Customer Admin capability per the SPEC roles matrix. Each create endpoint
// uses RLS-aware existence checks to confirm any referenced parent belongs to
// the same customer — a UUID guess for another tenant's row simply fails
// EXISTS and the INSERT writes 0 rows.

type createRegionReq struct {
	Name string `json:"name"`
}
type createLocationReq struct {
	RegionID string `json:"region_id"`
	Name     string `json:"name"`
}
type createBuildingReq struct {
	LocationID string `json:"location_id"`
	Name       string `json:"name"`
	Address    string `json:"address,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
}
type createRoomReq struct {
	BuildingID string `json:"building_id"`
	Name       string `json:"name"`
}

func (h *Handler) CreateRegion(w http.ResponseWriter, r *http.Request) {
	var req createRegionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var id string
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO regions (customer_id, name) VALUES ($1, $2) RETURNING id::text`,
			p.CustomerID, req.Name).Scan(&id); err != nil {
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "regions", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "region.create", TargetKind: "region", TargetID: id, After: after,
		})
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

func (h *Handler) CreateLocation(w http.ResponseWriter, r *http.Request) {
	var req createLocationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RegionID == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "region_id and name are required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var id string
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO locations (customer_id, region_id, name)
			SELECT $1, $2, $3
			WHERE EXISTS (SELECT 1 FROM regions WHERE id = $2)
			RETURNING id::text`,
			p.CustomerID, req.RegionID, req.Name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "locations", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "location.create", TargetKind: "location", TargetID: id, After: after,
		})
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusBadRequest, "region_id not found in this customer")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

func (h *Handler) CreateBuilding(w http.ResponseWriter, r *http.Request) {
	var req createBuildingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LocationID == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "location_id and name are required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var id string
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO buildings (customer_id, location_id, name, address, timezone)
			SELECT $1, $2, $3, NULLIF($4,''), NULLIF($5,'')
			WHERE EXISTS (SELECT 1 FROM locations WHERE id = $2)
			RETURNING id::text`,
			p.CustomerID, req.LocationID, req.Name, req.Address, req.Timezone).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "buildings", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "building.create", TargetKind: "building", TargetID: id, After: after,
		})
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusBadRequest, "location_id not found in this customer")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

func (h *Handler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BuildingID == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "building_id and name are required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var id string
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO rooms (customer_id, building_id, name)
			SELECT $1, $2, $3
			WHERE EXISTS (SELECT 1 FROM buildings WHERE id = $2)
			RETURNING id::text`,
			p.CustomerID, req.BuildingID, req.Name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "rooms", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "room.create", TargetKind: "room", TargetID: id, After: after,
		})
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusBadRequest, "building_id not found in this customer")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

// ----- list (any authed user, RLS-scoped) -----------------------------------

type namedRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
}

func (h *Handler) listSimple(w http.ResponseWriter, r *http.Request, sql string) {
	out := []namedRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr namedRow
			if err := rows.Scan(&rr.ID, &rr.Name, &rr.ParentID); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) ListRegions(w http.ResponseWriter, r *http.Request) {
	h.listSimple(w, r, `SELECT id::text, name, '' FROM regions ORDER BY name`)
}
func (h *Handler) ListLocations(w http.ResponseWriter, r *http.Request) {
	h.listSimple(w, r, `SELECT id::text, name, region_id::text FROM locations ORDER BY name`)
}
func (h *Handler) ListBuildings(w http.ResponseWriter, r *http.Request) {
	// Buildings carry address + timezone in addition to the shared fields, so
	// they need their own row shape rather than the generic listSimple path.
	type buildingRow struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
		Address  string `json:"address,omitempty"`
		Timezone string `json:"timezone,omitempty"`
	}
	out := []buildingRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, location_id::text,
			       COALESCE(address,'') AS address,
			       COALESCE(timezone,'') AS timezone
			FROM buildings ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr buildingRow
			if err := rows.Scan(&rr.ID, &rr.Name, &rr.ParentID, &rr.Address, &rr.Timezone); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) ListRooms(w http.ResponseWriter, r *http.Request) {
	h.listSimple(w, r, `SELECT id::text, name, building_id::text FROM rooms ORDER BY name`)
}

// ----- hierarchy updates -----------------------------------------------------
//
// Region/location/room are simple-rename PATCHes. Building also takes optional
// address + timezone. All gated to admin role at the route layer.

type updateNamedReq struct {
	Name *string `json:"name,omitempty"`
}

type updateBuildingReq struct {
	Name     *string `json:"name,omitempty"`
	Address  *string `json:"address,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}

// updateSimpleNamed is the shared workhorse for the three name-only tables.
// The table + action names are the only things that change between them, so
// we parameterise rather than copy three near-identical handlers.
func (h *Handler) updateSimpleNamed(w http.ResponseWriter, r *http.Request, table, kind string) {
	id := r.PathValue("id")
	var req updateNamedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == nil || *req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotByTable(ctx, tx, table, id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			"UPDATE "+table+" SET name = $1 WHERE id = $2", *req.Name, id)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		after, err := audit.SnapshotByTable(ctx, tx, table, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: kind + ".update", TargetKind: kind, TargetID: id,
			Before: before, After: after,
		})
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, kind+" not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": *req.Name})
}

func (h *Handler) UpdateRegion(w http.ResponseWriter, r *http.Request) {
	h.updateSimpleNamed(w, r, "regions", "region")
}
func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	h.updateSimpleNamed(w, r, "locations", "location")
}
func (h *Handler) UpdateRoom(w http.ResponseWriter, r *http.Request) {
	h.updateSimpleNamed(w, r, "rooms", "room")
}

// UpdateBuilding — PATCH /api/v1/buildings/{id}
//
// Pointer-per-field PATCH so callers can rename without touching address, or
// clear a field by sending it as an empty string.
func (h *Handler) UpdateBuilding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateBuildingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name cannot be empty")
		return
	}

	set := []string{}
	args := []any{}
	add := func(col string, val any) {
		args = append(args, val)
		set = append(set, col+" = $"+strconv.Itoa(len(args)))
	}
	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Address != nil {
		add("address", nullIfEmpty(*req.Address))
	}
	if req.Timezone != nil {
		add("timezone", nullIfEmpty(*req.Timezone))
	}
	if len(set) == 0 {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	sql := "UPDATE buildings SET " + strings.Join(set, ", ") + " WHERE id = $" + strconv.Itoa(len(args))

	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotByTable(ctx, tx, "buildings", id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		after, err := audit.SnapshotByTable(ctx, tx, "buildings", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "building.update", TargetKind: "building", TargetID: id,
			Before: before, After: after,
		})
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "building not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// ----- hierarchy deletes -----------------------------------------------------
//
// Each delete cascades down (region → location → building → room) and orphans
// affected devices (devices.room_id FK is ON DELETE SET NULL — see migration
// 0001). The before-snapshot + audit metadata together capture the row that
// was removed and how many descendants the cascade swept up, so the audit
// trail is enough to reconstruct intent later.

type hierarchyImpact struct {
	Locations       int `json:"locations,omitempty"`
	Buildings       int `json:"buildings,omitempty"`
	Rooms           int `json:"rooms,omitempty"`
	DevicesOrphaned int `json:"devices_orphaned"`
}

// impactToMap converts the typed counts into the map[string]any shape the
// audit layer expects for the metadata column. Zero-value counts are dropped
// so the JSON stays compact and the read-side diff doesn't show noise.
func impactToMap(imp hierarchyImpact) map[string]any {
	m := map[string]any{"devices_orphaned": imp.DevicesOrphaned}
	if imp.Locations > 0 {
		m["locations"] = imp.Locations
	}
	if imp.Buildings > 0 {
		m["buildings"] = imp.Buildings
	}
	if imp.Rooms > 0 {
		m["rooms"] = imp.Rooms
	}
	return m
}

// countRegionImpact / countLocationImpact / countBuildingImpact / countRoomImpact
// must run inside the same tx as the DELETE so RLS keeps the counts tenant-
// scoped (no cross-tenant info leak via counts).
func countRegionImpact(ctx context.Context, tx pgx.Tx, id string) (hierarchyImpact, error) {
	var imp hierarchyImpact
	err := tx.QueryRow(ctx, `
		WITH loc AS (SELECT id FROM locations WHERE region_id = $1),
		     bld AS (SELECT id FROM buildings WHERE location_id IN (SELECT id FROM loc)),
		     rms AS (SELECT id FROM rooms WHERE building_id IN (SELECT id FROM bld))
		SELECT
		  (SELECT COUNT(*) FROM loc)::int,
		  (SELECT COUNT(*) FROM bld)::int,
		  (SELECT COUNT(*) FROM rms)::int,
		  (SELECT COUNT(*) FROM devices WHERE room_id IN (SELECT id FROM rms))::int`,
		id).Scan(&imp.Locations, &imp.Buildings, &imp.Rooms, &imp.DevicesOrphaned)
	return imp, err
}

func countLocationImpact(ctx context.Context, tx pgx.Tx, id string) (hierarchyImpact, error) {
	var imp hierarchyImpact
	err := tx.QueryRow(ctx, `
		WITH bld AS (SELECT id FROM buildings WHERE location_id = $1),
		     rms AS (SELECT id FROM rooms WHERE building_id IN (SELECT id FROM bld))
		SELECT
		  (SELECT COUNT(*) FROM bld)::int,
		  (SELECT COUNT(*) FROM rms)::int,
		  (SELECT COUNT(*) FROM devices WHERE room_id IN (SELECT id FROM rms))::int`,
		id).Scan(&imp.Buildings, &imp.Rooms, &imp.DevicesOrphaned)
	return imp, err
}

func countBuildingImpact(ctx context.Context, tx pgx.Tx, id string) (hierarchyImpact, error) {
	var imp hierarchyImpact
	err := tx.QueryRow(ctx, `
		WITH rms AS (SELECT id FROM rooms WHERE building_id = $1)
		SELECT
		  (SELECT COUNT(*) FROM rms)::int,
		  (SELECT COUNT(*) FROM devices WHERE room_id IN (SELECT id FROM rms))::int`,
		id).Scan(&imp.Rooms, &imp.DevicesOrphaned)
	return imp, err
}

func countRoomImpact(ctx context.Context, tx pgx.Tx, id string) (hierarchyImpact, error) {
	var imp hierarchyImpact
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM devices WHERE room_id = $1`,
		id).Scan(&imp.DevicesOrphaned)
	return imp, err
}

// deleteHierarchyNode is the shared workhorse. counter computes the cascade
// impact for audit metadata; table is the row to delete; kind names the audit
// action.
func (h *Handler) deleteHierarchyNode(
	w http.ResponseWriter, r *http.Request,
	table, kind string,
	counter func(context.Context, pgx.Tx, string) (hierarchyImpact, error),
) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotByTable(ctx, tx, table, id)
		if err != nil {
			return err
		}
		if before == nil {
			// Row didn't exist or wasn't visible to this tenant; either way,
			// nothing to delete. Leave rowsAffected at 0 so the handler 404s.
			return nil
		}
		imp, err := counter(ctx, tx, id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			"DELETE FROM "+table+" WHERE id = $1", id)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: kind + ".delete", TargetKind: kind, TargetID: id,
			Before:   before,
			Metadata: impactToMap(imp),
		})
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, kind+" not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteRegion(w http.ResponseWriter, r *http.Request) {
	h.deleteHierarchyNode(w, r, "regions", "region", countRegionImpact)
}
func (h *Handler) DeleteLocation(w http.ResponseWriter, r *http.Request) {
	h.deleteHierarchyNode(w, r, "locations", "location", countLocationImpact)
}
func (h *Handler) DeleteBuilding(w http.ResponseWriter, r *http.Request) {
	h.deleteHierarchyNode(w, r, "buildings", "building", countBuildingImpact)
}
func (h *Handler) DeleteRoom(w http.ResponseWriter, r *http.Request) {
	h.deleteHierarchyNode(w, r, "rooms", "room", countRoomImpact)
}

// ----- device CRUD -----------------------------------------------------------
//
// Slice 4.1: portal owns device config end-to-end. Slice 4's bridgecfg path
// returns whatever the portal has stored; YAML on the bridge is now just
// a bootstrap-seed mechanism for fresh deployments.
//
// Allowed protocols + types are kept in sync with the bridge's validate() in
// av-bridge/internal/config/config.go. The cloud module doesn't import the
// bridge module so we duplicate the enum — small, stable, and worth catching
// bad input here before the bridge silently drops the device.

var (
	allowedProtocols = map[string]bool{
		"rest": true, "websocket": true, "telnet": true, "serial": true,
		"tesira": true, "sony_bravia": true, "poly_videoos": true, "aurora_rxt": true,
	}
	allowedTypes = map[string]bool{
		"display": true, "conferencing": true, "audio": true,
		"camera": true, "control": true,
	}
)

// Subscription mirrors the bridge's SubscriptionSpec (and bridgecfg's
// Subscription). Duplicated so portalapi has no dependency on the
// bridge-facing package.
type Subscription struct {
	Tag       string `json:"tag"`
	Attribute string `json:"attribute"`
	Channel   int    `json:"channel"`
	Label     string `json:"label"`
	Rate      int    `json:"rate,omitempty"`
}

type createDeviceReq struct {
	CollectorID   string            `json:"collector_id"`
	ReportedID    string            `json:"reported_id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Protocol      string            `json:"protocol"`
	Address       string            `json:"address,omitempty"`
	BaudRate      int               `json:"baud_rate,omitempty"`
	Username      string            `json:"username,omitempty"`
	Password      string            `json:"password,omitempty"`
	PollRate      int               `json:"poll_rate_seconds,omitempty"`
	Commands      map[string]string `json:"commands,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	Subscriptions []Subscription    `json:"subscriptions,omitempty"`
	RoomID        string            `json:"room_id,omitempty"`
}

// CreateDevice — POST /api/v1/devices
//
// Mints a device row owned by an existing collector. Both collector_id and
// room_id are checked via RLS-aware EXISTS so a cross-tenant UUID guess fails
// closed. Credentials are encrypted before they touch the DB.
func (h *Handler) CreateDevice(w http.ResponseWriter, r *http.Request) {
	var req createDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.CollectorID == "" || req.ReportedID == "" {
		writeErr(w, http.StatusBadRequest, "collector_id and reported_id are required")
		return
	}
	if req.Protocol != "" && !allowedProtocols[req.Protocol] {
		writeErr(w, http.StatusBadRequest, "unsupported protocol")
		return
	}
	if req.Type != "" && !allowedTypes[req.Type] {
		writeErr(w, http.StatusBadRequest, "unsupported type")
		return
	}

	p, _ := portalauth.From(r.Context())
	userEnc, err := h.encryptOptional(req.Username)
	if err != nil {
		h.log.Error("encrypt username", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	passEnc, err := h.encryptOptional(req.Password)
	if err != nil {
		h.log.Error("encrypt password", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	var (
		id            string
		collectorBad  bool
		roomBad       bool
		duplicate     bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Confirm the collector belongs to this customer (RLS-aware).
		var collectorExists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM collectors WHERE id = $1)`, req.CollectorID,
		).Scan(&collectorExists); err != nil {
			return err
		}
		if !collectorExists {
			collectorBad = true
			return nil
		}
		if req.RoomID != "" {
			var roomExists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, req.RoomID,
			).Scan(&roomExists); err != nil {
				return err
			}
			if !roomExists {
				roomBad = true
				return nil
			}
		}

		var roomParam any = nil
		if req.RoomID != "" {
			roomParam = req.RoomID
		}
		// ON CONFLICT DO NOTHING keeps the transaction healthy on a duplicate —
		// a statement error here would poison the tx and Commit would fail with
		// "commit unexpectedly resulted in rollback". The conflict surfaces as
		// pgx.ErrNoRows from Scan, which is the clean signal for "409".
		err := tx.QueryRow(ctx, `
			INSERT INTO devices (
				customer_id, collector_id, room_id, reported_id,
				name, type, protocol, address, baud_rate,
				username_enc, password_enc, poll_rate_seconds,
				commands, tags, subscriptions
			) VALUES (
				$1, $2, $3, $4,
				NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9,
				$10, $11, $12,
				$13::jsonb, $14::jsonb, $15::jsonb
			)
			ON CONFLICT (collector_id, reported_id) DO NOTHING
			RETURNING id::text`,
			p.CustomerID, req.CollectorID, roomParam, req.ReportedID,
			req.Name, req.Type, req.Protocol, req.Address, nullIfZero(req.BaudRate),
			userEnc, passEnc, nullIfZero(req.PollRate),
			jsonOrNil(req.Commands), jsonOrNil(req.Tags), jsonOrNil(req.Subscriptions),
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			duplicate = true
			return nil
		}
		if err != nil {
			return err
		}
		after, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "device.create", TargetKind: "device", TargetID: id, After: after,
		})
	})
	if !ok {
		return
	}
	if collectorBad {
		writeErr(w, http.StatusBadRequest, "collector_id not found in this customer")
		return
	}
	if roomBad {
		writeErr(w, http.StatusBadRequest, "room_id not found in this customer")
		return
	}
	if duplicate {
		writeErr(w, http.StatusConflict, "reported_id already exists on this collector")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// updateDeviceReq — every field is a pointer (or *json.RawMessage for the
// jsonb columns) so we can distinguish "not in payload" from "set to empty".
// Pointer-nil means leave the column untouched.
type updateDeviceReq struct {
	Name          *string             `json:"name,omitempty"`
	Type          *string             `json:"type,omitempty"`
	Protocol      *string             `json:"protocol,omitempty"`
	Address       *string             `json:"address,omitempty"`
	BaudRate      *int                `json:"baud_rate,omitempty"`
	Username      *string             `json:"username,omitempty"`
	Password      *string             `json:"password,omitempty"`
	PollRate      *int                `json:"poll_rate_seconds,omitempty"`
	Commands      *map[string]string  `json:"commands,omitempty"`
	Tags          *map[string]string  `json:"tags,omitempty"`
	Subscriptions *[]Subscription     `json:"subscriptions,omitempty"`

	// room_id: nil = don't touch placement; non-nil = set (empty string clears).
	// Matches the previous PlaceDevice semantics so the portal's existing
	// placement flow keeps working through the combined PATCH.
	RoomID *string `json:"room_id,omitempty"`
}

// UpdateDevice — PATCH /api/v1/devices/{id}
//
// Partial update: fields present in the body are written, fields absent are
// left alone. room_id keeps the old PlaceDevice semantics (nil = no change,
// empty string = unassigned, uuid = place).
func (h *Handler) UpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Protocol != nil && *req.Protocol != "" && !allowedProtocols[*req.Protocol] {
		writeErr(w, http.StatusBadRequest, "unsupported protocol")
		return
	}
	if req.Type != nil && *req.Type != "" && !allowedTypes[*req.Type] {
		writeErr(w, http.StatusBadRequest, "unsupported type")
		return
	}

	set := []string{}
	args := []any{}
	add := func(col string, val any) {
		args = append(args, val)
		set = append(set, col+" = $"+strconv.Itoa(len(args)))
	}

	if req.Name != nil {
		add("name", nullIfEmpty(*req.Name))
	}
	if req.Type != nil {
		add("type", nullIfEmpty(*req.Type))
	}
	if req.Protocol != nil {
		add("protocol", nullIfEmpty(*req.Protocol))
	}
	if req.Address != nil {
		add("address", nullIfEmpty(*req.Address))
	}
	if req.BaudRate != nil {
		add("baud_rate", nullIfZero(*req.BaudRate))
	}
	if req.PollRate != nil {
		add("poll_rate_seconds", nullIfZero(*req.PollRate))
	}
	if req.Username != nil {
		enc, err := h.encryptOptional(*req.Username)
		if err != nil {
			h.log.Error("encrypt username", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		add("username_enc", enc)
	}
	if req.Password != nil {
		enc, err := h.encryptOptional(*req.Password)
		if err != nil {
			h.log.Error("encrypt password", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		add("password_enc", enc)
	}
	if req.Commands != nil {
		add("commands", jsonOrNilCasted(*req.Commands))
	}
	if req.Tags != nil {
		add("tags", jsonOrNilCasted(*req.Tags))
	}
	if req.Subscriptions != nil {
		add("subscriptions", jsonOrNilCasted(*req.Subscriptions))
	}

	// room_id needs the same RLS-aware EXISTS guard PlaceDevice had.
	// We inline it instead of using `add` so the SQL can carry the EXISTS check.
	roomTouch := req.RoomID != nil
	var roomVal any
	if roomTouch {
		if *req.RoomID == "" {
			roomVal = nil
		} else {
			roomVal = *req.RoomID
		}
		args = append(args, roomVal)
		set = append(set, "room_id = $"+strconv.Itoa(len(args))+"::uuid")
	}

	if len(set) == 0 {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	idParam := "$" + strconv.Itoa(len(args))

	// Build WHERE: the device must belong to this customer (RLS), and if a
	// room_id was provided AND non-NULL, the room must exist in this customer.
	where := "id = " + idParam
	if roomTouch && roomVal != nil {
		where += " AND EXISTS (SELECT 1 FROM rooms WHERE id = $" + strconv.Itoa(len(args)-1) + "::uuid)"
	}

	sql := "UPDATE devices SET " + strings.Join(set, ", ") + " WHERE " + where

	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		after, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "device.update", TargetKind: "device", TargetID: id,
			Before: before, After: after,
		})
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "device not found, or room_id not visible to this customer")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"updated": time.Now().UTC().Format(time.RFC3339),
	})
}

// DeleteDevice — DELETE /api/v1/devices/{id}
//
// FK cascades drop the device's telemetry, events, and commands. The audit
// row captures the device's full pre-delete state so it can be reconstructed
// for forensic purposes even after the cascade has run.
func (h *Handler) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM devices WHERE id = $1`, id)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "device.delete", TargetKind: "device", TargetID: id,
			Before: before,
		})
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// encryptOptional encrypts non-empty plaintext to ciphertext, or returns nil
// for an empty string so the column ends up NULL in the DB. Mirrors the
// behaviour in bridgecfg so the round-trip with the bridge stays consistent.
func (h *Handler) encryptOptional(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	return h.cipher.Encrypt([]byte(plain))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// jsonOrNil marshals a value to a JSON string ready for ::jsonb casting, or
// returns nil so the column ends up SQL NULL. Treats empty maps/slices as
// "absent" so a UI sending {} doesn't store an empty object.
func jsonOrNil(v any) any {
	switch x := v.(type) {
	case map[string]string:
		if len(x) == 0 {
			return nil
		}
	case []Subscription:
		if len(x) == 0 {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	return string(b)
}

// jsonOrNilCasted is the form used inside dynamic SET clauses. Identical
// behaviour but explicit cast happens in the column expression rather than the
// query — kept as a separate function for readability.
func jsonOrNilCasted(v any) any { return jsonOrNil(v) }

