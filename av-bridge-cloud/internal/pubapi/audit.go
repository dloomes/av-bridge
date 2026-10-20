package pubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// publicAuditEntry is one row of the tenant's audit trail. before/after
// are snapshots of the changed record; secrets (device credentials,
// password hashes, token hashes) are excluded when the entry is written,
// so nothing here needs redacting on the way out.
type publicAuditEntry struct {
	ID                string          `json:"id"`
	Timestamp         time.Time       `json:"timestamp"`
	Actor             string          `json:"actor"`
	Action            string          `json:"action"`
	TargetKind        string          `json:"target_kind"`
	TargetID          string          `json:"target_id,omitempty"`
	RelatedTargetKind string          `json:"related_target_kind,omitempty"`
	RelatedTargetID   string          `json:"related_target_id,omitempty"`
	Before            json.RawMessage `json:"before,omitempty"`
	After             json.RawMessage `json:"after,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// ListAudit — GET /pub/v1/audit
//
// Filters (all optional):
//
//	action       exact action, e.g. device.update; or a prefix ending in
//	             "." (device.) for every action on that kind of record
//	target_kind  exact, e.g. device, user, notification_channel
//	target_id    matches the primary or the related target, so a device's
//	             feed includes commands sent to it (as in the portal)
//	actor        exact actor label
//	device_id    entries about the device, including commands sent to it
//	room_id      entries about the room and everything in it: its devices,
//	             their commands, its assets, its Room Readiness runs
//	building_id  the same for a building and its rooms
//	location_id  the same for a location (site) and its buildings
//	since/until  RFC 3339; since is inclusive, until exclusive
//	last         a rolling window instead of since: 15m, 2h, 7d (up to 366d)
//	cursor+limit standard pagination
//
// Place filters match a record's current place: a device moved to another
// room shows under its new room, its history included.
//
// Requires the view.audit scope. Ordered by (ts, id) desc. Tenant scoping
// comes from RLS on audit_log, as everywhere else in the public API.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	var cursorID int64
	if cursor.TS != nil {
		if cursorID, err = strconv.ParseInt(cursor.ID, 10, 64); err != nil {
			writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
			return
		}
	}
	limit := ParseLimit(r)
	qv := r.URL.Query()
	parseTime := func(name string) (*time.Time, bool) {
		v := strings.TrimSpace(qv.Get(name))
		if v == "" {
			return nil, true
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, name+" must be an RFC 3339 timestamp, e.g. 2026-10-01T00:00:00Z")
			return nil, false
		}
		return &t, true
	}
	since, ok := parseTime("since")
	if !ok {
		return
	}
	until, ok := parseTime("until")
	if !ok {
		return
	}
	if v := strings.TrimSpace(qv.Get("last")); v != "" {
		if since != nil {
			writeErr(w, http.StatusBadRequest, "use since or last, not both")
			return
		}
		d, err := parseWindow(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "last "+err.Error())
			return
		}
		t := time.Now().Add(-d)
		since = &t
	}
	places := map[string]string{}
	for _, name := range []string{"device_id", "room_id", "building_id", "location_id"} {
		v := strings.TrimSpace(qv.Get(name))
		if v == "" {
			continue
		}
		if !uuidRE.MatchString(v) {
			writeErr(w, http.StatusBadRequest, name+" must be a UUID")
			return
		}
		places[name] = strings.ToLower(v)
	}

	sql := `
		SELECT a.id::text, a.ts, a.actor, a.action, a.target_kind,
		       COALESCE(a.target_id, ''),
		       COALESCE(a.related_target_kind, ''), COALESCE(a.related_target_id, ''),
		       a.before, a."after", a.metadata
		  FROM audit_log a
		 WHERE 1=1`
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}

	if action := strings.TrimSpace(qv.Get("action")); action != "" {
		if strings.HasSuffix(action, ".") {
			sql += " AND starts_with(a.action, " + arg(action) + ")"
		} else {
			sql += " AND a.action = " + arg(action)
		}
	}
	targetKind := strings.TrimSpace(qv.Get("target_kind"))
	if targetID := strings.TrimSpace(qv.Get("target_id")); targetID != "" {
		idP := arg(targetID)
		if targetKind != "" {
			kindP := arg(targetKind)
			sql += " AND ((a.target_id = " + idP + " AND a.target_kind = " + kindP + ")" +
				" OR (a.related_target_id = " + idP + " AND a.related_target_kind = " + kindP + "))"
		} else {
			sql += " AND (a.target_id = " + idP + " OR a.related_target_id = " + idP + ")"
		}
	} else if targetKind != "" {
		sql += " AND a.target_kind = " + arg(targetKind)
	}
	if actor := strings.TrimSpace(qv.Get("actor")); actor != "" {
		sql += " AND a.actor = " + arg(actor)
	}
	// Each place filter is the set of records in that place, as (kind, id);
	// an entry matches when its target or related target is one of them.
	for _, name := range []string{"device_id", "room_id", "building_id", "location_id"} {
		id, ok := places[name]
		if !ok {
			continue
		}
		sql += " AND EXISTS (SELECT 1 FROM (" + placeRecordsSQL(name, arg(id)) + ") pr(kind, id)" +
			" WHERE (pr.kind = a.target_kind AND pr.id = a.target_id)" +
			" OR (pr.kind = a.related_target_kind AND pr.id = a.related_target_id))"
	}
	if since != nil {
		sql += " AND a.ts >= " + arg(*since)
	}
	if until != nil {
		sql += " AND a.ts < " + arg(*until)
	}
	if cursor.TS != nil {
		tsP := arg(*cursor.TS)
		idP := arg(cursorID)
		sql += " AND (a.ts, a.id) < (" + tsP + ", " + idP + "::bigint)"
	}
	sql += " ORDER BY a.ts DESC, a.id DESC LIMIT " + arg(limit+1) + "::int"

	out := []publicAuditEntry{}
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e publicAuditEntry
			if err := rows.Scan(&e.ID, &e.Timestamp, &e.Actor, &e.Action, &e.TargetKind,
				&e.TargetID, &e.RelatedTargetKind, &e.RelatedTargetID,
				&e.Before, &e.After, &e.Metadata); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	var next *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: &last.Timestamp, ID: last.ID})
		next = &nc
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, Page(out, next))
}

// maxAuditWindow bounds last=.
const maxAuditWindow = 366 * 24 * time.Hour

// parseWindow reads a rolling window such as 15m, 2h or 7d.
func parseWindow(v string) (time.Duration, error) {
	var d time.Duration
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, errWindow
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, errWindow
		}
	}
	if d <= 0 || d > maxAuditWindow {
		return 0, errWindow
	}
	return d, nil
}

var errWindow = errors.New("must be a duration such as 15m, 2h or 7d, up to 366d")

// placeRecordsSQL selects (kind, id) for every record in a place: the place
// itself and everything beneath it (buildings, rooms, devices, assets).
// p is the bound parameter holding the place's id.
func placeRecordsSQL(filter, p string) string {
	devicesIn := func(roomsSQL string) string {
		return `SELECT 'device', d.id::text FROM devices d WHERE d.room_id IN (` + roomsSQL + `)` +
			` UNION ALL SELECT 'asset', x.id::text FROM assets x WHERE x.room_id IN (` + roomsSQL + `)`
	}
	switch filter {
	case "device_id":
		return `SELECT 'device'::text, ` + p + `::text`
	case "room_id":
		rooms := `SELECT ` + p + `::uuid`
		return `SELECT 'room'::text, ` + p + `::text UNION ALL ` + devicesIn(rooms)
	case "building_id":
		rooms := `SELECT r.id FROM rooms r WHERE r.building_id = ` + p + `::uuid`
		return `SELECT 'building'::text, ` + p + `::text` +
			` UNION ALL SELECT 'room', r.id::text FROM rooms r WHERE r.building_id = ` + p + `::uuid` +
			` UNION ALL ` + devicesIn(rooms)
	default: // location_id
		buildings := `SELECT b.id FROM buildings b WHERE b.location_id = ` + p + `::uuid`
		rooms := `SELECT r.id FROM rooms r WHERE r.building_id IN (` + buildings + `)`
		return `SELECT 'location'::text, ` + p + `::text` +
			` UNION ALL SELECT 'building', b.id::text FROM buildings b WHERE b.location_id = ` + p + `::uuid` +
			` UNION ALL SELECT 'room', r.id::text FROM rooms r WHERE r.building_id IN (` + buildings + `)` +
			` UNION ALL ` + devicesIn(rooms)
	}
}
