package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Moving devices between collectors — the cold-standby path. When a
// collector's machine dies, an operator enrols a replacement as a new
// collector and moves the devices across (one at a time, or all at once
// with ReplaceCollector) instead of recreating them.
//
// What a move does, in one tx:
//   - devices.collector_id → target. The devices trigger (migration 0049)
//     bumps both collectors' config_version and NOTIFYs them, so each
//     bridge re-pulls its config within seconds: the old one stops
//     polling the device, the new one starts.
//   - tombstones (old collector, reported_id) so late or spooled
//     telemetry from the old collector can't recreate the device there.
//   - pending commands follow the device to the target collector.
//
// All-or-nothing: if any device's reported_id is already taken on the
// target (including by a deleted device — the unique key covers those),
// nothing moves and the conflicts are returned.

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// maxMoveDevices bounds one move request. ReplaceCollector isn't bound by
// it — it moves whatever the collector has.
const maxMoveDevices = 500

type moveConflict struct {
	DeviceID   string `json:"device_id"`
	ReportedID string `json:"reported_id"`
	Name       string `json:"name,omitempty"`
}

type moveResult struct {
	Moved     int            `json:"moved"`
	Unchanged int            `json:"unchanged"` // already on the target
	Conflicts []moveConflict `json:"conflicts,omitempty"`
}

var (
	errMoveTargetNotFound = errors.New("target collector not found")
	errMoveDeviceNotFound = errors.New("device not found")
)

