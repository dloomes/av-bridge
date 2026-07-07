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
	"github.com/dloomes/av-bridge-cloud/internal/notify"
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "region.create", TargetKind: "region", TargetID: id, After: after,
		}))
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "location.create", TargetKind: "location", TargetID: id, After: after,
		}))
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "building.create", TargetKind: "building", TargetID: id, After: after,
		}))
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room.create", TargetKind: "room", TargetID: id, After: after,
		}))
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: kind + ".update", TargetKind: kind, TargetID: id,
			Before: before, After: after,
		}))
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "building.update", TargetKind: "building", TargetID: id,
			Before: before, After: after,
		}))
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

// ----- notification channels ------------------------------------------------
//
// Per-customer outbound channels (email / Teams webhook / generic webhook)
// that alerts get dispatched to when first opened. Reads and lists are open
// to any authenticated user in the customer; create/update/delete are gated
// to admin at the route layer.

type notificationChannelReq struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Target      string         `json:"target"`
	Config      map[string]any `json:"config,omitempty"`
	MinSeverity string         `json:"min_severity"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

var allowedChannelTypes = map[string]bool{
	"email": true, "teams": true, "webhook": true,
}

var allowedSeverities = map[string]bool{
	"info": true, "warning": true, "critical": true,
}

func (h *Handler) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		Type        string     `json:"type"`
		Target      string     `json:"target"`
		MinSeverity string     `json:"min_severity"`
		Enabled     bool       `json:"enabled"`
		LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
		LastError   string     `json:"last_error,omitempty"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, type, target, min_severity, enabled,
			       last_sent_at, COALESCE(last_error,'')
			  FROM notification_channels ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.ID, &it.Name, &it.Type, &it.Target,
				&it.MinSeverity, &it.Enabled, &it.LastSentAt, &it.LastError); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) CreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var req notificationChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" || req.Target == "" {
		writeErr(w, http.StatusBadRequest, "name and target are required")
		return
	}
	if !allowedChannelTypes[req.Type] {
		writeErr(w, http.StatusBadRequest, "type must be one of email/teams/webhook")
		return
	}
	if req.MinSeverity == "" {
		req.MinSeverity = "warning"
	}
	if !allowedSeverities[req.MinSeverity] {
		writeErr(w, http.StatusBadRequest, "min_severity must be info/warning/critical")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, _ := portalauth.From(r.Context())

	var id string
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO notification_channels
			    (customer_id, name, type, target, config, min_severity, enabled)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
			RETURNING id::text`,
			p.CustomerID, req.Name, req.Type, req.Target,
			jsonOrNil(req.Config), req.MinSeverity, enabled).Scan(&id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "notification_channel.create",
			TargetKind: "notification_channel", TargetID: id,
			After: mustJSON(map[string]any{
				"name": req.Name, "type": req.Type, "target": req.Target,
				"min_severity": req.MinSeverity, "enabled": enabled,
			}),
		}))
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *Handler) UpdateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req notificationChannelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Validation mirrors create — but type can't change on update because
	// channels are typed at creation (a different type means a different
	// target shape entirely).
	if req.Name == "" || req.Target == "" {
		writeErr(w, http.StatusBadRequest, "name and target are required")
		return
	}
	if req.MinSeverity == "" {
		req.MinSeverity = "warning"
	}
	if !allowedSeverities[req.MinSeverity] {
		writeErr(w, http.StatusBadRequest, "min_severity must be info/warning/critical")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE notification_channels
			   SET name = $2, target = $3, config = $4::jsonb,
			       min_severity = $5, enabled = $6
			 WHERE id = $1`,
			id, req.Name, req.Target, jsonOrNil(req.Config),
			req.MinSeverity, enabled)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "notification_channel.update",
			TargetKind: "notification_channel", TargetID: id,
			After: mustJSON(map[string]any{
				"name": req.Name, "target": req.Target,
				"min_severity": req.MinSeverity, "enabled": enabled,
			}),
		}))
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *Handler) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "notification_channel.delete",
			TargetKind: "notification_channel", TargetID: id,
		}))
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestNotificationChannel — POST /api/v1/notifications/channels/{id}/test
//
// Sends a synthetic alert through a single channel so an operator can verify
// the target works before relying on it. Returns 200 if the send succeeded,
// 502 with the underlying error message otherwise. The channel's
// last_sent_at + last_error are updated by the dispatcher regardless.
func (h *Handler) TestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.dispatcher == nil {
		writeErr(w, http.StatusServiceUnavailable, "notification dispatcher not configured")
		return
	}
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	evt := notify.AlertEvent{
		CustomerID: p.CustomerID,
		DeviceID:   "00000000-0000-0000-0000-000000000000",
		DeviceName: "Test device",
		AlertKey:   "test_notification",
		Severity:   "info",
		Message:    "This is a test notification triggered from the portal by " + p.ActorLabel() + ".",
		OpenedAt:   time.Now().UTC(),
	}
	if err := h.dispatcher.SendToChannel(r.Context(), p.CustomerID, id, evt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		writeErr(w, http.StatusBadGateway, "send failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// mustJSON returns a marshalled json.RawMessage for an audit before/after
// snapshot. Falls back to nil if marshalling fails — the audit row is
// best-effort and shouldn't fail the actual operation.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: kind + ".delete", TargetKind: kind, TargetID: id,
			Before:   before,
			Metadata: impactToMap(imp),
		}))
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
	// AssetID optionally links this device to an EXISTING asset row.
	// Mutually exclusive with Asset (a device can either link to an
	// existing catalogue entry or create a new one, not both).
	AssetID       string            `json:"asset_id,omitempty"`
	// Asset optionally carries fresh CMDB fields for a new asset row.
	// When any field is populated the device create tx also inserts the
	// asset and links it — no bounce to /assets required. If AssetID is
	// also set, Asset is treated as a PATCH against that existing row.
	Asset *deviceAssetInput `json:"asset,omitempty"`
}

