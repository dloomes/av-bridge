package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// FirmwareSummary — GET /api/v1/firmware
//
// Returns per-device firmware state. No automatic "outdated" flag — the
// portal renders a neutral per-(make, model) fleet breakdown so a beta on
// one device doesn't paint the rest of the fleet as behind. When a
// firmware_targets row is set for a (make, model), the target_version and
// docs_url ride along on every device in that bucket so the UI can badge
// devices against the customer's declared target and link out to the
// vendor's release notes.
//
// Devices with no reported firmware (never polled, or the adapter doesn't
// surface it) still appear so gaps are visible.
func (h *Handler) FirmwareSummary(w http.ResponseWriter, r *http.Request) {
	type item struct {
		DeviceID        string `json:"device_id"`
		Name            string `json:"name"`
		Location        string `json:"location"`
		Make            string `json:"make,omitempty"`
		Model           string `json:"model,omitempty"`
		FirmwareVersion string `json:"firmware_version,omitempty"`
		TargetVersion   string `json:"target_version,omitempty"`
		DocsURL         string `json:"docs_url,omitempty"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
			  d.id::text,
			  COALESCE(d.name, d.reported_id, '') AS dev_name,
			  CASE
			    WHEN b.name IS NOT NULL AND r.name IS NOT NULL THEN b.name || ' / ' || r.name
			    WHEN r.name IS NOT NULL THEN r.name
			    ELSE ''
			  END AS dev_location,
			  COALESCE(d.make, '')             AS mk,
			  COALESCE(d.model, '')            AS md,
			  COALESCE(d.firmware_version, '') AS fw,
			  COALESCE(ft.target_version, '')  AS target_ver,
			  COALESCE(ft.docs_url, '')        AS docs
			FROM devices d
			LEFT JOIN rooms r     ON r.id = d.room_id
			LEFT JOIN buildings b ON b.id = r.building_id
			LEFT JOIN firmware_targets ft
			  ON ft.make  = COALESCE(d.make, '')
			 AND ft.model = COALESCE(d.model, '')
			ORDER BY mk, md, dev_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.DeviceID, &it.Name, &it.Location,
				&it.Make, &it.Model, &it.FirmwareVersion,
				&it.TargetVersion, &it.DocsURL); err != nil {
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

// ----- firmware_targets CRUD ------------------------------------------------
//
// Admin-only via wrapAdmin at the route layer. Keyed on (make, model) inside
// the customer scope — the endpoint upserts on POST so admins don't have to
// distinguish create vs update in the UI.

type firmwareTargetRow struct {
	ID            string     `json:"id"`
	Make          string     `json:"make"`
	Model         string     `json:"model"`
	TargetVersion string     `json:"target_version,omitempty"`
	DocsURL       string     `json:"docs_url,omitempty"`
	Notes         string     `json:"notes,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	UpdatedBy     string     `json:"updated_by,omitempty"`
}

func (h *Handler) ListFirmwareTargets(w http.ResponseWriter, r *http.Request) {
	out := []firmwareTargetRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, make, model,
			       COALESCE(target_version,''), COALESCE(docs_url,''),
			       COALESCE(notes,''), updated_at, COALESCE(updated_by,'')
			  FROM firmware_targets
			 ORDER BY make, model`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t firmwareTargetRow
			if err := rows.Scan(&t.ID, &t.Make, &t.Model,
				&t.TargetVersion, &t.DocsURL, &t.Notes,
				&t.UpdatedAt, &t.UpdatedBy); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type upsertFirmwareTargetReq struct {
	Make          string `json:"make"`
	Model         string `json:"model"`
	TargetVersion string `json:"target_version,omitempty"`
	DocsURL       string `json:"docs_url,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// UpsertFirmwareTarget — POST /api/v1/firmware/targets
//
// Idempotent by (customer_id, make, model). Sending the same make/model
// again updates the existing row rather than creating a duplicate — matches
// how the UI treats these (edit-in-place per model card).
func (h *Handler) UpsertFirmwareTarget(w http.ResponseWriter, r *http.Request) {
	var req upsertFirmwareTargetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Make == "" || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "make and model are required")
		return
	}
	// At least one policy field must be present — an empty upsert is a
	// no-op that would confuse anyone reading the audit trail.
	if req.TargetVersion == "" && req.DocsURL == "" && req.Notes == "" {
		writeErr(w, http.StatusBadRequest, "at least one of target_version, docs_url, notes is required")
		return
	}

	p, _ := portalauth.From(r.Context())

	var id string
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO firmware_targets
			    (customer_id, make, model, target_version, docs_url, notes, updated_by)
			VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), $7)
			ON CONFLICT (customer_id, make, model) DO UPDATE SET
			    target_version = EXCLUDED.target_version,
			    docs_url       = EXCLUDED.docs_url,
			    notes          = EXCLUDED.notes,
			    updated_at     = now(),
			    updated_by     = EXCLUDED.updated_by
			RETURNING id::text`,
			p.CustomerID, req.Make, req.Model,
			req.TargetVersion, req.DocsURL, req.Notes, p.ActorLabel()).
			Scan(&id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "firmware_target.upsert",
			TargetKind: "firmware_target", TargetID: id,
			After: mustJSON(map[string]any{
				"make": req.Make, "model": req.Model,
				"target_version": req.TargetVersion, "docs_url": req.DocsURL,
			}),
		}))
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *Handler) DeleteFirmwareTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM firmware_targets WHERE id = $1`, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "firmware_target.delete",
			TargetKind: "firmware_target", TargetID: id,
		}))
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "firmware target not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
