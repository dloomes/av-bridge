package portalapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// nightlyRunsExportMaxRows caps one export. A big estate over a long
// window is still well under this (1,000 rooms × 30 nights × a dozen
// steps); past it the operator should narrow the dates.
const nightlyRunsExportMaxRows = 500_000

// nightlyRunsExportColumns — one row per step result (spec §11), with the
// run's fields repeated so the file pivots without a join. A run with no
// step results (power-cycle only, or skipped) still gets one row with the
// step columns empty, so every run is in the file.
var nightlyRunsExportColumns = []string{
	"scheduled_at", "region", "location", "building", "room", "routine",
	"run_status", "run_phase", "failure_reason",
	"run_started_at", "run_completed_at", "run_duration_seconds",
	"step_index", "step_name", "step_type", "device",
	"step_passed", "expected", "actual", "step_error",
	"step_started_at", "step_completed_at",
}

// ExportNightlyRuns — GET /api/v1/nightly/runs/export.csv
//
// Same filters as ListNightlyRuns (from, to, room_id, status), no limit.
// Rows are gathered before anything is written so a query failure is a
// clean 500, not a truncated download.
func (h *Handler) ExportNightlyRuns(w http.ResponseWriter, r *http.Request) {
	f, msg := parseNightlyRunsFilter(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}

	var records [][]string
	tooMany := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{f.from, f.to}
		sql := `
			SELECT nr.scheduled_at,
			       COALESCE(reg.name, ''), COALESCE(loc.name, ''),
			       COALESCE(b.name, ''), r.name,
			       COALESCE(tr.name, ''),
			       nr.status, nr.phase, COALESCE(nr.failure_reason, ''),
			       nr.started_at, nr.completed_at,
			       sr.step_index, COALESCE(sr.step_name, ''), COALESCE(sr.step_type, ''),
			       COALESCE(d.name, ''),
			       sr.passed, sr.expected::text, sr.actual::text, COALESCE(sr.error, ''),
			       sr.started_at, sr.completed_at
			  FROM nightly_run nr
			  JOIN rooms r                        ON r.id = nr.room_id
			  LEFT JOIN buildings b               ON b.id = r.building_id
			  LEFT JOIN locations loc             ON loc.id = b.location_id
			  LEFT JOIN regions   reg             ON reg.id = loc.region_id
			  LEFT JOIN nightly_test_routine tr   ON tr.id = nr.routine_id
			  LEFT JOIN nightly_step_result sr    ON sr.run_id = nr.id
			  LEFT JOIN devices d                 ON d.id = sr.device_id
			 WHERE nr.scheduled_at >= $1
			   AND nr.scheduled_at < $2`
		if f.room != "" {
			args = append(args, f.room)
			sql += " AND nr.room_id = $" + strconv.Itoa(len(args))
		}
		if len(f.statuses) > 0 {
			args = append(args, f.statuses)
			sql += " AND nr.status = ANY($" + strconv.Itoa(len(args)) + "::text[])"
		}
		args = append(args, nightlyRunsExportMaxRows+1)
		sql += " ORDER BY nr.scheduled_at DESC, r.name, nr.id, sr.step_index LIMIT $" + strconv.Itoa(len(args))

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				sched                            time.Time
				region, location, building, room string
				routine, status, phase, failure  string
				started, completed               *time.Time
				stepIndex                        *int
				stepName, stepType, device       string
				passed                           *bool
				expected, actual                 *string
				stepErr                          string
				stepStarted, stepCompleted       *time.Time
			)
			if err := rows.Scan(&sched,
				&region, &location, &building, &room, &routine,
				&status, &phase, &failure,
				&started, &completed,
				&stepIndex, &stepName, &stepType, &device,
				&passed, &expected, &actual, &stepErr,
				&stepStarted, &stepCompleted,
			); err != nil {
				return err
			}
			if len(records) == nightlyRunsExportMaxRows {
				tooMany = true
				return nil
			}
			duration := ""
			if started != nil && completed != nil {
				duration = strconv.Itoa(int(completed.Sub(*started).Seconds()))
			}
			idx, pass := "", ""
			if stepIndex != nil {
				idx = strconv.Itoa(*stepIndex)
			}
			if passed != nil {
				pass = strconv.FormatBool(*passed)
			}
			records = append(records, []string{
				sched.UTC().Format(time.RFC3339),
				csvText(region), csvText(location), csvText(building), csvText(room), csvText(routine),
				status, phase, csvText(failure),
				csvTime(started), csvTime(completed), duration,
				idx, csvText(stepName), stepType, csvText(device),
				pass, csvText(deref(expected)), csvText(deref(actual)), csvText(stepErr),
				csvTime(stepStarted), csvTime(stepCompleted),
			})
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	if tooMany {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("more than %d rows; narrow the date range", nightlyRunsExportMaxRows))
		return
	}

	filename := fmt.Sprintf("room-readiness-runs-%s.csv", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write(nightlyRunsExportColumns)
	_ = cw.WriteAll(records) // flushes
}

// nightlyRunsFilter is the shared filter for the runs list and export.
type nightlyRunsFilter struct {
	from, to time.Time
	room     string
	statuses []string
}

// parseNightlyRunsFilter reads from / to / room_id / status. A non-empty
// message is a 400.
func parseNightlyRunsFilter(r *http.Request) (nightlyRunsFilter, string) {
	q := r.URL.Query()
	f := nightlyRunsFilter{
		from:     time.Now().Add(-30 * 24 * time.Hour),
		to:       time.Now().Add(24 * time.Hour),
		room:     q.Get("room_id"),
		statuses: parseStatusFilter(q.Get("status")),
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "from must be an RFC 3339 timestamp"
		}
		f.from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "to must be an RFC 3339 timestamp"
		}
		f.to = t
	}
	return f, ""
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// csvText stops a spreadsheet treating user-entered text (room, step and
// device names, error messages) as a formula.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
