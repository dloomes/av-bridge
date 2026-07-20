package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Nightly Room Readiness — per-room overrides.
//
// Slice 2A of Phase A. This is the second half of the schedule surface:
// slice 1 covers the customer default; this covers per-room deviations
// (custom times / days, or a manual exclusion until a date). Nullable
// fields in the override row inherit the customer default at read time
// via COALESCE — a room with no override row is fine, it simply inherits
// everything.
//
// Endpoints:
//   GET    /api/v1/nightly/rooms          list rooms + effective schedule
//   PATCH  /api/v1/nightly/rooms/{id}     upsert an override
//   DELETE /api/v1/nightly/rooms/{id}     clear the override (revert to inherit)

// roomOverrideRow is the wire shape for the list response. Effective values
// resolve NULL-in-override → customer default, so the portal can render a
// single "what will actually happen tonight" value plus a `has_override`
// flag to badge customised rooms.
type roomOverrideRow struct {
	RoomID              string  `json:"room_id"`
	RoomName            string  `json:"room_name"`
	BuildingID          string  `json:"building_id"`
	BuildingName        string  `json:"building_name"`
	LocationName        string  `json:"location_name,omitempty"`
	RegionName          string  `json:"region_name,omitempty"`
	EffectivePowerOff   string  `json:"effective_power_off_time"`  // HH:MM
	EffectivePowerOn    string  `json:"effective_power_on_time"`   // HH:MM
	EffectiveDaysOfWeek []int   `json:"effective_days_of_week"`
	HasOverride         bool    `json:"has_override"`
	OverridePowerOff    *string `json:"override_power_off_time,omitempty"`
	OverridePowerOn     *string `json:"override_power_on_time,omitempty"`
	OverrideDays        *[]int  `json:"override_days_of_week,omitempty"`
	ExcludedUntil       *string `json:"excluded_until,omitempty"` // YYYY-MM-DD
}