// deviceAssetInput mirrors the standard asset field set that the device
// form now surfaces inline. Fields are all optional — a non-empty ANY
// of these triggers the create-or-patch path. Name defaults to the
// device's own name so we don't have to duplicate it in the payload.
type deviceAssetInput struct {
	AssetTag     string `json:"asset_tag,omitempty"`
	Category     string `json:"category,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	Status       string `json:"status,omitempty"`
	PurchaseDate string `json:"purchase_date,omitempty"`
	WarrantyEnd  string `json:"warranty_end,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

// hasAny reports whether any field on the sub-object is populated. Empty
// pointers / all-empty structs mean "no inline asset work requested"; the
// device row goes through the create path unchanged.
func (a *deviceAssetInput) hasAny() bool {
	if a == nil {
		return false
	}
	return a.AssetTag != "" || a.Category != "" || a.Manufacturer != "" ||
		a.Model != "" || a.SerialNumber != "" || a.Status != "" ||
		a.PurchaseDate != "" || a.WarrantyEnd != "" || a.Notes != ""
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
		id             string
		collectorBad   bool
		roomBad        bool
		assetBad       bool
		duplicate      bool
		assetInlineErr error
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
		// asset_id, if provided, must reference an asset visible to this
		// tenant. RLS on assets scopes the SELECT — an out-of-tenant uuid
		// simply returns no rows, which surfaces as a 400 via assetBad.
		if req.AssetID != "" {
			var assetExists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM assets WHERE id = $1)`, req.AssetID,
			).Scan(&assetExists); err != nil {
				return err
			}
			if !assetExists {
				assetBad = true
				return nil
			}
		}

		var roomParam any = nil
		if req.RoomID != "" {
			roomParam = req.RoomID
		}
		var assetParam any = nil
		if req.AssetID != "" {
			assetParam = req.AssetID
		}

		// Inline asset create — the device form's Physical inventory
		// section arrived populated but no existing asset was picked. We
		// insert an asset in the same tx and use its id as the device's
		// link. If AssetID was also supplied, we PATCH that existing row
		// with the sub-object's fields instead.
		if req.Asset.hasAny() {
			category := req.Asset.Category
			// Default the category from the device type when the caller
			// didn't pick one — display→display, camera→camera etc. Any
			// device type that maps 1:1 to an asset category saves a
			// keystroke; unknowns fall through to "other".
			if category == "" {
				category = deviceTypeToAssetCategory(req.Type)
			}
			if !allowedAssetCategories[category] {
				assetInlineErr = errors.New("asset.category unsupported")
				return nil
			}
			status := req.Asset.Status
			if status == "" {
				status = "in_service"
			}
			if !allowedAssetStatuses[status] {
				assetInlineErr = errors.New("asset.status unsupported")
				return nil
			}
			purchase, err := parseDate(req.Asset.PurchaseDate)
			if err != nil {
				assetInlineErr = errors.New("asset.purchase_date: " + err.Error())
				return nil
			}
			warranty, err := parseDate(req.Asset.WarrantyEnd)
			if err != nil {
				assetInlineErr = errors.New("asset.warranty_end: " + err.Error())
				return nil
			}
			// asset_tag uniqueness pre-check — same pattern as CreateAsset,
			// keeps the tx healthy on conflict.
			if req.Asset.AssetTag != "" {
				var taken bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM assets WHERE asset_tag = $1)`,
					req.Asset.AssetTag,
				).Scan(&taken); err != nil {
					return err
				}
				if taken && req.AssetID == "" {
					// New asset conflicts with an existing tag.
					assetInlineErr = errors.New("asset.asset_tag already exists in this tenant")
					return nil
				}
			}
			if req.AssetID == "" {
				// Create a fresh asset row. Name defaults to the device's
				// name so the CMDB entry reads naturally in listings.
				assetName := req.Name
				if assetName == "" {
					assetName = req.ReportedID
				}
				var newAssetID string
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
					p.CustomerID, roomParam, req.Asset.AssetTag,
					assetName, category,
					req.Asset.Manufacturer, req.Asset.Model, req.Asset.SerialNumber,
					status, purchase, warranty, req.Asset.Notes,
				).Scan(&newAssetID); err != nil {
					return err
				}
				assetParam = newAssetID
			} else {
				// Existing asset — patch the fields the caller supplied.
				// Category / status stay optional; only overwrite what was
				// sent so we don't smash existing values with a partial form.
				patchFields := []string{}
				patchArgs := []any{req.AssetID}
				patchAdd := func(col string, val any) {
					patchArgs = append(patchArgs, val)
					patchFields = append(patchFields,
						col+" = $"+strconv.Itoa(len(patchArgs)))
				}
				if req.Asset.AssetTag != "" {
					patchAdd("asset_tag", req.Asset.AssetTag)
				}
				if req.Asset.Category != "" {
					patchAdd("category", category)
				}
				if req.Asset.Manufacturer != "" {
					patchAdd("manufacturer", req.Asset.Manufacturer)
				}
				if req.Asset.Model != "" {
					patchAdd("model", req.Asset.Model)
				}
				if req.Asset.SerialNumber != "" {
					patchAdd("serial_number", req.Asset.SerialNumber)
				}
				if req.Asset.Status != "" {
					patchAdd("status", status)
				}
				if req.Asset.PurchaseDate != "" {
					patchAdd("purchase_date", purchase)
				}
				if req.Asset.WarrantyEnd != "" {
					patchAdd("warranty_end", warranty)
				}
				if req.Asset.Notes != "" {
					patchAdd("notes", req.Asset.Notes)
				}
				if len(patchFields) > 0 {
					if _, err := tx.Exec(ctx,
						"UPDATE assets SET "+strings.Join(patchFields, ", ")+
							" WHERE id = $1", patchArgs...); err != nil {
						return err
					}
				}
			}
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
				commands, tags, subscriptions, asset_id
			) VALUES (
				$1, $2, $3, $4,
				NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9,
				$10, $11, $12,
				$13::jsonb, $14::jsonb, $15::jsonb, $16
			)
			ON CONFLICT (collector_id, reported_id) DO NOTHING
			RETURNING id::text`,
			p.CustomerID, req.CollectorID, roomParam, req.ReportedID,
			req.Name, req.Type, req.Protocol, req.Address, nullIfZero(req.BaudRate),
			userEnc, passEnc, nullIfZero(req.PollRate),
			jsonOrNil(req.Commands), jsonOrNil(req.Tags), jsonOrNil(req.Subscriptions),
			assetParam,
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "device.create", TargetKind: "device", TargetID: id, After: after,
		}))
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
	if assetBad {
		writeErr(w, http.StatusBadRequest, "asset_id not found in this customer")
		return
	}
	if assetInlineErr != nil {
		writeErr(w, http.StatusBadRequest, assetInlineErr.Error())
		return
	}
	if duplicate {
		writeErr(w, http.StatusConflict, "reported_id already exists on this collector")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// deviceTypeToAssetCategory maps the small device type enum onto an asset
// category so the inline "create asset alongside device" path can default
// sensibly. Device types that don't correspond to a single physical thing
// (control, conferencing) fall through to "other" — the operator can
// override with an explicit category on the form.
func deviceTypeToAssetCategory(deviceType string) string {
	switch deviceType {
	case "display":
		return "display"
	case "camera":
		return "camera"
	case "audio":
		return "audio"
	case "conferencing":
		return "conferencing"
	default:
		return "other"
	}
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

	// asset_id: same pointer semantics — nil = untouched, empty string =
	// clear the CMDB link, uuid = link. The FK is ON DELETE SET NULL so
	// unlink is always safe.
	AssetID *string `json:"asset_id,omitempty"`

	// Asset optionally carries CMDB fields to apply to the linked asset
	// row. If asset_id is set (either pre-existing or being set in this
	// same PATCH), the fields PATCH that asset. If asset_id is null and
	// the sub-object is populated, a fresh asset is created and linked.
	// Same shape as the create-flow sub-object.
	Asset *deviceAssetInput `json:"asset,omitempty"`
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
	var roomParamIdx int
	if roomTouch {
		if *req.RoomID == "" {
			roomVal = nil
		} else {
			roomVal = *req.RoomID
		}
		args = append(args, roomVal)
		roomParamIdx = len(args)
		set = append(set, "room_id = $"+strconv.Itoa(roomParamIdx)+"::uuid")
	}

	// asset_id follows the same pattern. RLS on assets guarantees a
	// cross-tenant uuid can't be linked — an EXISTS check on non-NULL asset
	// values keeps the failure path a clean 404 rather than a silent no-op.
	assetTouch := req.AssetID != nil
	var assetVal any
	var assetParamIdx int
	if assetTouch {
		if *req.AssetID == "" {
			assetVal = nil
		} else {
			assetVal = *req.AssetID
		}
		args = append(args, assetVal)
		assetParamIdx = len(args)
		set = append(set, "asset_id = $"+strconv.Itoa(assetParamIdx)+"::uuid")
	}

	// A PATCH that only carries an inline asset sub-object (no device
	// fields) is still valid — we skip the device UPDATE in that case
	// and jump straight to the asset write below.
	assetOnly := len(set) == 0 && req.Asset.hasAny()
	if len(set) == 0 && !assetOnly {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	idParam := "$" + strconv.Itoa(len(args))

	// Build WHERE: the device must belong to this customer (RLS), and any
	// non-NULL room_id / asset_id must reference a tenant-visible row.
	where := "id = " + idParam
	if roomTouch && roomVal != nil {
		where += " AND EXISTS (SELECT 1 FROM rooms WHERE id = $" + strconv.Itoa(roomParamIdx) + "::uuid)"
	}
	if assetTouch && assetVal != nil {
		where += " AND EXISTS (SELECT 1 FROM assets WHERE id = $" + strconv.Itoa(assetParamIdx) + "::uuid)"
	}

	sql := "UPDATE devices SET " + strings.Join(set, ", ") + " WHERE " + where

	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	var assetInlineErr error
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		if !assetOnly {
			tag, err := tx.Exec(ctx, sql, args...)
			if err != nil {
				return err
			}
			rowsAffected = tag.RowsAffected()
			if rowsAffected == 0 {
				return nil
			}
		} else {
			// No device fields changed — confirm the device exists so a
			// bogus id gets a 404 rather than a silent asset-only write.
			var deviceExists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, id,
			).Scan(&deviceExists); err != nil {
				return err
			}
			if !deviceExists {
				return nil
			}
			rowsAffected = 1
		}

		// Inline asset write. Figure out the effective asset_id: what the
		// device row now points at after the UPDATE (or after we resolved
		// assetVal in this PATCH). If there's no linked asset yet, create
		// one and link it in a follow-up UPDATE.
		if req.Asset.hasAny() {
			effectiveAssetID := ""
			if assetTouch && assetVal != nil {
				effectiveAssetID, _ = assetVal.(string)
			} else if err := tx.QueryRow(ctx,
				`SELECT COALESCE(asset_id::text, '') FROM devices WHERE id = $1`, id,
			).Scan(&effectiveAssetID); err != nil {
				return err
			}

			// Validate the inline payload up front so we return a clean 400.
			category := req.Asset.Category
			if category == "" {
				// Fall back to the pre-existing category if this is a PATCH
				// against an existing asset. On create, default from the
				// device row's current type.
				if effectiveAssetID == "" {
					var currentType *string
					if err := tx.QueryRow(ctx,
						`SELECT type FROM devices WHERE id = $1`, id,
					).Scan(&currentType); err == nil && currentType != nil {
						category = deviceTypeToAssetCategory(*currentType)
					} else {
						category = "other"
					}
				}
			}
			if category != "" && !allowedAssetCategories[category] {
				assetInlineErr = errors.New("asset.category unsupported")
				return nil
			}
			status := req.Asset.Status
			if status != "" && !allowedAssetStatuses[status] {
				assetInlineErr = errors.New("asset.status unsupported")
				return nil
			}
			purchase, err := parseDate(req.Asset.PurchaseDate)
			if err != nil {
				assetInlineErr = errors.New("asset.purchase_date: " + err.Error())
				return nil
			}
			warranty, err := parseDate(req.Asset.WarrantyEnd)
			if err != nil {
				assetInlineErr = errors.New("asset.warranty_end: " + err.Error())
				return nil
			}
			if req.Asset.AssetTag != "" {
				var taken bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM assets WHERE asset_tag = $1 AND id <> COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid))`,
					req.Asset.AssetTag, nullIfEmpty(effectiveAssetID),
				).Scan(&taken); err != nil {
					return err
				}
				if taken {
					assetInlineErr = errors.New("asset.asset_tag already exists in this tenant")
					return nil
				}
			}

			if effectiveAssetID == "" {
				// Create a new asset — inherit the device's current name/room
				// so the CMDB entry reads naturally.
				var deviceName, deviceRoom *string
				if err := tx.QueryRow(ctx,
					`SELECT name, room_id::text FROM devices WHERE id = $1`, id,
				).Scan(&deviceName, &deviceRoom); err != nil {
					return err
				}
				assetName := ""
				if deviceName != nil {
					assetName = *deviceName
				}
				if assetName == "" {
					// Fall back to reported_id — every device has one.
					if err := tx.QueryRow(ctx,
						`SELECT reported_id FROM devices WHERE id = $1`, id,
					).Scan(&assetName); err != nil {
						return err
					}
				}
				statusForNew := status
				if statusForNew == "" {
					statusForNew = "in_service"
				}
				categoryForNew := category
				if categoryForNew == "" {
					categoryForNew = "other"
				}
				var newAssetID string
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
					p.CustomerID, deviceRoom, req.Asset.AssetTag,
					assetName, categoryForNew,
					req.Asset.Manufacturer, req.Asset.Model, req.Asset.SerialNumber,
					statusForNew, purchase, warranty, req.Asset.Notes,
				).Scan(&newAssetID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx,
					`UPDATE devices SET asset_id = $1 WHERE id = $2`,
					newAssetID, id); err != nil {
					return err
				}
			} else {
				// PATCH the linked asset — only overwrite fields the caller
				// supplied so a partial form doesn't smash existing values.
				patchFields := []string{}
				patchArgs := []any{effectiveAssetID}
				patchAdd := func(col string, val any) {
					patchArgs = append(patchArgs, val)
					patchFields = append(patchFields,
						col+" = $"+strconv.Itoa(len(patchArgs)))
				}
				if req.Asset.AssetTag != "" {
					patchAdd("asset_tag", req.Asset.AssetTag)
				}
				if req.Asset.Category != "" {
					patchAdd("category", category)
				}
				if req.Asset.Manufacturer != "" {
					patchAdd("manufacturer", req.Asset.Manufacturer)
				}
				if req.Asset.Model != "" {
					patchAdd("model", req.Asset.Model)
				}
				if req.Asset.SerialNumber != "" {
					patchAdd("serial_number", req.Asset.SerialNumber)
				}
				if req.Asset.Status != "" {
					patchAdd("status", status)
				}
				if req.Asset.PurchaseDate != "" {
					patchAdd("purchase_date", purchase)
				}
				if req.Asset.WarrantyEnd != "" {
					patchAdd("warranty_end", warranty)
				}
				if req.Asset.Notes != "" {
					patchAdd("notes", req.Asset.Notes)
				}
				if len(patchFields) > 0 {
					if _, err := tx.Exec(ctx,
						"UPDATE assets SET "+strings.Join(patchFields, ", ")+
							" WHERE id = $1", patchArgs...); err != nil {
						return err
					}
				}
			}
		}

		after, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "device.update", TargetKind: "device", TargetID: id,
			Before: before, After: after,
		}))
	})
	if !ok {
		return
	}
	if assetInlineErr != nil {
		writeErr(w, http.StatusBadRequest, assetInlineErr.Error())
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound,
			"device not found, or room_id / asset_id not visible to this customer")
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
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "device.delete", TargetKind: "device", TargetID: id,
			Before: before,
		}))
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