// moveDevices moves deviceIDs to target inside tx. Returns a result with
// Conflicts set (and nothing changed) when reported_ids clash on the target.
func moveDevices(ctx context.Context, tx pgx.Tx, p portalauth.Principal, deviceIDs []string, target string) (moveResult, error) {
	var res moveResult

	var targetExists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM collectors WHERE id = $1)`, target,
	).Scan(&targetExists); err != nil {
		return res, err
	}
	if !targetExists {
		return res, errMoveTargetNotFound
	}

	type dev struct {
		id, collectorID, reportedID, name string
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, collector_id::text, COALESCE(reported_id, ''), COALESCE(name, '')
		  FROM devices
		 WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL
		 ORDER BY id
		   FOR UPDATE`, deviceIDs)
	if err != nil {
		return res, err
	}
	var found []dev
	for rows.Next() {
		var d dev
		if err := rows.Scan(&d.id, &d.collectorID, &d.reportedID, &d.name); err != nil {
			rows.Close()
			return res, err
		}
		found = append(found, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(found) != len(deviceIDs) {
		return res, errMoveDeviceNotFound
	}

	var moving []dev
	for _, d := range found {
		if d.collectorID == target {
			res.Unchanged++
			continue
		}
		moving = append(moving, d)
	}
	if len(moving) == 0 {
		return res, nil
	}

	ids := make([]string, len(moving))
	fromCollectors := make([]string, len(moving))
	reportedIDs := make([]string, len(moving))
	byReported := make(map[string]dev, len(moving))
	for i, d := range moving {
		ids[i] = d.id
		fromCollectors[i] = d.collectorID
		reportedIDs[i] = d.reportedID
		byReported[d.reportedID] = d
	}

	// Two devices in the same request can't share a reported_id on the
	// target either (possible when moving from two different collectors).
	if len(byReported) != len(moving) {
		seen := map[string]bool{}
		for _, d := range moving {
			if seen[d.reportedID] {
				res.Conflicts = append(res.Conflicts, moveConflict{DeviceID: d.id, ReportedID: d.reportedID, Name: d.name})
			}
			seen[d.reportedID] = true
		}
		return res, nil
	}

	crows, err := tx.Query(ctx, `
		SELECT reported_id FROM devices
		 WHERE collector_id = $1 AND reported_id = ANY($2::text[])`,
		target, reportedIDs)
	if err != nil {
		return res, err
	}
	for crows.Next() {
		var rid string
		if err := crows.Scan(&rid); err != nil {
			crows.Close()
			return res, err
		}
		d := byReported[rid]
		res.Conflicts = append(res.Conflicts, moveConflict{DeviceID: d.id, ReportedID: rid, Name: d.name})
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return res, err
	}
	if len(res.Conflicts) > 0 {
		return res, nil
	}

	befores := make(map[string]json.RawMessage, len(moving))
	for _, d := range moving {
		snap, err := audit.SnapshotDevice(ctx, tx, d.id)
		if err != nil {
			return res, err
		}
		befores[d.id] = snap
	}

	if _, err := tx.Exec(ctx,
		`UPDATE devices SET collector_id = $1 WHERE id = ANY($2::uuid[])`, target, ids,
	); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO collector_device_tombstones (customer_id, collector_id, reported_id)
		SELECT $1, f.collector_id, f.reported_id
		  FROM unnest($2::uuid[], $3::text[]) AS f(collector_id, reported_id)
		 WHERE f.reported_id <> ''
		ON CONFLICT DO NOTHING`,
		p.CustomerID, fromCollectors, reportedIDs,
	); err != nil {
		return res, err
	}
	// Moving a device back to a collector it once left: lift that
	// collector's tombstone so its telemetry is accepted again.
	if _, err := tx.Exec(ctx, `
		DELETE FROM collector_device_tombstones
		 WHERE collector_id = $1 AND reported_id = ANY($2::text[])`,
		target, reportedIDs,
	); err != nil {
		return res, err
	}
	// Pending commands follow the device. In-flight ones stay with the
	// collector that claimed them: it either reports a result or the
	// sweeper fails them.
	if _, err := tx.Exec(ctx, `
		UPDATE commands SET collector_id = $1
		 WHERE device_id = ANY($2::uuid[]) AND status = 'pending'`,
		target, ids,
	); err != nil {
		return res, err
	}

	for _, d := range moving {
		after, err := audit.SnapshotDevice(ctx, tx, d.id)
		if err != nil {
			return res, err
		}
		if err := audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "device.move", TargetKind: "device", TargetID: d.id,
			RelatedTargetKind: "collector", RelatedTargetID: target,
			Before: befores[d.id], After: after,
			Metadata: map[string]any{
				"from_collector_id": d.collectorID,
				"to_collector_id":   target,
			},
		})); err != nil {
			return res, err
		}
	}
	res.Moved = len(moving)
	return res, nil
}

type moveDevicesReq struct {
	DeviceIDs         []string `json:"device_ids"`
	TargetCollectorID string   `json:"target_collector_id"`
}

// MoveDevices — POST /api/v1/devices/move
//
// Runs under the caller's physical scope, so a scoped user can only move
// devices they can see.
func (h *Handler) MoveDevices(w http.ResponseWriter, r *http.Request) {
	var req moveDevicesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !uuidRe.MatchString(req.TargetCollectorID) {
		writeErr(w, http.StatusBadRequest, "target_collector_id is required")
		return
	}
	if len(req.DeviceIDs) == 0 || len(req.DeviceIDs) > maxMoveDevices {
		writeErr(w, http.StatusBadRequest, "device_ids must list 1–500 devices")
		return
	}
	seen := make(map[string]bool, len(req.DeviceIDs))
	ids := make([]string, 0, len(req.DeviceIDs))
	for _, id := range req.DeviceIDs {
		if !uuidRe.MatchString(id) {
			writeErr(w, http.StatusBadRequest, "device_ids must be UUIDs")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	p, _ := portalauth.From(r.Context())
	var (
		res     moveResult
		moveErr error
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = moveDevices(ctx, tx, p, ids, req.TargetCollectorID)
		if errors.Is(err, errMoveTargetNotFound) || errors.Is(err, errMoveDeviceNotFound) {
			moveErr = err
			return nil
		}
		return err
	})
	if !ok {
		return
	}
	writeMoveResult(w, res, moveErr)
}

type replaceCollectorReq struct {
	TargetCollectorID string `json:"target_collector_id"`
}

// ReplaceCollector — POST /api/v1/collectors/{id}/replace
//
// Moves every live device from collector {id} to the target. Typical use:
// the machine behind {id} has died, a replacement was enrolled as a new
// collector, and this hands it the whole device set. The old collector is
// left in place (empty) for the operator to delete.
//
// Needs whole-tenant scope: a scoped caller can't see every device on the
// collector, and a "replace" that silently leaves some behind is worse
// than a refusal.
func (h *Handler) ReplaceCollector(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	if !principalScope(p).Empty() {
		writeErr(w, http.StatusForbidden, "replacing a collector needs access to the whole estate")
		return
	}
	source := r.PathValue("id")
	var req replaceCollectorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !uuidRe.MatchString(source) {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}
	if !uuidRe.MatchString(req.TargetCollectorID) {
		writeErr(w, http.StatusBadRequest, "target_collector_id is required")
		return
	}
	if source == req.TargetCollectorID {
		writeErr(w, http.StatusBadRequest, "choose a different collector to move the devices to")
		return
	}

	var (
		res           moveResult
		moveErr       error
		sourceMissing bool
	)
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM collectors WHERE id = $1)`, source,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			sourceMissing = true
			return nil
		}
		var ids []string
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM devices WHERE collector_id = $1 AND deleted_at IS NULL`, source)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) > 0 {
			res, err = moveDevices(ctx, tx, p, ids, req.TargetCollectorID)
			if errors.Is(err, errMoveTargetNotFound) {
				moveErr = err
				return nil
			}
			if err != nil {
				return err
			}
		} else {
			var targetExists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM collectors WHERE id = $1)`, req.TargetCollectorID,
			).Scan(&targetExists); err != nil {
				return err
			}
			if !targetExists {
				moveErr = errMoveTargetNotFound
				return nil
			}
		}
		if len(res.Conflicts) > 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "collector.replace", TargetKind: "collector", TargetID: source,
			RelatedTargetKind: "collector", RelatedTargetID: req.TargetCollectorID,
			Metadata: map[string]any{"moved": res.Moved},
		}))
	})
	if !ok {
		return
	}
	if sourceMissing {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}
	writeMoveResult(w, res, moveErr)
}

func writeMoveResult(w http.ResponseWriter, res moveResult, moveErr error) {
	switch {
	case errors.Is(moveErr, errMoveTargetNotFound):
		writeErr(w, http.StatusBadRequest, "target collector not found in this customer")
	case errors.Is(moveErr, errMoveDeviceNotFound):
		writeErr(w, http.StatusNotFound, "one or more devices not found")
	case len(res.Conflicts) > 0:
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "the target collector already has devices with these reported IDs (deleted devices count too); nothing was moved",
			"conflicts": res.Conflicts,
		})
	default:
		writeJSON(w, http.StatusOK, res)
	}
}
