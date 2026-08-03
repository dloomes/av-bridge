package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/nightly"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ad-hoc routine trigger — "Run now".
//
// POST /api/v1/nightly/rooms/{room_id}/run-now
//
// Creates a nightly_run for the given room bypassing the schedule: skips
// the power-cycle preamble (no scheduled_off / warming) and lands
// directly in the `testing` phase so the executor picks it up and runs
// the routine immediately. The routine is either the explicit
// routine_id in the request body (useful when testing a new routine),
// or the effective routine for the room (per-room override → customer
// default). Returns { run_id } so the portal can navigate to the run
// detail page and watch the results appear live.
//
// Gated on nightly.manage — this dispatches real device commands once
// command-dispatch is wired, so it's a permissioned action.

type runNowRequest struct {
	// Optional override — when set, run this routine instead of the
	// effective one. Handy for "test a new routine against a specific
	// room" from the routine editor.
	RoutineID string `json:"routine_id,omitempty"`
}

type runNowResponse struct {
	RunID     string `json:"run_id"`
	RoutineID string `json:"routine_id"`
}

// SendRoutineRunNow — POST /api/v1/nightly/rooms/{id}/run-now
func (h *Handler) SendRoutineRunNow(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		writeErr(w, http.StatusBadRequest, "room id required")
		return
	}
	if h.executor == nil {
		writeErr(w, http.StatusServiceUnavailable, "nightly executor not configured on this deployment")
		return
	}

	var req runNowRequest
	// Body is optional — a missing / empty body means "use the effective
	// routine". We still decode when present so a routine_id override
	// takes precedence.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	p, _ := portalauth.From(r.Context())

	var (
		newRunID   string
		routineID  string
		notFound   bool
		noRoutine  bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Confirm the room exists in the caller's tenant. RLS already
		// enforces this, but the explicit lookup gives us a clean 404.
		var roomExists bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM rooms WHERE id = $1`, roomID,
		).Scan(&roomExists); errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		} else if err != nil {
			return err
		}

		// Resolve the effective routine. Priority:
		//   1. Explicit routine_id in the request body
		//   2. Per-room override's test_routine_id
		//   3. Customer default's test_routine_id
		// A room with no routine at any tier → 400 (no point running).
		if req.RoutineID != "" {
			// Sanity-check the routine exists inside the caller's
			// tenant (RLS enforces this via nightly_test_routine's
			// tenant policy).
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT true FROM nightly_test_routine WHERE id = $1`, req.RoutineID,
			).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
				noRoutine = true
				return nil
			} else if err != nil {
				return err
			}
			routineID = req.RoutineID
		} else {
			var resolved *string
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(rnc.test_routine_id, ns.test_routine_id)::text
				  FROM nightly_schedule ns
				  LEFT JOIN room_nightly_config rnc ON rnc.room_id = $1
				 WHERE ns.customer_id = current_setting('app.current_customer', true)::uuid
			`, roomID).Scan(&resolved); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					noRoutine = true
					return nil
				}
				return err
			}
			if resolved == nil || *resolved == "" {
				noRoutine = true
				return nil
			}
			routineID = *resolved
		}

		// Insert the run directly in the `testing` phase. Bypass the
		// (room_id, scheduled_at) unique index by using now() as the
		// scheduled_at — sub-second collisions are only possible if a
		// user double-clicks within microseconds, which the unique
		// constraint would reject with a clear error.
		if err := tx.QueryRow(ctx, `
			INSERT INTO nightly_run (
			  customer_id, room_id, routine_id,
			  scheduled_at, started_at, phase, status
			) VALUES (
			  current_setting('app.current_customer', true)::uuid,
			  $1, $2, now(), now(), 'testing', 'in_progress'
			)
			RETURNING id::text
		`, roomID, routineID).Scan(&newRunID); err != nil {
			return err
		}

		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.routine.run-now",
			TargetKind: "nightly_run", TargetID: newRunID,
			After: mustJSON(map[string]any{
				"room_id":    roomID,
				"routine_id": routineID,
				"trigger":    "manual",
			}),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	if noRoutine {
		writeErr(w, http.StatusBadRequest, "no routine assigned to this room and no routine_id in request")
		return
	}

	// Hand the freshly-inserted run to the executor. This is safe to
	// call in a fire-and-forget style — the executor spawns its own
	// goroutine on a shutdown-aware context and returns quickly. The
	// portal will see step_results appear via the run-detail poll.
	//
	// We look up the room name for the log line (nice-to-have) but the
	// executor re-queries whatever else it needs from the run.
	roomName := lookupRoomName(r.Context(), h.store.AdminPool(), roomID)
	took, err := h.executor.MaybeStart(r.Context(), nightly.RunContext{
		RunID:      newRunID,
		CustomerID: p.CustomerID,
		RoomID:     roomID,
		RoomName:   roomName,
		RoutineID:  &routineID,
	})
	if err != nil {
		h.log.Warn("nightly run-now: executor start failed",
			"run", newRunID, "error", err)
		// The run row exists but the executor rejected it. Mark it
		// failed so it doesn't sit in `testing` forever; SweepStuck
		// would catch it on next boot anyway but earlier is kinder.
		markRunFailed(r.Context(), h.store.AdminPool(), newRunID, "executor start failed: "+err.Error())
		writeErr(w, http.StatusInternalServerError, "failed to start routine")
		return
	}
	if !took {
		// Executor declined (disabled, or the run isn't eligible for
		// some reason). Treat as unavailable rather than silent
		// success.
		h.log.Warn("nightly run-now: executor declined to start",
			"run", newRunID, "routine", routineID)
		markRunFailed(r.Context(), h.store.AdminPool(), newRunID, "executor declined to start")
		writeErr(w, http.StatusServiceUnavailable, "executor not enabled or declined to start")
		return
	}

	writeJSON(w, http.StatusOK, runNowResponse{
		RunID:     newRunID,
		RoutineID: routineID,
	})
}

// lookupRoomName is a best-effort convenience for logging. Returns ""
// on any error — the executor doesn't need the name to function.
func lookupRoomName(ctx context.Context, pool *pgxpool.Pool, roomID string) string {
	var name string
	_ = pool.QueryRow(ctx, `SELECT name FROM rooms WHERE id = $1`, roomID).Scan(&name)
	return name
}

// markRunFailed writes a failed status on a run created by run-now
// when the executor handoff didn't succeed. Uses the admin pool so
// it works outside any tenant scope. Best-effort; errors logged by
// the caller already committed to an HTTP response.
func markRunFailed(ctx context.Context, pool *pgxpool.Pool, runID, reason string) {
	_, _ = pool.Exec(ctx, `
		UPDATE nightly_run
		   SET phase = 'failed', status = 'failed',
		       completed_at = now(), failure_reason = $2
		 WHERE id = $1
	`, runID, reason)
}
