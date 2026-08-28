package pubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/devicestatus"
	"github.com/jackc/pgx/v5"
)

// Public device shape — deliberately smaller than the internal
// portal DeviceSummary. The public contract elides fields that are
// portal implementation details (adapter capabilities blob, sort
// hints) and surfaces the identity fields an integrating system
// actually asks for: make, model, serial, firmware.
//
// Every field here is a stable contract — renames become breaking
// changes. Anything speculative should land on a later /pub/v2.
type publicDevice struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	Protocol        string            `json:"protocol"`
	Status          string            `json:"status"`
	IPAddress       string            `json:"ip_address,omitempty"`
	Make            string            `json:"make,omitempty"`
	Model           string            `json:"model,omitempty"`
	SerialNumber    string            `json:"serial_number,omitempty"`
	FirmwareVersion string            `json:"firmware_version,omitempty"`
	MACAddress      string            `json:"mac_address,omitempty"`
	RoomID          string            `json:"room_id,omitempty"`
	Room            string            `json:"room,omitempty"`
	BuildingID      string            `json:"building_id,omitempty"`
	Building        string            `json:"building,omitempty"`
	LastSeenAt      *time.Time        `json:"last_seen_at,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// scanDeviceRow reads one row of the shared device SELECT into a
// publicDevice. Split out because ListDevices and GetDevice run the
// same base query with a different WHERE clause.
//
// The make/model/serial/firmware/mac fields fall back through several
// sources so a mixed fleet (some adapters emit rich identity, some
// don't) still populates:
//   1. devices.<field> — for older columns that were explicit
//   2. tags->>'<field>' — if the admin edited it in the portal
//   3. latest_metrics->>'<field>' — as reported by the adapter
//
// Order matters: explicit columns win over tags, tags win over
// live-reported metrics (so a portal-authored override sticks).
// status uses the effective projection so a stale latest_status on an
// offline-collector device doesn't leak out through the public API.
var publicDeviceBaseSelect = `
	SELECT d.id::text,
	       COALESCE(d.name, d.reported_id, ''),
	       COALESCE(d.type, ''),
	       COALESCE(d.protocol, ''),
	       ` + devicestatus.EffectiveStatusSQL + `,
	       COALESCE(d.ip_address, ''),
	       COALESCE(NULLIF(d.make,''), NULLIF(d.tags->>'make',''), COALESCE(d.latest_metrics->>'make','')) AS make,
	       COALESCE(NULLIF(d.model,''), NULLIF(d.tags->>'model',''), COALESCE(d.latest_metrics->>'model','')) AS model,
	       COALESCE(NULLIF(d.tags->>'serial_number',''), COALESCE(d.latest_metrics->>'serial_number','')) AS serial_number,
	       COALESCE(NULLIF(d.tags->>'firmware_version',''), COALESCE(d.latest_metrics->>'firmware_version','')) AS firmware_version,
	       COALESCE(NULLIF(d.tags->>'mac_address',''), COALESCE(d.latest_metrics->>'mac_address','')) AS mac_address,
	       COALESCE(d.room_id::text, ''),
	       COALESCE(r.name, ''),
	       COALESCE(b.id::text, ''),
	       COALESCE(b.name, ''),
	       d.last_seen_at,
	       d.tags
	  FROM devices d
	  LEFT JOIN rooms r     ON r.id = d.room_id
	  LEFT JOIN buildings b ON b.id = r.building_id
	  LEFT JOIN collectors c ON c.id = d.collector_id`

// scanDevice reads a single publicDevice off the pgx.Row. Kept as a
// method-free helper so both single-row (QueryRow.Scan) and multi-row
// (rows.Scan) callers use the same field order.
func scanDevice(scan func(dest ...any) error) (publicDevice, error) {
	var d publicDevice
	var tags []byte
	if err := scan(
		&d.ID, &d.Name, &d.Type, &d.Protocol, &d.Status, &d.IPAddress,
		&d.Make, &d.Model, &d.SerialNumber, &d.FirmwareVersion, &d.MACAddress,
		&d.RoomID, &d.Room, &d.BuildingID, &d.Building,
		&d.LastSeenAt, &tags,
	); err != nil {
		return d, err
	}
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &d.Tags)
	}
	return d, nil
}

// ListDevices — GET /pub/v1/devices
//
// Filters (all optional, all query-string):
//   building_id = <uuid>   only devices in that building
//   room_id     = <uuid>   only devices in that room
//   status      = <string> exact match on latest_status
//   cursor + limit         standard pagination
//
// Requires the view.dashboard scope. Ordered by (last_seen_at, id) so
// the cursor is stable and integrators polling for recently-active
// gear can page in a predictable order. NULL last_seen_at sorts last
// — devices that never phoned home don't clutter the head of the
// listing.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)

	buildingFilter := strings.TrimSpace(r.URL.Query().Get("building_id"))
	roomFilter := strings.TrimSpace(r.URL.Query().Get("room_id"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))

	sql := publicDeviceBaseSelect + " WHERE d.deleted_at IS NULL"
	args := []any{}
	next := func() string { return "$" + itoa(len(args)+1) }

	if buildingFilter != "" {
		args = append(args, buildingFilter)
		sql += " AND r.building_id::text = " + next()
	}
	if roomFilter != "" {
		args = append(args, roomFilter)
		sql += " AND d.room_id::text = " + next()
	}
	if statusFilter != "" {
		// Filter on effective status so an integrator asking for
		// status=online never sees a stale-on-offline-collector row.
		args = append(args, statusFilter)
		sql += " AND (" + devicestatus.EffectiveStatusSQL + ") = " + next()
	}
	// Cursor: page forward from wherever the previous response left
	// off. Compare on (COALESCE(last_seen_at,'-infinity'), id) so nulls
	// sort last consistently and the tuple comparison stays strict.
	if cursor.TS != nil {
		args = append(args, *cursor.TS, cursor.ID)
		sql += " AND (COALESCE(d.last_seen_at, '-infinity'::timestamptz), d.id::text) < (" + next()
		sql += ", $" + itoa(len(args)) + ")"
	}
	args = append(args, limit+1) // fetch one extra to know if there's a next page
	sql += " ORDER BY COALESCE(d.last_seen_at, '-infinity'::timestamptz) DESC, d.id::text DESC LIMIT " + next()

	out, nextCursor, ok := listPublicDevices(h, w, r, sql, args, limit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, Page(out, nextCursor))
}

// listPublicDevices runs the shared SELECT + tenant scope + pagination
// logic and returns the resulting page. Extracted so both the top-level
// list and any future filtered variant (e.g. by collector) share the
// pagination cursor logic.
func listPublicDevices(h *Handler, w http.ResponseWriter, r *http.Request, sql string, args []any, limit int) ([]publicDevice, *string, bool) {
	out := []publicDevice{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDevice(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if !ok {
		return nil, nil, false
	}
	var nextCursor *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: last.LastSeenAt, ID: last.ID})
		nextCursor = &nc
		out = out[:limit]
	}
	return out, nextCursor, true
}

// GetDevice — GET /pub/v1/devices/{id}
//
// Requires view.dashboard. Adds latest metrics on top of the list
// shape so a caller resolving a device from an alert or event doesn't
// need a follow-up telemetry call for common lookups.
func (h *Handler) GetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "device id required")
		return
	}

	type detail struct {
		publicDevice
		Metrics json.RawMessage `json:"metrics,omitempty"`
	}

	var (
		out      detail
		notFound bool
	)
	sql := publicDeviceBaseSelect + ", d.latest_metrics WHERE d.id = $1 AND d.deleted_at IS NULL"
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, sql, id)
		var tags []byte
		var metrics []byte
		err := row.Scan(
			&out.ID, &out.Name, &out.Type, &out.Protocol, &out.Status, &out.IPAddress,
			&out.Make, &out.Model, &out.SerialNumber, &out.FirmwareVersion, &out.MACAddress,
			&out.RoomID, &out.Room, &out.BuildingID, &out.Building,
			&out.LastSeenAt, &tags, &metrics,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &out.Tags)
		}
		if len(metrics) > 0 && string(metrics) != "null" {
			out.Metrics = json.RawMessage(metrics)
		}
		return nil
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetDeviceTelemetry — GET /pub/v1/devices/{id}/telemetry
//
// Just the latest snapshot. History is out of scope for v1 — the
// telemetry table can be arbitrarily large and callers wanting time
// series should use the events endpoint or the future /pub/v2
// history route.
func (h *Handler) GetDeviceTelemetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "device id required")
		return
	}
	type out struct {
		DeviceID  string          `json:"device_id"`
		Status    string          `json:"status"`
		Timestamp *time.Time      `json:"timestamp,omitempty"`
		Metrics   json.RawMessage `json:"metrics,omitempty"`
		Error     string          `json:"error,omitempty"`
	}
	var (
		o        out
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT d.id::text,
			       `+devicestatus.EffectiveStatusSQL+`,
			       d.last_seen_at,
			       d.latest_metrics
			  FROM devices d
			  LEFT JOIN collectors c ON c.id = d.collector_id
			 WHERE d.id = $1 AND d.deleted_at IS NULL`, id,
		).Scan(&o.DeviceID, &o.Status, &o.Timestamp, &o.Metrics)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		return err
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// GetDeviceEvents — GET /pub/v1/devices/{id}/events
//
// Recent events for one device, paginated by (ts, event_id). Same
// shape as the top-level /pub/v1/events endpoint so a caller can
// upgrade from single-device polling to global polling without
// changing their parsing.
func (h *Handler) GetDeviceEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "device id required")
		return
	}
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)

	sql := `
		SELECT e.id::text,
		       d.id::text,
		       COALESCE(d.name, d.reported_id, ''),
		       COALESCE(e.event_type, ''),
		       e.payload,
		       e.ts
		  FROM events e
		  JOIN devices d ON d.id = e.device_id
		 WHERE e.device_id = $1
		   AND d.deleted_at IS NULL`
	args := []any{id}
	next := func() string { return "$" + itoa(len(args)+1) }
	if cursor.TS != nil {
		args = append(args, *cursor.TS, cursor.ID)
		sql += " AND (e.ts, e.id::text) < (" + next()
		sql += ", $" + itoa(len(args)) + ")"
	}
	args = append(args, limit+1)
	sql += " ORDER BY e.ts DESC, e.id::text DESC LIMIT " + next()

	out, nextCursor, ok := listPublicEvents(h, w, r, sql, args, limit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, Page(out, nextCursor))
}

// itoa is a tiny numeric-to-string helper used to compose $N
// placeholders inline. strconv.Itoa would work; the two-char alias
// keeps SQL-builder blocks readable when there are half a dozen
// placeholders in a row.
func itoa(n int) string {
	// Cover the range we ever generate — pubapi list handlers all
	// stay well under 20 placeholders per query.
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	buf := make([]byte, 0, 4)
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	return string(buf)
}

