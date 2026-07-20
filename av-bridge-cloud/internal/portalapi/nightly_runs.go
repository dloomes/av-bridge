package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Nightly Room Readiness — run history read model.
//
// Slice 4. Two endpoints:
//   GET /api/v1/nightly/runs        list, filterable by date + room + status
//   GET /api/v1/nightly/runs/{id}   detail with step results
//
// Both operate under the caller's tenant + building_scope via the existing
// RLS policies (tenant_isolation + building_scope_nightly_run in
// migration 0023). Vendor callers get an unscoped view within their acting
// customer, matching every other portalapi read.

// nightlyRunRow is the wire shape for the list endpoint. Duration is
// computed server-side so the portal doesn't need to know that a NULL
// completed_at means "still in progress".
type nightlyRunRow struct {
	ID              string  `json:"id"`
	RoomID          string  `json:"room_id"`
	RoomName        string  `json:"room_name"`
	BuildingID      string  `json:"building_id"`
	BuildingName    string  `json:"building_name"`
	LocationName    string  `json:"location_name,omitempty"`
	RegionName      string  `json:"region_name,omitempty"`
	RecipeID        string  `json:"recipe_id,omitempty"`
	RecipeName      string  `json:"recipe_name,omitempty"`
	Phase           string  `json:"phase"`
	Status          string  `json:"status"`
	ScheduledAt     string  `json:"scheduled_at"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	DurationSeconds *int    `json:"duration_seconds,omitempty"`
	FailureReason   string  `json:"failure_reason,omitempty"`
}

// ListNightlyRuns — GET /api/v1/nightly/runs
//
// Query params:
//
//	from        RFC 3339 timestamp (inclusive). Defaults to 30 days ago.
//	to          RFC 3339 timestamp (exclusive). Defaults to now + 1 day
//	            so still-in-progress runs at midnight boundaries show.
//	room_id     uuid; if set, only that room.
//	status      csv of pending|in_progress|succeeded|failed|skipped.
//	limit       default 200, cap 1000. Runs list is a heatmap feed,
//	            not paginated for MVP — big estates + long windows will
//	            get truncated at the limit rather than 502ing.
func (h *Handler) ListNightlyRuns(w http.ResponseWriter, r *http.Request) {
	// Sensible defaults keep the endpoint useful on a bare curl too.
	q := r.URL.Query()
	from := time.Now().Add(-30 * 24 * time.Hour)
	to := time.Now().Add(24 * time.Hour)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "from must be an RFC 3339 timestamp")
			return
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "to must be an RFC 3339 timestamp")
			return
		}
		to = t
	}
	roomFilter := q.Get("room_id")
	statusFilter := parseStatusFilter(q.Get("status"))

	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > 1000 {
			n = 1000
		}
		limit = n
	}

	out := []nightlyRunRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Dynamic-arg SQL — the number of parameters depends on which
		// filters are present. Order: from, to, then optional room, then
		// optional status array, then limit.
		args := []any{from, to}
		sql := `
			SELECT nr.id::text,
			       nr.room_id::text, r.name AS room_name,
			       b.id::text, b.name AS building_name,
			       loc.name AS location_name,
			       reg.name AS region_name,
			       nr.recipe_id::text, tr.name AS recipe_name,
			       nr.phase, nr.status,
			       nr.scheduled_at, nr.started_at, nr.completed_at,
			       nr.failure_reason
			  FROM nightly_run nr
			  JOIN rooms r                       ON r.id = nr.room_id
			  LEFT JOIN buildings b              ON b.id = r.building_id
			  LEFT JOIN locations loc            ON loc.id = b.location_id
			  LEFT JOIN regions   reg            ON reg.id = loc.region_id
			  LEFT JOIN nightly_test_recipe tr   ON tr.id = nr.recipe_id
			 WHERE nr.scheduled_at >= $1
			   AND nr.scheduled_at < $2`
		if roomFilter != "" {
			args = append(args, roomFilter)
			sql += " AND nr.room_id = $3"
		}
		if len(statusFilter) > 0 {
			args = append(args, statusFilter)
			// Placeholder index depends on whether roomFilter was set.
			sql += " AND nr.status = ANY($" + strconv.Itoa(len(args)) + "::text[])"
		}
		args = append(args, limit)
		sql += " ORDER BY nr.scheduled_at DESC LIMIT $" + strconv.Itoa(len(args))

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				row                          nightlyRunRow
				locName, regName, recipeName *string
				recipeID                     *string
				sched                        time.Time
				started, completed           *time.Time
				failure                      *string
			)
			if err := rows.Scan(
				&row.ID,
				&row.RoomID, &row.RoomName,
				&row.BuildingID, &row.BuildingName,
				&locName, &regName,
				&recipeID, &recipeName,
				&row.Phase, &row.Status,
				&sched, &started, &completed,
				&failure,
			); err != nil {
				return err
			}
			if locName != nil {
				row.LocationName = *locName
			}
			if regName != nil {
				row.RegionName = *regName
			}
			if recipeID != nil {
				row.RecipeID = *recipeID
			}
			if recipeName != nil {
				row.RecipeName = *recipeName
			}
			row.ScheduledAt = sched.UTC().Format(time.RFC3339)
			if started != nil {
				row.StartedAt = started.UTC().Format(time.RFC3339)
			}
			if completed != nil {
				row.CompletedAt = completed.UTC().Format(time.RFC3339)
				if started != nil {
					secs := int(completed.Sub(*started).Seconds())
					row.DurationSeconds = &secs
				}
			}
			if failure != nil {
				row.FailureReason = *failure
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

// nightlyStepRow is one entry in the run detail's steps array.
type nightlyStepRow struct {
	StepIndex   int             `json:"step_index"`
	StepName    string          `json:"step_name"`
	StepType    string          `json:"step_type"`
	DeviceID    string          `json:"device_id,omitempty"`
	DeviceName  string          `json:"device_name,omitempty"`
	Expected    json.RawMessage `json:"expected,omitempty"`
	Actual      json.RawMessage `json:"actual,omitempty"`
	Passed      bool            `json:"passed"`
	Error       string          `json:"error,omitempty"`
	StartedAt   string          `json:"started_at,omitempty"`
	CompletedAt string          `json:"completed_at,omitempty"`
}

type nightlyRunDetail struct {
	nightlyRunRow
	Steps []nightlyStepRow `json:"steps"`
}

// GetNightlyRun — GET /api/v1/nightly/runs/{id}
func (h *Handler) GetNightlyRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeErr(w, http.StatusBadRequest, "run id required")
		return
	}

	var out nightlyRunDetail
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Base row.
		var (
			locName, regName, recipeName *string
			recipeID                     *string
			sched                        time.Time
			started, completed           *time.Time
			failure                      *string
		)
		err := tx.QueryRow(ctx, `
			SELECT nr.id::text,
			       nr.room_id::text, r.name,
			       b.id::text, b.name,
			       loc.name, reg.name,
			       nr.recipe_id::text, tr.name,
			       nr.phase, nr.status,
			       nr.scheduled_at, nr.started_at, nr.completed_at,
			       nr.failure_reason
			  FROM nightly_run nr
			  JOIN rooms r                       ON r.id = nr.room_id
			  LEFT JOIN buildings b              ON b.id = r.building_id
			  LEFT JOIN locations loc            ON loc.id = b.location_id
			  LEFT JOIN regions   reg            ON reg.id = loc.region_id
			  LEFT JOIN nightly_test_recipe tr   ON tr.id = nr.recipe_id
			 WHERE nr.id = $1
		`, runID).Scan(
			&out.ID,
			&out.RoomID, &out.RoomName,
			&out.BuildingID, &out.BuildingName,
			&locName, &regName,
			&recipeID, &recipeName,
			&out.Phase, &out.Status,
			&sched, &started, &completed,
			&failure,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if locName != nil {
			out.LocationName = *locName
		}
		if regName != nil {
			out.RegionName = *regName
		}
		if recipeID != nil {
			out.RecipeID = *recipeID
		}
		if recipeName != nil {
			out.RecipeName = *recipeName
		}
		out.ScheduledAt = sched.UTC().Format(time.RFC3339)
		if started != nil {
			out.StartedAt = started.UTC().Format(time.RFC3339)
		}
		if completed != nil {
			out.CompletedAt = completed.UTC().Format(time.RFC3339)
			if started != nil {
				secs := int(completed.Sub(*started).Seconds())
				out.DurationSeconds = &secs
			}
		}
		if failure != nil {
			out.FailureReason = *failure
		}

		// Step results — empty for slice 3 (dry-run doesn't populate
		// them), non-empty once Phase B lands.
		out.Steps = []nightlyStepRow{}
		stepRows, err := tx.Query(ctx, `
			SELECT sr.step_index, sr.step_name, sr.step_type,
			       sr.device_id::text, d.name,
			       sr.expected, sr.actual, sr.passed, sr.error,
			       sr.started_at, sr.completed_at
			  FROM nightly_step_result sr
			  LEFT JOIN devices d ON d.id = sr.device_id
			 WHERE sr.run_id = $1
			 ORDER BY sr.step_index
		`, runID)
		if err != nil {
			return err
		}
		defer stepRows.Close()
		for stepRows.Next() {
			var (
				step         nightlyStepRow
				deviceID     *string
				deviceName   *string
				expected     []byte
				actual       []byte
				errStr       *string
				stepStarted  *time.Time
				stepDone     *time.Time
			)
			if err := stepRows.Scan(
				&step.StepIndex, &step.StepName, &step.StepType,
				&deviceID, &deviceName,
				&expected, &actual, &step.Passed, &errStr,
				&stepStarted, &stepDone,
			); err != nil {
				return err
			}
			if deviceID != nil {
				step.DeviceID = *deviceID
			}
			if deviceName != nil {
				step.DeviceName = *deviceName
			}
			if len(expected) > 0 {
				step.Expected = json.RawMessage(expected)
			}
			if len(actual) > 0 {
				step.Actual = json.RawMessage(actual)
			}
			if errStr != nil {
				step.Error = *errStr
			}
			if stepStarted != nil {
				step.StartedAt = stepStarted.UTC().Format(time.RFC3339)
			}
			if stepDone != nil {
				step.CompletedAt = stepDone.UTC().Format(time.RFC3339)
			}
			out.Steps = append(out.Steps, step)
		}
		return stepRows.Err()
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

// parseStatusFilter turns "succeeded,failed" into ["succeeded","failed"],
// dropping anything not in the known set so bad input can't smuggle an
// unwanted SQL predicate through.
func parseStatusFilter(csv string) []string {
	if csv == "" {
		return nil
	}
	allowed := map[string]bool{
		"pending":     true,
		"in_progress": true,
		"succeeded":   true,
		"failed":      true,
		"skipped":     true,
	}
	var out []string
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			part := csv[start:i]
			if allowed[part] {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}
