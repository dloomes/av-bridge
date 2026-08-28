package pubapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// publicEvent is the wire shape for both /pub/v1/events and
// /pub/v1/devices/{id}/events. Keeping a single shape lets a caller
// upgrade from single-device polling to global polling without
// changing their JSON parsing.
type publicEvent struct {
	ID         string          `json:"id"`
	DeviceID   string          `json:"device_id"`
	DeviceName string          `json:"device_name"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Timestamp  time.Time       `json:"timestamp"`
}

// listPublicEvents runs the shared events SELECT + pagination for both
// device-scoped and tenant-scoped event listings. Callers build the
// WHERE / ORDER BY / LIMIT clauses and pass the finished SQL + args
// slice; this helper handles tenant scoping, row scanning, and next-
// cursor derivation so the two entry points stay small.
func listPublicEvents(h *Handler, w http.ResponseWriter, r *http.Request, sql string, args []any, limit int) ([]publicEvent, *string, bool) {
	out := []publicEvent{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e publicEvent
			if err := rows.Scan(&e.ID, &e.DeviceID, &e.DeviceName, &e.EventType, &e.Payload, &e.Timestamp); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if !ok {
		return nil, nil, false
	}
	var nextCursor *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: &last.Timestamp, ID: last.ID})
		nextCursor = &nc
		out = out[:limit]
	}
	return out, nextCursor, true
}

// ListEvents — GET /pub/v1/events
//
// Filter: ?device_id=<uuid>  optional; equivalent to
//                            /pub/v1/devices/{id}/events but composes
//                            with other filters if they land later
// Pagination: standard cursor + limit.
//
// Requires view.dashboard scope. Ordered by (ts, event_id) desc for
// a "most recent first" feed — the natural polling shape.
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)
	deviceFilter := strings.TrimSpace(r.URL.Query().Get("device_id"))

	sql := `
		SELECT e.id::text,
		       d.id::text,
		       COALESCE(d.name, d.reported_id, ''),
		       COALESCE(e.event_type, ''),
		       e.payload,
		       e.ts
		  FROM events e
		  JOIN devices d ON d.id = e.device_id
		 WHERE d.deleted_at IS NULL`
	args := []any{}
	next := func() string { return "$" + itoa(len(args)+1) }
	if deviceFilter != "" {
		args = append(args, deviceFilter)
		sql += " AND e.device_id::text = " + next()
	}
	if cursor.TS != nil {
		args = append(args, *cursor.TS, cursor.ID)
		sql += " AND (e.ts, e.id::text) < (" + next()
		sql += ", $" + itoa(len(args)) + ")"
	}
	args = append(args, limit+1)
	sql += " ORDER BY e.ts DESC, e.id::text DESC LIMIT " + next() + "::int"

	out, nextCursor, ok := listPublicEvents(h, w, r, sql, args, limit)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, Page(out, nextCursor))
}
