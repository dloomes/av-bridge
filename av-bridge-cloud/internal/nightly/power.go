package nightly

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/commands"
)

// powerSettleTimeout bounds how long a run waits for its scheduled power
// commands to finish. Past the command queue's pending expiry (10 minutes
// by default, after which an undelivered command fails as 'expired'), so
// normally every command is terminal well before this; it only guards
// against a command stuck in progress.
const powerSettleTimeout = 12 * time.Minute

// Step indexes for the scheduled power commands. Negative so they sort
// before the routine's own steps (0, 1, ...) in the run's step results.
const (
	powerOffStepIndex = -2
	powerOnStepIndex  = -1
)

// powerCapabilityKey maps a power action to its key in the capabilities
// the Collector reports: {"power": {"on": true, "off": true}, ...}.
func powerCapabilityKey(action string) string {
	if action == "power_on" {
		return "on"
	}
	return "off"
}

// powerCapableIn narrows devices to those whose adapter declares the power
// action. Devices without it (PDUs, ping probes, a display without
// wake-on-LAN) would only fail the command, so they're left out.
func powerCapableIn(ctx context.Context, pool *pgxpool.Pool, devices []resolvedDevice, action string) ([]resolvedDevice, error) {
	if len(devices) == 0 {
		return devices, nil
	}
	ids := make([]string, len(devices))
	for i, d := range devices {
		ids[i] = d.id
	}
	rows, err := pool.Query(ctx, `
		SELECT id::text FROM devices
		 WHERE id = ANY($1::uuid[])
		   AND COALESCE((capabilities->'power'->>$2)::boolean, false)`,
		ids, powerCapabilityKey(action))
	if err != nil {
		return nil, err
	}
	capable := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		capable[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := devices[:0:0]
	for _, d := range devices {
		if capable[d.id] {
			out = append(out, d)
		}
	}
	return out, nil
}

// scheduleSubmitter tags the scheduled power commands of one run, so they
// can be told apart from the routine's own commands (tagged "nightly:<run>").
func scheduleSubmitter(runID string) string { return "nightly-schedule:" + runID }

// submitRoomPower queues the power action for every power-capable device in
// the room, in one transaction, and returns how many were queued. The
// commands reach each Collector through the usual queue and NOTIFY.
func submitRoomPower(ctx context.Context, pool *pgxpool.Pool, runID, customerID, roomID, action string) (int, error) {
	all, err := queryDevices(ctx, pool, roomID, ``, nil)
	if err != nil {
		return 0, err
	}
	devices, err := powerCapableIn(ctx, pool, all, action)
	if err != nil || len(devices) == 0 {
		return 0, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, d := range devices {
			if _, err := commands.Submit(ctx, tx, customerID, d.id, action, nil, scheduleSubmitter(runID)); err != nil {
				return fmt.Errorf("%s: %w", d.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(devices), nil
}

// powerOutcome reports whether a run's scheduled power commands have all
// finished, and how they went. done is false while any is still pending or
// in progress and the wait hasn't passed powerSettleTimeout; once it has,
// unfinished commands count as failed. The wait is measured on the
// database clock, which also stamped when the phase began.
func powerOutcome(ctx context.Context, pool *pgxpool.Pool, runID, action string) (done bool, failures []string, actual map[string]any, err error) {
	var timedOut bool
	if err := pool.QueryRow(ctx, `
		SELECT now() - COALESCE(phase_changed_at, started_at) >= make_interval(secs => $2)
		  FROM nightly_run WHERE id = $1`, runID, powerSettleTimeout.Seconds()).Scan(&timedOut); err != nil {
		return false, nil, nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(d.name, d.reported_id, ''), c.status, COALESCE(c.error, '')
		  FROM commands c
		  JOIN devices d ON d.id = c.device_id
		 WHERE c.submitted_by = $1 AND c.name = $2
		 ORDER BY d.name`, scheduleSubmitter(runID), action)
	if err != nil {
		return false, nil, nil, err
	}
	defer rows.Close()
	perDevice := map[string]string{}
	for rows.Next() {
		var name, status, cmdErr string
		if err := rows.Scan(&name, &status, &cmdErr); err != nil {
			return false, nil, nil, err
		}
		perDevice[name] = status
		switch commands.Status(status) {
		case commands.StatusSucceeded:
		case commands.StatusPending, commands.StatusInProgress:
			if !timedOut {
				return false, nil, nil, nil
			}
			failures = append(failures, name+": no response from the Collector")
		default:
			if cmdErr == "" {
				cmdErr = status
			}
			failures = append(failures, name+": "+cmdErr)
		}
	}
	if err := rows.Err(); err != nil {
		return false, nil, nil, err
	}
	return true, failures, map[string]any{"devices": len(perDevice), "per_device": perDevice}, nil
}

// recordPowerStep stores the scheduled power command's outcome as a step of
// the run, so the run detail, CSV export and API show which devices did or
// didn't switch.
func recordPowerStep(ctx context.Context, pool *pgxpool.Pool, runID, customerID, action string, failures []string, actual map[string]any, started time.Time) error {
	idx, name := powerOffStepIndex, "Scheduled power off"
	if action == "power_on" {
		idx, name = powerOnStepIndex, "Scheduled power on"
	}
	expected, _ := json.Marshal(map[string]any{"action": action})
	act, _ := json.Marshal(actual)
	var errArg any
	if len(failures) > 0 {
		errArg = strings.Join(failures, "; ")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO nightly_step_result
		  (customer_id, run_id, step_index, step_name, step_type, expected, actual, passed, error, started_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
		customerID, runID, idx, name, action, expected, act, len(failures) == 0, errArg, started)
	return err
}