// ListNightlyRooms — GET /api/v1/nightly/rooms
//
// Returns every room the caller can see (RLS + building_scope apply) with
// its effective schedule resolved against the customer default. Also
// auto-provisions the customer schedule row on first read so the join
// always has a right-hand side — same convenience as GetNightlySchedule.
func (h *Handler) ListNightlyRooms(w http.ResponseWriter, r *http.Request) {
	out := []roomOverrideRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Ensure the customer default exists so COALESCE has something.
		if _, err := loadSchedule(ctx, tx); errors.Is(err, pgx.ErrNoRows) {
			if err := insertDefaultSchedule(ctx, tx); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		// Single query, LEFT JOIN across the hierarchy so unplaced /
		// mid-hierarchy rooms still render. COALESCE picks override fields
		// first, falls back to the customer default. RLS handles the
		// tenant filter on rooms; the joined tables are all tenant-scoped
		// tables so their rows will drop out for wrong-tenant reads. The
		// nightly_schedule row is loaded via the customer_id on rooms so
		// no additional filter is needed.
		rows, err := tx.Query(ctx, `
			SELECT
			  r.id::text                                          AS room_id,
			  r.name                                              AS room_name,
			  b.id::text                                          AS building_id,
			  b.name                                              AS building_name,
			  loc.name                                            AS location_name,
			  reg.name                                            AS region_name,
			  COALESCE(rnc.power_off_time, ns.power_off_time)     AS eff_power_off,
			  COALESCE(rnc.power_on_time,  ns.power_on_time)      AS eff_power_on,
			  COALESCE(rnc.days_of_week,   ns.days_of_week)       AS eff_days,
			  (rnc.id IS NOT NULL)                                AS has_override,
			  rnc.power_off_time,
			  rnc.power_on_time,
			  rnc.days_of_week,
			  rnc.excluded_until
			FROM rooms r
			LEFT JOIN buildings b               ON b.id = r.building_id
			LEFT JOIN locations loc             ON loc.id = b.location_id
			LEFT JOIN regions   reg             ON reg.id = loc.region_id
			LEFT JOIN room_nightly_config rnc   ON rnc.room_id = r.id
			LEFT JOIN nightly_schedule ns       ON ns.customer_id = r.customer_id
			ORDER BY reg.name NULLS LAST,
			         loc.name NULLS LAST,
			         b.name   NULLS LAST,
			         r.name
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row               roomOverrideRow
				loc, reg          *string
				effOff, effOn     time.Time
				effDays           []int32
				overOff, overOn   *time.Time
				overDays          []int32
				excludedUntil     *time.Time
			)
			if err := rows.Scan(
				&row.RoomID, &row.RoomName,
				&row.BuildingID, &row.BuildingName,
				&loc, &reg,
				&effOff, &effOn, &effDays,
				&row.HasOverride,
				&overOff, &overOn, &overDays,
				&excludedUntil,
			); err != nil {
				return err
			}
			if loc != nil {
				row.LocationName = *loc
			}
			if reg != nil {
				row.RegionName = *reg
			}
			row.EffectivePowerOff = effOff.Format("15:04")
			row.EffectivePowerOn = effOn.Format("15:04")
			row.EffectiveDaysOfWeek = int32sToInts(effDays)
			if overOff != nil {
				s := overOff.Format("15:04")
				row.OverridePowerOff = &s
			}
			if overOn != nil {
				s := overOn.Format("15:04")
				row.OverridePowerOn = &s
			}
			if overDays != nil {
				d := int32sToInts(overDays)
				row.OverrideDays = &d
			}
			if excludedUntil != nil {
				s := excludedUntil.Format("2006-01-02")
				row.ExcludedUntil = &s
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

// updateRoomOverrideReq uses value json.RawMessage (not pointer) so we can
// distinguish three states per field:
//   - absent from payload            → RawMessage is nil / len 0
//   - explicit JSON null             → RawMessage is []byte("null")
//   - concrete value                 → RawMessage contains the raw bytes
//
// The distinction matters: `null` means "clear this override, revert to
// inheriting the customer default"; omitting the field means "no change".
// A *json.RawMessage collapses both absent-and-null to a nil pointer, so
// we can't use the pointer form for this behaviour.
type updateRoomOverrideReq struct {
	PowerOffTime  json.RawMessage `json:"power_off_time"`
	PowerOnTime   json.RawMessage `json:"power_on_time"`
	DaysOfWeek    json.RawMessage `json:"days_of_week"`
	ExcludedUntil json.RawMessage `json:"excluded_until"`
}

// UpdateRoomOverride — PATCH /api/v1/nightly/rooms/{id}
//
// Upserts a room_nightly_config row. Explicit JSON null clears a field
// (inherits customer default); omitting a field leaves it alone. RLS +
// building_scope apply — a scoped user can only edit rooms in their
// buildings, and only within their own tenant.
func (h *Handler) UpdateRoomOverride(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		writeErr(w, http.StatusBadRequest, "room id required")
		return
	}

	var req updateRoomOverrideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Parse + validate before hitting the DB so bad input returns 400.
	type patch struct {
		set   bool // field was in the payload at all
		clear bool // explicit null in payload
		v     any  // typed value when set && !clear
	}
	parseTime := func(raw json.RawMessage) (patch, error) {
		if len(raw) == 0 {
			return patch{}, nil
		}
		if string(raw) == "null" {
			return patch{set: true, clear: true}, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return patch{}, errors.New("must be HH:MM string or null")
		}
		if _, err := time.Parse("15:04", s); err != nil {
			return patch{}, errors.New("must be HH:MM")
		}
		return patch{set: true, v: s}, nil
	}
	parseDays := func(raw json.RawMessage) (patch, error) {
		if len(raw) == 0 {
			return patch{}, nil
		}
		if string(raw) == "null" {
			return patch{set: true, clear: true}, nil
		}
		var days []int
		if err := json.Unmarshal(raw, &days); err != nil {
			return patch{}, errors.New("must be array of ISO weekdays or null")
		}
		for _, d := range days {
			if d < 1 || d > 7 {
				return patch{}, errors.New("weekday values must be 1-7")
			}
		}
		return patch{set: true, v: days}, nil
	}
	parseDate := func(raw json.RawMessage) (patch, error) {
		if len(raw) == 0 {
			return patch{}, nil
		}
		if string(raw) == "null" {
			return patch{set: true, clear: true}, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return patch{}, errors.New("must be YYYY-MM-DD string or null")
		}
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return patch{}, errors.New("must be YYYY-MM-DD")
		}
		return patch{set: true, v: t}, nil
	}

	off, err := parseTime(req.PowerOffTime)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "power_off_time "+err.Error())
		return
	}
	on, err := parseTime(req.PowerOnTime)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "power_on_time "+err.Error())
		return
	}
	days, err := parseDays(req.DaysOfWeek)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "days_of_week "+err.Error())
		return
	}
	excluded, err := parseDate(req.ExcludedUntil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "excluded_until "+err.Error())
		return
	}
	// If both times end up set (either via override or would-be-clear +
	// existing stored value), we can't check off != on here without a DB
	// round-trip. The migration's CHECK handles same-value; we fall back
	// to the 500 in that pathological case since it's already gated by
	// the customer default check on the schedule row.

	p, _ := portalauth.From(r.Context())

	// notFound is set inside the tx closure when the room isn't visible
	// to the caller. withTenant returns 500 on any error, so we can't use
	// a sentinel error for the 404 path — capture the state instead and
	// respond after withTenant closes cleanly.
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Verify the room exists inside the caller's scope before writing —
		// RLS on rooms will hide out-of-scope rows, so a lookup that finds
		// nothing means the room isn't visible. Deliberately not
		// distinguishing "room doesn't exist" from "not in scope" — a 404
		// for both keeps scoped users from fingerprinting room IDs.
		var customerID string
		err := tx.QueryRow(ctx,
			`SELECT customer_id::text FROM rooms WHERE id = $1`, roomID,
		).Scan(&customerID)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}

		// UPSERT: insert if the row doesn't exist, update otherwise.
		// Building the dynamic SET is straightforward with a fresh row
		// (all cleared fields become NULL); for an update we emit only
		// changed columns so we don't accidentally clear things the
		// caller didn't touch.
		//
		// Two-step is easier to reason about than a single INSERT ...
		// ON CONFLICT with dynamic columns.
		var existingID string
		existsErr := tx.QueryRow(ctx,
			`SELECT id::text FROM room_nightly_config WHERE room_id = $1`, roomID,
		).Scan(&existingID)
		if errors.Is(existsErr, pgx.ErrNoRows) {
			// Fresh row — construct with just the set values.
			cols := []string{"customer_id", "room_id"}
			args := []any{customerID, roomID}
			placeholders := []string{"$1", "$2"}
			addCol := func(col string, v any) {
				cols = append(cols, col)
				args = append(args, v)
				placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
			}
			if off.set && !off.clear {
				addCol("power_off_time", off.v)
			}
			if on.set && !on.clear {
				addCol("power_on_time", on.v)
			}
			if days.set && !days.clear {
				addCol("days_of_week", days.v)
			}
			if excluded.set && !excluded.clear {
				addCol("excluded_until", excluded.v)
			}
			sql := fmt.Sprintf(
				"INSERT INTO room_nightly_config (%s) VALUES (%s)",
				strings.Join(cols, ", "),
				strings.Join(placeholders, ", "),
			)
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return err
			}
		} else if existsErr != nil {
			return existsErr
		} else {
			// UPDATE only the set fields — explicit null clears, otherwise
			// value passed through.
			set := []string{}
			args := []any{roomID}
			add := func(col string, val any) {
				args = append(args, val)
				set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
			}
			if off.set {
				if off.clear {
					add("power_off_time", nil)
				} else {
					add("power_off_time", off.v)
				}
			}
			if on.set {
				if on.clear {
					add("power_on_time", nil)
				} else {
					add("power_on_time", on.v)
				}
			}
			if days.set {
				if days.clear {
					add("days_of_week", nil)
				} else {
					add("days_of_week", days.v)
				}
			}
			if excluded.set {
				if excluded.clear {
					add("excluded_until", nil)
				} else {
					add("excluded_until", excluded.v)
				}
			}
			if len(set) == 0 {
				return nil
			}
			sql := "UPDATE room_nightly_config SET " + strings.Join(set, ", ") +
				" WHERE room_id = $1"
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return err
			}
		}

		// Audit — capture the requested changes.
		payload := map[string]any{"room_id": roomID}
		if off.set {
			if off.clear {
				payload["power_off_time"] = nil
			} else {
				payload["power_off_time"] = off.v
			}
		}
		if on.set {
			if on.clear {
				payload["power_on_time"] = nil
			} else {
				payload["power_on_time"] = on.v
			}
		}
		if days.set {
			if days.clear {
				payload["days_of_week"] = nil
			} else {
				payload["days_of_week"] = days.v
			}
		}
		if excluded.set {
			if excluded.clear {
				payload["excluded_until"] = nil
			} else {
				payload["excluded_until"] = excluded.v.(time.Time).Format("2006-01-02")
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.room_override.update",
			TargetKind: "room", TargetID: roomID,
			After: mustJSON(payload),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteRoomOverride — DELETE /api/v1/nightly/rooms/{id}
//
// Removes the room_nightly_config row so the room reverts to inheriting
// every field from the customer default. Idempotent — deleting a non-
// existent override returns 204.
func (h *Handler) DeleteRoomOverride(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		writeErr(w, http.StatusBadRequest, "room id required")
		return
	}
	p, _ := portalauth.From(r.Context())
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Same visibility check as UpdateRoomOverride.
		var customerID string
		err := tx.QueryRow(ctx,
			`SELECT customer_id::text FROM rooms WHERE id = $1`, roomID,
		).Scan(&customerID)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM room_nightly_config WHERE room_id = $1`, roomID,
		); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.room_override.clear",
			TargetKind: "room", TargetID: roomID,
			After: mustJSON(map[string]any{"room_id": roomID}),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func int32sToInts(in []int32) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}
