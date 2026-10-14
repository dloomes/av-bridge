package pubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Room Readiness — the nightly power-cycle and test routine results.
// All three routes need the nightly.view scope. Tenant scoping comes from
// RLS on nightly_run / nightly_step_result, as everywhere else here.

// publicReadinessRun is one nightly run: a room's power-cycle and, when a
// routine was assigned, its test.
type publicReadinessRun struct {
	ID              string     `json:"id"`
	RoomID          string     `json:"room_id"`
	Room            string     `json:"room"`
	BuildingID      string     `json:"building_id"`
	Building        string     `json:"building"`
	RoutineID       *string    `json:"routine_id"`
	Routine         *string    `json:"routine"`
	Status          string     `json:"status"`
	Phase           string     `json:"phase"`
	ScheduledAt     time.Time  `json:"scheduled_at"`
	StartedAt       *time.Time `json:"started_at"`
	PoweredOnAt     *time.Time `json:"powered_on_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	DurationSeconds *int       `json:"duration_seconds"`
	FailureReason   *string    `json:"failure_reason"`
	StepsTotal      int        `json:"steps_total"`
	StepsPassed     int        `json:"steps_passed"`
	StepsFailed     int        `json:"steps_failed"`
}

// publicReadinessStep is one step of a run's test routine.
type publicReadinessStep struct {
	Index       int             `json:"index"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	DeviceID    *string         `json:"device_id"`
	Device      *string         `json:"device"`
	Passed      bool            `json:"passed"`
	Expected    json.RawMessage `json:"expected"`
	Actual      json.RawMessage `json:"actual"`
	Error       *string         `json:"error"`
	StartedAt   *time.Time      `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at"`
}

const publicRunColumns = `
	SELECT nr.id::text, nr.room_id::text, r.name, b.id::text, b.name,
	       nr.routine_id::text, tr.name,
	       nr.status, nr.phase, nr.scheduled_at, nr.started_at, nr.powered_on_at, nr.completed_at,
	       nr.failure_reason,
	       COALESCE(sc.total, 0), COALESCE(sc.passed, 0)
	  FROM nightly_run nr
	  JOIN rooms r                      ON r.id = nr.room_id
	  JOIN buildings b                  ON b.id = r.building_id
	  LEFT JOIN nightly_test_routine tr ON tr.id = nr.routine_id
	  LEFT JOIN LATERAL (
	        SELECT count(*)::int AS total, count(*) FILTER (WHERE passed)::int AS passed
	          FROM nightly_step_result WHERE run_id = nr.id
	  ) sc ON true`

func scanPublicRun(row pgx.Row) (publicReadinessRun, error) {
	var run publicReadinessRun
	err := row.Scan(&run.ID, &run.RoomID, &run.Room, &run.BuildingID, &run.Building,
		&run.RoutineID, &run.Routine,
		&run.Status, &run.Phase, &run.ScheduledAt, &run.StartedAt, &run.PoweredOnAt, &run.CompletedAt,
		&run.FailureReason, &run.StepsTotal, &run.StepsPassed)
	if err != nil {
		return run, err
	}
	run.StepsFailed = run.StepsTotal - run.StepsPassed
	// Measured from the morning power-on (how long the room took to be
	// ready), or from the start for a run without a power cycle.
	from := run.StartedAt
	if run.PoweredOnAt != nil {
		from = run.PoweredOnAt
	}
	if from != nil && run.CompletedAt != nil {
		d := int(run.CompletedAt.Sub(*from).Seconds())
		run.DurationSeconds = &d
	}
	return run, nil
}

var runStatuses = map[string]bool{
	"pending": true, "in_progress": true, "succeeded": true, "failed": true, "skipped": true,
}

// ListReadinessRuns — GET /pub/v1/room-readiness/runs
//
// Filters (all optional):
//
//	room_id      one room
//	status       comma-separated: pending, in_progress, succeeded, failed, skipped
//	since/until  RFC 3339, on scheduled_at; since inclusive, until exclusive
//	cursor+limit standard pagination
//
// Ordered by (scheduled_at, id) desc.
func (h *Handler) ListReadinessRuns(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	if cursor.TS != nil && !uuidRE.MatchString(cursor.ID) {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)
	qv := r.URL.Query()

	sql := publicRunColumns + " WHERE 1=1"
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}
	if room := strings.TrimSpace(qv.Get("room_id")); room != "" {
		if !uuidRE.MatchString(room) {
			writeErr(w, http.StatusBadRequest, "room_id must be a UUID")
			return
		}
		sql += " AND nr.room_id = " + arg(room) + "::uuid"
	}
	if v := strings.TrimSpace(qv.Get("status")); v != "" {
		var statuses []string
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if !runStatuses[s] {
				writeErr(w, http.StatusBadRequest, "status must be one or more of pending, in_progress, succeeded, failed, skipped")
				return
			}
			statuses = append(statuses, s)
		}
		sql += " AND nr.status = ANY(" + arg(statuses) + "::text[])"
	}
	for _, p := range []struct{ name, op string }{{"since", ">="}, {"until", "<"}} {
		v := strings.TrimSpace(qv.Get(p.name))
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, p.name+" must be an RFC 3339 timestamp, e.g. 2026-10-01T00:00:00Z")
			return
		}
		sql += " AND nr.scheduled_at " + p.op + " " + arg(t)
	}
	if cursor.TS != nil {
		sql += " AND (nr.scheduled_at, nr.id) < (" + arg(*cursor.TS) + ", " + arg(cursor.ID) + "::uuid)"
	}
	sql += " ORDER BY nr.scheduled_at DESC, nr.id DESC LIMIT " + arg(limit+1) + "::int"

	out := []publicReadinessRun{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanPublicRun(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	var next *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: &last.ScheduledAt, ID: last.ID})
		next = &nc
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, Page(out, next))
}

