package portalapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// Display links show shared dashboards on a wall screen with no one
// signed in (migration 0059, portalauth.DisplayResolver). Managing them
// needs dashboard.share; the link itself can only view.

type displayLink struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	DashboardIDs  []string   `json:"dashboard_ids"`
	RotateSeconds int        `json:"rotate_seconds"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
}

// ListDisplayLinks — GET /api/v1/display-links (dashboard.share)
func (h *Handler) ListDisplayLinks(w http.ResponseWriter, r *http.Request) {
	out := []displayLink{}
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, dashboard_ids::text[], rotate_seconds, created_by, created_at, last_used_at
			  FROM dashboard_display_links
			 WHERE revoked_at IS NULL
			 ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l displayLink
			if err := rows.Scan(&l.ID, &l.Name, &l.DashboardIDs, &l.RotateSeconds, &l.CreatedBy, &l.CreatedAt, &l.LastUsedAt); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// CreateDisplayLink — POST /api/v1/display-links (dashboard.share)
// body: { name, dashboard_ids: [shared dashboard ids, in order], rotate_seconds }
//
// Returns the link with its token, shown once; only a hash is kept.
func (h *Handler) CreateDisplayLink(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	var body struct {
		Name          string   `json:"name"`
		DashboardIDs  []string `json:"dashboard_ids"`
		RotateSeconds int      `json:"rotate_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name, err := cleanDashboardName(body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var ids []string // in the order given, each once
	for _, id := range body.DashboardIDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 10 {
		writeErr(w, http.StatusBadRequest, "choose between 1 and 10 shared dashboards")
		return
	}
	if body.RotateSeconds == 0 {
		body.RotateSeconds = 60
	}
	if body.RotateSeconds < 10 || body.RotateSeconds > 3600 {
		writeErr(w, http.StatusBadRequest, "rotate_seconds must be between 10 and 3600")
		return
	}
	scope, _ := json.Marshal(portalauth.ScopeOf(p))

	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := portalauth.DisplayTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	var (
		l      displayLink
		badIDs bool
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var shared int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM dashboards
			 WHERE id::text = ANY($1) AND owner_user_id IS NULL`, ids).Scan(&shared); err != nil {
			return err
		}
		if shared != len(ids) {
			badIDs = true
			return nil
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO dashboard_display_links
			       (customer_id, name, token_hash, dashboard_ids, rotate_seconds, scope, created_by)
			VALUES ($1, $2, $3, $4::uuid[], $5, $6::jsonb, $7)
			RETURNING id::text, name, dashboard_ids::text[], rotate_seconds, created_by, created_at, last_used_at`,
			p.CustomerID, name, portalauth.HashToken(token), ids, body.RotateSeconds, string(scope), p.ActorLabel()).
			Scan(&l.ID, &l.Name, &l.DashboardIDs, &l.RotateSeconds, &l.CreatedBy, &l.CreatedAt, &l.LastUsedAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "display_link.create", TargetKind: "display_link", TargetID: l.ID,
			Metadata: map[string]any{"name": name, "dashboards": len(ids), "rotate_seconds": body.RotateSeconds},
		}))
	}) {
		return
	}
	if badIDs {
		writeErr(w, http.StatusBadRequest, "a display link can only show shared dashboards")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"link": l, "token": token})
}

// RevokeDisplayLink — DELETE /api/v1/display-links/{id} (dashboard.share)
//
// The screen stops updating at once and shows that the link was revoked.
func (h *Handler) RevokeDisplayLink(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	id := r.PathValue("id")
	var found bool
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `
			UPDATE dashboard_display_links SET revoked_at = now()
			 WHERE id::text = $1 AND revoked_at IS NULL RETURNING name`, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "display_link.revoke", TargetKind: "display_link", TargetID: id,
			Metadata: map[string]any{"name": name},
		}))
	}) {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "display link not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CurrentDisplay — GET /api/v1/display
//
// For a display link: its name, the shared dashboards it still has (in
// its order), and how long to show each. A wall screen polls it, so a
// revoked link (401) or a dashboard that's been unshared stops showing.
func (h *Handler) CurrentDisplay(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	if p.DisplayLinkID == "" {
		writeErr(w, http.StatusNotFound, "not a display link")
		return
	}
	var (
		rotate int
		ids    []string
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT rotate_seconds FROM dashboard_display_links WHERE id::text = $1`, p.DisplayLinkID).Scan(&rotate); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT d.id::text
			  FROM unnest($1::uuid[]) WITH ORDINALITY AS l(id, n)
			  JOIN dashboards d ON d.id = l.id AND d.owner_user_id IS NULL
			 ORDER BY l.n`, p.DisplayDashboardIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}) {
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": p.Name, "dashboard_ids": ids, "rotate_seconds": rotate,
	})
}

// RoomsInUse — GET /api/v1/rooms/in-use
//
// Rooms in use right now: a device reported a call (active_calls > 0) or
// people in the room (people_count > 0) in its latest reading, within
// the last 10 minutes. Same signals as RoomUsageReport.
func (h *Handler) RoomsInUse(w http.ResponseWriter, r *http.Request) {
	type row struct {
		RoomID       string `json:"room_id"`
		RoomName     string `json:"room_name"`
		BuildingName string `json:"building_name"`
		InCall       bool   `json:"in_call"`
		People       *int   `json:"people"`
	}
	out := []row{}
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.name, COALESCE(b.name, ''),
			       bool_or(jsonb_typeof(d.latest_metrics->'active_calls') = 'number'
			               AND (d.latest_metrics->>'active_calls')::numeric > 0),
			       MAX(CASE WHEN jsonb_typeof(d.latest_metrics->'people_count') = 'number'
			                 AND (d.latest_metrics->>'people_count')::numeric >= 0
			                THEN (d.latest_metrics->>'people_count')::numeric END)::int
			  FROM devices d
			  JOIN rooms r ON r.id = d.room_id
			  LEFT JOIN buildings b ON b.id = r.building_id
			 WHERE d.deleted_at IS NULL
			   AND d.latest_status = 'online'
			   AND d.last_seen_at > now() - interval '10 minutes'
			 GROUP BY r.id, r.name, b.name
			HAVING bool_or(jsonb_typeof(d.latest_metrics->'active_calls') = 'number'
			               AND (d.latest_metrics->>'active_calls')::numeric > 0)
			    OR bool_or(jsonb_typeof(d.latest_metrics->'people_count') = 'number'
			               AND (d.latest_metrics->>'people_count')::numeric > 0)
			 ORDER BY b.name NULLS LAST, r.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName, &rr.InCall, &rr.People); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	}) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
