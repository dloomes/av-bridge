package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// Readiness checks — the routine half of Room Readiness, separate from the
// power schedule (nightly.go / nightly_rooms.go). A customer default
// (readiness_schedule) plus per-room overrides (room_readiness_config);
// NULL in an override inherits the default.
//
//   GET    /api/v1/readiness/schedule      the default (auto-created)
//   PATCH  /api/v1/readiness/schedule
//   GET    /api/v1/readiness/rooms         every room, effective settings
//   PATCH  /api/v1/readiness/rooms/{id}    set / clear (null) a room's own
//   DELETE /api/v1/readiness/rooms/{id}    back to the default
//
// Gated on nightly.view / nightly.manage, like the power schedule.

type readinessScheduleOut struct {
	Enabled         bool   `json:"enabled"`
	TestRoutineID   string `json:"test_routine_id,omitempty"`
	TestRoutineName string `json:"test_routine_name,omitempty"`
	RunTime         string `json:"run_time"`     // HH:MM
	DaysOfWeek      []int  `json:"days_of_week"` // ISO 1-7
	Timezone        string `json:"timezone"`     // shared, from the Settings tab
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// ensureReadinessSchedule creates the customer's default rows (readiness,
// and the shared settings row that holds the timezone) on first use.
func ensureReadinessSchedule(ctx context.Context, tx pgx.Tx) error {
	if err := insertDefaultSchedule(ctx, tx); err != nil {
		return err
	}
	p, _ := portalauth.From(ctx)
	_, err := tx.Exec(ctx,
		`INSERT INTO readiness_schedule (customer_id) VALUES ($1) ON CONFLICT (customer_id) DO NOTHING`,
		p.CustomerID)
	return err
}

func loadReadinessSchedule(ctx context.Context, tx pgx.Tx) (readinessScheduleOut, error) {
	var (
		out                 readinessScheduleOut
		routineID, name, tz *string
		runTime, updated    time.Time
		days                []int32
	)
	err := tx.QueryRow(ctx, `
		SELECT rs.enabled, rs.test_routine_id::text, tr.name, rs.run_time, rs.days_of_week,
		       ns.timezone, rs.updated_at
		  FROM readiness_schedule rs
		  LEFT JOIN nightly_test_routine tr ON tr.id = rs.test_routine_id
		  LEFT JOIN nightly_schedule ns     ON ns.customer_id = rs.customer_id
		 LIMIT 1`).Scan(&out.Enabled, &routineID, &name, &runTime, &days, &tz, &updated)
	if err != nil {
		return out, err
	}
	out.TestRoutineID, out.TestRoutineName = deref(routineID), deref(name)
	out.RunTime = runTime.Format("15:04")
	out.DaysOfWeek = int32sToInts(days)
	out.Timezone = "Europe/London"
	if tz != nil {
		out.Timezone = *tz
	}
	out.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return out, nil
}

// GetReadinessSchedule — GET /api/v1/readiness/schedule
func (h *Handler) GetReadinessSchedule(w http.ResponseWriter, r *http.Request) {
	var out readinessScheduleOut
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := ensureReadinessSchedule(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = loadReadinessSchedule(ctx, tx)
		return err
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type updateReadinessScheduleReq struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	TestRoutineID *string `json:"test_routine_id,omitempty"` // "" clears
	RunTime       *string `json:"run_time,omitempty"`
	DaysOfWeek    *[]int  `json:"days_of_week,omitempty"`
}

// UpdateReadinessSchedule — PATCH /api/v1/readiness/schedule
func (h *Handler) UpdateReadinessSchedule(w http.ResponseWriter, r *http.Request) {
	var req updateReadinessScheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RunTime != nil && !isHHMM(*req.RunTime) {
		writeErr(w, http.StatusBadRequest, "run_time must be HH:MM")
		return
	}
	if req.DaysOfWeek != nil {
		if msg := checkDays(*req.DaysOfWeek); msg != "" {
			writeErr(w, http.StatusBadRequest, "days_of_week "+msg)
			return
		}
	}
	p, _ := portalauth.From(r.Context())
	badRoutine, noRoutine := false, false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := ensureReadinessSchedule(ctx, tx); err != nil {
			return err
		}
		if req.TestRoutineID != nil && *req.TestRoutineID != "" {
			mine, err := routineIsCustomers(ctx, tx, *req.TestRoutineID)
			if err != nil {
				return err
			}
			if !mine {
				badRoutine = true
				return nil
			}
		}
		// Switched on with no routine anywhere would quietly check nothing:
		// refuse it, judging by what the settings would be after this save.
		var enabled, defaultRoutine, roomRoutine bool
		if err := tx.QueryRow(ctx, `
			SELECT enabled, test_routine_id IS NOT NULL,
			       EXISTS (SELECT 1 FROM room_readiness_config WHERE test_routine_id IS NOT NULL)
			  FROM readiness_schedule LIMIT 1`).Scan(&enabled, &defaultRoutine, &roomRoutine); err != nil {
			return err
		}
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		if req.TestRoutineID != nil {
			defaultRoutine = *req.TestRoutineID != ""
		}
		if enabled && !defaultRoutine && !roomRoutine {
			noRoutine = true
			return nil
		}

		set, args := []string{}, []any{p.CustomerID}
		add := func(col string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		payload := map[string]any{}
		if req.Enabled != nil {
			add("enabled", *req.Enabled)
			payload["enabled"] = *req.Enabled
		}
		if req.TestRoutineID != nil {
			if *req.TestRoutineID == "" {
				add("test_routine_id", nil)
			} else {
				add("test_routine_id", *req.TestRoutineID)
			}
			payload["test_routine_id"] = *req.TestRoutineID
		}
		if req.RunTime != nil {
			add("run_time", *req.RunTime)
			payload["run_time"] = *req.RunTime
		}
		if req.DaysOfWeek != nil {
			add("days_of_week", *req.DaysOfWeek)
			payload["days_of_week"] = *req.DaysOfWeek
		}
		if len(set) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE readiness_schedule SET "+strings.Join(set, ", ")+
				" WHERE customer_id = $1", args...); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "readiness.schedule.update",
			TargetKind: "customer", TargetID: p.CustomerID,
			After: mustJSON(payload),
		}))
	})
	switch {
	case !ok:
		return
	case noRoutine:
		writeErr(w, http.StatusBadRequest, "choose a routine before switching readiness checks on")
		return
	case badRoutine:
		writeErr(w, http.StatusBadRequest, "test_routine_id is not one of your routines")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readinessRoomRow — one room's readiness settings, resolved against the
// default, plus the room's power times so the portal can warn when a
// check would land while the room is switched off.
type readinessRoomRow struct {
	RoomID       string `json:"room_id"`
	RoomName     string `json:"room_name"`
	BuildingID   string `json:"building_id"`
	BuildingName string `json:"building_name"`
	LocationName string `json:"location_name,omitempty"`
	RegionName   string `json:"region_name,omitempty"`

	EffectiveRoutineID   *string `json:"effective_test_routine_id,omitempty"`
	EffectiveRoutineName *string `json:"effective_test_routine_name,omitempty"`
	EffectiveRunTime     string  `json:"effective_run_time"`
	EffectiveDays        []int   `json:"effective_days_of_week"`

	HasOverride       bool    `json:"has_override"`
	OverrideRoutineID *string `json:"override_test_routine_id,omitempty"`
	OverrideRunTime   *string `json:"override_run_time,omitempty"`
	OverrideDays      *[]int  `json:"override_days_of_week,omitempty"`

	ExcludedUntil *string `json:"excluded_until,omitempty"` // shared with the power schedule

	// The room's power schedule, when it's switched on.
	PowerEnabled bool   `json:"power_enabled"`
	PowerOffTime string `json:"power_off_time,omitempty"`
	PowerOnTime  string `json:"power_on_time,omitempty"`
}

// ListReadinessRooms — GET /api/v1/readiness/rooms
func (h *Handler) ListReadinessRooms(w http.ResponseWriter, r *http.Request) {
	out := []readinessRoomRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := ensureReadinessSchedule(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.name, b.id::text, b.name, loc.name, reg.name,
			       tr.id::text, tr.name,
			       COALESCE(rrc.run_time, rs.run_time),
			       COALESCE(rrc.days_of_week, rs.days_of_week),
			       (rrc.id IS NOT NULL),
			       rrc.test_routine_id::text, rrc.run_time, rrc.days_of_week,
			       rnc.excluded_until,
			       COALESCE(ns.enabled, false),
			       COALESCE(rnc.power_off_time, ns.power_off_time),
			       COALESCE(rnc.power_on_time, ns.power_on_time)
			  FROM rooms r
			  LEFT JOIN buildings b               ON b.id = r.building_id
			  LEFT JOIN locations loc             ON loc.id = b.location_id
			  LEFT JOIN regions reg               ON reg.id = loc.region_id
			  LEFT JOIN readiness_schedule rs     ON rs.customer_id = r.customer_id
			  LEFT JOIN room_readiness_config rrc ON rrc.room_id = r.id
			  LEFT JOIN room_nightly_config rnc   ON rnc.room_id = r.id
			  LEFT JOIN nightly_schedule ns       ON ns.customer_id = r.customer_id
			  LEFT JOIN nightly_test_routine tr   ON tr.id = COALESCE(rrc.test_routine_id, rs.test_routine_id)
			 ORDER BY reg.name NULLS LAST, loc.name NULLS LAST, b.name NULLS LAST, r.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row               readinessRoomRow
				bID, bName        *string
				locName, regName  *string
				runTime           time.Time
				days              []int32
				ovTime            *time.Time
				ovDays            []int32
				excluded          *time.Time
				powerOff, powerOn *time.Time
			)
			if err := rows.Scan(&row.RoomID, &row.RoomName, &bID, &bName, &locName, &regName,
				&row.EffectiveRoutineID, &row.EffectiveRoutineName,
				&runTime, &days,
				&row.HasOverride, &row.OverrideRoutineID, &ovTime, &ovDays,
				&excluded, &row.PowerEnabled, &powerOff, &powerOn); err != nil {
				return err
			}
			row.BuildingID, row.BuildingName = deref(bID), deref(bName)
			row.LocationName, row.RegionName = deref(locName), deref(regName)
			row.EffectiveRunTime = runTime.Format("15:04")
			row.EffectiveDays = int32sToInts(days)
			if ovTime != nil {
				t := ovTime.Format("15:04")
				row.OverrideRunTime = &t
			}
			if ovDays != nil {
				d := int32sToInts(ovDays)
				row.OverrideDays = &d
			}
			if excluded != nil {
				d := excluded.Format("2006-01-02")
				row.ExcludedUntil = &d
			}
			if row.PowerEnabled && powerOff != nil && powerOn != nil {
				row.PowerOffTime = powerOff.Format("15:04")
				row.PowerOnTime = powerOn.Format("15:04")
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

// updateReadinessRoomReq — RawMessage per field: absent = no change,
// null = inherit the default, value = this room's own.
type updateReadinessRoomReq struct {
	TestRoutineID json.RawMessage `json:"test_routine_id"`
	RunTime       json.RawMessage `json:"run_time"`
	DaysOfWeek    json.RawMessage `json:"days_of_week"`
}

// UpdateReadinessRoom — PATCH /api/v1/readiness/rooms/{id}
func (h *Handler) UpdateReadinessRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	var req updateReadinessRoomReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	type field struct {
		col        string
		set, clear bool
		v          any
	}
	var fields []field
	parse := func(col string, raw json.RawMessage, check func(json.RawMessage) (any, string)) string {
		if len(raw) == 0 {
			return ""
		}
		if string(raw) == "null" {
			fields = append(fields, field{col: col, set: true, clear: true})
			return ""
		}
		v, msg := check(raw)
		if msg != "" {
			return col + " " + msg
		}
		fields = append(fields, field{col: col, set: true, v: v})
		return ""
	}
	var routine string
	for _, msg := range []string{
		parse("test_routine_id", req.TestRoutineID, func(raw json.RawMessage) (any, string) {
			if json.Unmarshal(raw, &routine) != nil || routine == "" {
				return nil, "must be a routine id or null"
			}
			return routine, ""
		}),
		parse("run_time", req.RunTime, func(raw json.RawMessage) (any, string) {
			var t string
			if json.Unmarshal(raw, &t) != nil || !isHHMM(t) {
				return nil, "must be HH:MM or null"
			}
			return t, ""
		}),
		parse("days_of_week", req.DaysOfWeek, func(raw json.RawMessage) (any, string) {
			var d []int
			if json.Unmarshal(raw, &d) != nil {
				return nil, "must be an array of ISO weekdays or null"
			}
			if msg := checkDays(d); msg != "" {
				return nil, msg
			}
			return d, ""
		}),
	} {
		if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}

	p, _ := portalauth.From(r.Context())
	notFound, badRoutine := false, false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var customerID string
		err := tx.QueryRow(ctx, `SELECT customer_id::text FROM rooms WHERE id::text = $1`, roomID).Scan(&customerID)
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
			INSERT INTO room_readiness_config (customer_id, room_id) VALUES ($1, $2)
			ON CONFLICT (room_id) DO NOTHING`, customerID, roomID); err != nil {
			return err
		}
		set, args := []string{}, []any{roomID}
		payload := map[string]any{"room_id": roomID}
		for _, f := range fields {
			var v any
			if !f.clear {
				v = f.v
			}
			args = append(args, v)
			set = append(set, fmt.Sprintf("%s = $%d", f.col, len(args)))
			payload[f.col] = v
		}
		if len(set) > 0 {
			if _, err := tx.Exec(ctx, "UPDATE room_readiness_config SET "+strings.Join(set, ", ")+
				" WHERE room_id = $1", args...); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "readiness.room.update",
			TargetKind: "room", TargetID: roomID,
			After: mustJSON(payload),
		}))
	})
	switch {
	case !ok:
		return
	case notFound:
		writeErr(w, http.StatusNotFound, "room not found")
		return
	case badRoutine:
		writeErr(w, http.StatusBadRequest, "test_routine_id is not one of your routines")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResetReadinessRoom — DELETE /api/v1/readiness/rooms/{id}
func (h *Handler) ResetReadinessRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	p, _ := portalauth.From(r.Context())
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM room_readiness_config WHERE room_id::text = $1`, roomID)
		if err != nil || res.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "readiness.room.reset",
			TargetKind: "room", TargetID: roomID,
		}))
	})
	if !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// routineIsCustomers reports whether the routine belongs to the caller's
// customer. FK checks ignore row security, so the FK alone would accept
// another tenant's routine id.
func routineIsCustomers(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var mine bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM nightly_test_routine WHERE id::text = $1)`, id).Scan(&mine)
	return mine, err
}

func checkDays(days []int) string {
	for _, d := range days {
		if d < 1 || d > 7 {
			return "values must be 1-7"
		}
	}
	return ""
}