// GetReadinessRun — GET /pub/v1/room-readiness/runs/{id}
//
// One run with every step of its test routine, in order.
func (h *Handler) GetReadinessRun(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !uuidRE.MatchString(id) {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	type detail struct {
		publicReadinessRun
		Steps []publicReadinessStep `json:"steps"`
	}
	var (
		out      detail
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		run, err := scanPublicRun(tx.QueryRow(ctx, publicRunColumns+" WHERE nr.id = $1", id))
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		out.publicReadinessRun = run
		out.Steps = []publicReadinessStep{}
		rows, err := tx.Query(ctx, `
			SELECT sr.step_index, sr.step_name, sr.step_type,
			       sr.device_id::text, d.name,
			       sr.passed, sr.expected, sr.actual, sr.error,
			       sr.started_at, sr.completed_at
			  FROM nightly_step_result sr
			  LEFT JOIN devices d ON d.id = sr.device_id
			 WHERE sr.run_id = $1
			 ORDER BY sr.step_index`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s publicReadinessStep
			var expected, actual []byte
			if err := rows.Scan(&s.Index, &s.Name, &s.Type, &s.DeviceID, &s.Device,
				&s.Passed, &expected, &actual, &s.Error, &s.StartedAt, &s.CompletedAt); err != nil {
				return err
			}
			s.Expected = rawOrNil(expected)
			s.Actual = rawOrNil(actual)
			out.Steps = append(out.Steps, s)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// publicReadinessRoom is a room's Room Readiness set-up and its most
// recent run — "is every room ready this morning?" in one call.
type publicReadinessRoom struct {
	RoomID        string              `json:"room_id"`
	Room          string              `json:"room"`
	BuildingID    string              `json:"building_id"`
	Building      string              `json:"building"`
	RoutineID     *string             `json:"routine_id"`
	Routine       *string             `json:"routine"`
	ExcludedUntil *string             `json:"excluded_until"`
	LatestRun     *publicReadinessRun `json:"latest_run"`
}

// ListReadinessRooms — GET /pub/v1/room-readiness/rooms
//
// Every room with the routine it runs (its own, or the customer default)
// and its latest run. Flat list, like /pub/v1/rooms.
func (h *Handler) ListReadinessRooms(w http.ResponseWriter, r *http.Request) {
	out := []publicReadinessRoom{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.name, b.id::text, b.name,
			       tr.id::text, tr.name,
			       to_char(rnc.excluded_until, 'YYYY-MM-DD'),
			       latest.id::text
			  FROM rooms r
			  JOIN buildings b                  ON b.id = r.building_id
			  LEFT JOIN room_nightly_config rnc ON rnc.room_id = r.id
			  LEFT JOIN nightly_schedule ns     ON ns.customer_id = r.customer_id
			  LEFT JOIN nightly_test_routine tr ON tr.id = COALESCE(rnc.test_routine_id, ns.test_routine_id)
			  LEFT JOIN LATERAL (
			        SELECT id FROM nightly_run
			         WHERE room_id = r.id
			         ORDER BY scheduled_at DESC, id DESC LIMIT 1
			  ) latest ON true
			 ORDER BY b.name, r.name`)
		if err != nil {
			return err
		}
		var latestIDs []string
		at := map[string]int{}
		for rows.Next() {
			var room publicReadinessRoom
			var latest *string
			if err := rows.Scan(&room.RoomID, &room.Room, &room.BuildingID, &room.Building,
				&room.RoutineID, &room.Routine, &room.ExcludedUntil, &latest); err != nil {
				rows.Close()
				return err
			}
			if latest != nil {
				at[*latest] = len(out)
				latestIDs = append(latestIDs, *latest)
			}
			out = append(out, room)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(latestIDs) == 0 {
			return nil
		}
		runs, err := tx.Query(ctx, publicRunColumns+" WHERE nr.id = ANY($1::uuid[])", latestIDs)
		if err != nil {
			return err
		}
		defer runs.Close()
		for runs.Next() {
			run, err := scanPublicRun(runs)
			if err != nil {
				return err
			}
			out[at[run.ID]].LatestRun = &run
		}
		return runs.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func rawOrNil(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return json.RawMessage(b)
}
