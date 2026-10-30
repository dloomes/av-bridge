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

// Readiness checks by room tag (migration 0071). A tag can carry its own
// check; rooms with the tag use it unless they have their own. When a
// room has several tags with checks, the tags' priority order decides.

type readinessTagRow struct {
	TagID     string `json:"tag_id"`
	Name      string `json:"name"`
	Colour    string `json:"colour"`
	Priority  *int   `json:"priority,omitempty"`
	RoomCount int    `json:"room_count"`
	// Configured: the tag has a check of its own. Its unset fields
	// inherit the default.
	Configured  bool    `json:"configured"`
	RoutineID   *string `json:"test_routine_id,omitempty"`
	RoutineName *string `json:"test_routine_name,omitempty"`
	RunTime     *string `json:"run_time,omitempty"`
	Days        *[]int  `json:"days_of_week,omitempty"`
}

// ListReadinessTags — GET /api/v1/readiness/tags. Every tag, in priority
// order, with its check if it has one.
func (h *Handler) ListReadinessTags(w http.ResponseWriter, r *http.Request) {
	out := []readinessTagRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.colour, t.priority,
			       (SELECT count(*)::int FROM room_tag_assignments a WHERE a.tag_id = t.id),
			       (c.id IS NOT NULL), c.test_routine_id::text, tr.name, c.run_time, c.days_of_week
			  FROM room_tags t
			  LEFT JOIN tag_readiness_config c  ON c.tag_id = t.id
			  LEFT JOIN nightly_test_routine tr ON tr.id = c.test_routine_id
			 ORDER BY t.priority NULLS LAST, lower(t.name), t.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row  readinessTagRow
				rt   *time.Time
				days []int32
			)
			if err := rows.Scan(&row.TagID, &row.Name, &row.Colour, &row.Priority, &row.RoomCount,
				&row.Configured, &row.RoutineID, &row.RoutineName, &rt, &days); err != nil {
				return err
			}
			if rt != nil {
				t := rt.Format("15:04")
				row.RunTime = &t
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

// UpdateReadinessTag — PATCH /api/v1/readiness/tags/{id}. Gives the tag a
// check, or changes it: per field, absent = no change, null = inherit the
// default, value = set. The first PATCH creates the tag's check even with
// no fields, so every field inherits until set.
func (h *Handler) UpdateReadinessTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if !uuidRe.MatchString(tagID) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	var req updateReadinessRoomReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	fields, routine, msg := parseReadinessFields(req)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	p, _ := portalauth.From(r.Context())
	notFound, badRoutine := false, false
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
		if routine != "" {
			mine, err := routineIsCustomers(ctx, tx, routine)
			if err != nil {
				return err
			}
			if !mine {
				badRoutine = true
				return nil
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tag_readiness_config (customer_id, tag_id) VALUES ($1, $2)
			ON CONFLICT (tag_id) DO NOTHING`, p.CustomerID, tagID); err != nil {
			return err
		}
		payload := map[string]any{"tag_id": tagID, "tag": name}
		set, args := setReadinessFields(fields, []any{tagID}, payload)
		if len(set) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE tag_readiness_config SET "+strings.Join(set, ", ")+
				" WHERE tag_id = $1", args...); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "readiness.tag.update",
			TargetKind: "room_tag", TargetID: tagID,
			After: mustJSON(payload),
		}))
	})
	switch {
	case !ok:
		return
	case notFound:
		writeErr(w, http.StatusNotFound, "tag not found")
	case badRoutine:
		writeErr(w, http.StatusBadRequest, "test_routine_id is not one of your routines")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ResetReadinessTag — DELETE /api/v1/readiness/tags/{id}. Removes the
// tag's check; its rooms go back to their next tag's check, or the default.
func (h *Handler) ResetReadinessTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if !uuidRe.MatchString(tagID) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	p, _ := portalauth.From(r.Context())
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM tag_readiness_config WHERE tag_id = $1`, tagID)
		if err != nil || res.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "readiness.tag.reset",
			TargetKind: "room_tag", TargetID: tagID,
		}))
	})
	if !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetReadinessTagOrder — PUT /api/v1/readiness/tag-order {tag_ids}: the
// tags in priority order, first wins. Tags left out go after them, by name.
func (h *Handler) SetReadinessTagOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TagIDs []string `json:"tag_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ids := uniqueStrings(req.TagIDs)
	for _, id := range ids {
		if !uuidRe.MatchString(id) {
			writeErr(w, http.StatusBadRequest, "unknown tag: "+id)
			return
		}
	}
	p, _ := portalauth.From(r.Context())
	unknown := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM room_tags WHERE id = ANY($1::uuid[])`, ids).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			unknown = true
			return nil
		}
		// Positions from the array; everything not listed loses its place.
		if _, err := tx.Exec(ctx, `
			UPDATE room_tags t
			   SET priority = o.pos
			  FROM (SELECT id, pos::int FROM unnest($1::uuid[]) WITH ORDINALITY AS u(id, pos)) o
			 WHERE t.id = o.id`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE room_tags SET priority = NULL WHERE NOT (id = ANY($1::uuid[]))`, ids); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "readiness.tag_order", TargetKind: "room_tag",
			After: mustJSON(map[string]any{"tag_ids": ids}),
		}))
	})
	if !ok {
		return
	}
	if unknown {
		writeErr(w, http.StatusBadRequest, "unknown tag")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
