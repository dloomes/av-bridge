package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// The power schedule by room tag (migration 0072). A tag can carry its own
// power-off and power-on times and days; rooms with the tag use them
// unless they have their own. Rooms with several tags take the first in
// the tags' priority order (shared with readiness checks, tag-order).

type powerTagRow struct {
	TagID      string  `json:"tag_id"`
	Name       string  `json:"name"`
	Colour     string  `json:"colour"`
	Priority   *int    `json:"priority,omitempty"`
	RoomCount  int     `json:"room_count"`
	Configured bool    `json:"configured"`
	PowerOff   *string `json:"power_off_time,omitempty"`
	PowerOn    *string `json:"power_on_time,omitempty"`
	Days       *[]int  `json:"days_of_week,omitempty"`
}

// ListPowerTags — GET /api/v1/nightly/tags. Every tag, in priority order,
// with its power settings if it has any.
func (h *Handler) ListPowerTags(w http.ResponseWriter, r *http.Request) {
	out := []powerTagRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.colour, t.priority,
			       (SELECT count(*)::int FROM room_tag_assignments a WHERE a.tag_id = t.id),
			       (c.id IS NOT NULL), c.power_off_time, c.power_on_time, c.days_of_week
			  FROM room_tags t
			  LEFT JOIN tag_power_config c ON c.tag_id = t.id
			 ORDER BY t.priority NULLS LAST, lower(t.name), t.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row     powerTagRow
				off, on *time.Time
				days    []int32
			)
			if err := rows.Scan(&row.TagID, &row.Name, &row.Colour, &row.Priority, &row.RoomCount,
				&row.Configured, &off, &on, &days); err != nil {
				return err
			}
			if off != nil {
				s := off.Format("15:04")
				row.PowerOff = &s
			}
			if on != nil {
				s := on.Format("15:04")
				row.PowerOn = &s
			}
			if days != nil {
				d := int32sToInts(days)
				row.Days = &d
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// updatePowerTagReq — per field: absent = no change, null = inherit the
// default, value = set.
type updatePowerTagReq struct {
	PowerOffTime json.RawMessage `json:"power_off_time"`
	PowerOnTime  json.RawMessage `json:"power_on_time"`
	DaysOfWeek   json.RawMessage `json:"days_of_week"`
}

func parsePowerFields(req updatePowerTagReq) ([]readinessField, string) {
	var fields []readinessField
	hhmm := func(col string, raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		if string(raw) == "null" {
			fields = append(fields, readinessField{col: col, set: true, clear: true})
			return ""
		}
		var t string
		if json.Unmarshal(raw, &t) != nil || !isHHMM(t) {
			return col + " must be HH:MM or null"
		}
		fields = append(fields, readinessField{col: col, set: true, v: t})
		return ""
	}
	if msg := hhmm("power_off_time", req.PowerOffTime); msg != "" {
		return nil, msg
	}
	if msg := hhmm("power_on_time", req.PowerOnTime); msg != "" {
		return nil, msg
	}
	if len(req.DaysOfWeek) > 0 {
		if string(req.DaysOfWeek) == "null" {
			fields = append(fields, readinessField{col: "days_of_week", set: true, clear: true})
		} else {
			var d []int
			if json.Unmarshal(req.DaysOfWeek, &d) != nil {
				return nil, "days_of_week must be an array of ISO weekdays or null"
			}
			if msg := checkDays(d); msg != "" {
				return nil, "days_of_week " + msg
			}
			fields = append(fields, readinessField{col: "days_of_week", set: true, v: d})
		}
	}
	return fields, ""
}

// UpdatePowerTag — PATCH /api/v1/nightly/tags/{id}. Gives the tag power
// settings, or changes them. The first PATCH creates them even with no
// fields, so every field inherits until set.
func (h *Handler) UpdatePowerTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if !uuidRe.MatchString(tagID) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	var req updatePowerTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	fields, msg := parsePowerFields(req)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	p, _ := portalauth.From(r.Context())
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM room_tags WHERE id = $1`, tagID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tag_power_config (customer_id, tag_id) VALUES ($1, $2)
			ON CONFLICT (tag_id) DO NOTHING`, p.CustomerID, tagID); err != nil {
			return err
		}
		payload := map[string]any{"tag_id": tagID, "tag": name}
		set, args := setReadinessFields(fields, []any{tagID}, payload)
		if len(set) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE tag_power_config SET "+strings.Join(set, ", ")+
				" WHERE tag_id = $1", args...); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.tag.update",
			TargetKind: "room_tag", TargetID: tagID,
			After: mustJSON(payload),
		}))
	})
	switch {
	case !ok:
		return
	case notFound:
		writeErr(w, http.StatusNotFound, "tag not found")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ResetPowerTag — DELETE /api/v1/nightly/tags/{id}. Removes the tag's power
// settings; its rooms go back to their next tag's, or the default.
func (h *Handler) ResetPowerTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if !uuidRe.MatchString(tagID) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	p, _ := portalauth.From(r.Context())
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM tag_power_config WHERE tag_id = $1`, tagID)
		if err != nil || res.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.tag.reset",
			TargetKind: "room_tag", TargetID: tagID,
		}))
	})
	if !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
