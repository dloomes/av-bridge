// Package commands is the cloud-side queue for portal-issued device commands.
// Portal POSTs → row goes pending → bridge claims via outbound poll → bridge
// posts result → row reaches a terminal status. The portal's submit handler
// waits up to a timeout for the row to become terminal so it looks synchronous
// from the portal's side, with a 202 + command_id escape hatch for slower runs.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

// Postgres LISTEN/NOTIFY channels used to wake long-polling waiters instead
// of polling the DB on a tick. Both are pub/sub broadcast channels so any
// listening cloud task wakes; SKIP LOCKED on the follow-up claim ensures
// only one bridge poll wins any given row.
//
//	ChannelPending — payload = collector_id. Emitted from Submit and from
//	                 the sweeper's requeue step.
//	ChannelDone    — payload = command_id. Emitted from Complete.
const (
	ChannelPending = "cmd_pending"
	ChannelDone    = "cmd_done"
)

// Status values mirror the CHECK constraint in 0003_commands.sql.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Command is the row, with the joined reported_id the bridge needs to dispatch.
type Command struct {
	ID         string          `json:"id"`
	DeviceID   string          `json:"device_id"`
	ReportedID string          `json:"reported_id"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args,omitempty"`
	Status     Status          `json:"status"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Submitted  time.Time       `json:"submitted_at"`
	Claimed    *time.Time      `json:"claimed_at,omitempty"`
	Completed  *time.Time      `json:"completed_at,omitempty"`
}

// Submit inserts a pending command. Caller must already be in a WithTenant tx.
// The submitter_role goes into the audit-friendly submitted_by column.
func Submit(ctx context.Context, tx pgx.Tx, customerID, deviceID, name string, args []byte, submittedBy string) (string, error) {
	var (
		id          string
		collectorID string
	)
	err := tx.QueryRow(ctx,
		`SELECT collector_id::text FROM devices WHERE id = $1 AND deleted_at IS NULL`, deviceID).Scan(&collectorID)
	if err != nil {
		return "", err
	}

	var argsParam any = nil
	if len(args) > 0 {
		argsParam = string(args)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO commands (customer_id, collector_id, device_id, name, args, submitted_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		RETURNING id::text`,
		customerID, collectorID, deviceID, name, argsParam, submittedBy,
	).Scan(&id)
	if err != nil {
		return "", err
	}
	// NOTIFY inside the same tx — Postgres holds the notify until COMMIT,
	// so a listener only wakes for rows a subsequent SELECT can actually
	// see. pg_notify() takes params (raw NOTIFY doesn't). Failure here is
	// non-fatal: the row is committed either way and the sweeper +
	// fallback poll pace would still deliver the command; but we surface
	// the error so a mis-configured pool doesn't silently degrade.
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, ChannelPending, collectorID); err != nil {
		return "", fmt.Errorf("notify %s: %w", ChannelPending, err)
	}
	return id, nil
}

// Get returns one command by id (tenant-scoped via RLS). Returns pgx.ErrNoRows
// if absent or not visible to the caller's tenant.
func Get(ctx context.Context, tx pgx.Tx, id string) (Command, error) {
	var c Command
	err := tx.QueryRow(ctx, `
		SELECT c.id::text, c.device_id::text, COALESCE(d.reported_id,''),
		       c.name, c.args, c.status, c.result, COALESCE(c.error,''),
		       c.submitted_at, c.claimed_at, c.completed_at
		  FROM commands c
		  JOIN devices d ON d.id = c.device_id AND d.deleted_at IS NULL
		 WHERE c.id = $1`, id).
		Scan(&c.ID, &c.DeviceID, &c.ReportedID, &c.Name, &c.Args, &c.Status, &c.Result,
			&c.Error, &c.Submitted, &c.Claimed, &c.Completed)
	return c, err
}

// WaitForTerminal calls get on a tight cadence until the returned command's
// status is terminal or the deadline passes. The closure form keeps us from
// holding a DB transaction across the wait — each poll opens a fresh short tx.
// On timeout, the last-seen command is returned so the caller can respond 202.
//
// Slice 3 lives without LISTEN/NOTIFY; revisit if load justifies it.
func WaitForTerminal(ctx context.Context, get func(context.Context) (Command, error), timeout time.Duration) (Command, error) {
	deadline := time.Now().Add(timeout)
	var last Command
	for {
		c, err := get(ctx)
		if err != nil {
			return c, err
		}
		last = c
		if c.Status.Terminal() {
			return c, nil
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// WaitForTerminalNotified is the LISTEN/NOTIFY version of WaitForTerminal.
// The caller opens a Listener on ChannelDone BEFORE the initial get() — that
// order closes the missed-signal window between the first DB check and the
// wait. Behaviour on timeout matches WaitForTerminal: return the last-seen
// command (not an error) so callers can respond 202.
//
// Payload filter: cmd_done carries the completed command's id; a payload
// that doesn't match commandID is another tenant's completion and we loop
// back to waiting. A single Listener could theoretically be shared across
// many waiters if we wanted to (one conn per cloud task instead of one
// per request), but the code is simpler with one Listener per waiter and
// the pool budget covers it at current scale.
func WaitForTerminalNotified(ctx context.Context, l *db.Listener, get func(context.Context) (Command, error), commandID string, timeout time.Duration) (Command, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Initial check — after LISTEN is already open, so a NOTIFY that
	// fires between here and Wait() is buffered by pgx and returned by
	// the next Wait call.
	c, err := get(waitCtx)
	if err != nil {
		return c, err
	}
	if c.Status.Terminal() {
		return c, nil
	}

	for {
		payload, werr := l.Wait(waitCtx)
		if werr != nil {
			// Distinguish our internal timeout from an outer cancel or a
			// listener-side failure. On internal timeout return the last
			// seen state (no error) so the caller responds 202.
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return c, nil
			}
			if ctx.Err() != nil {
				return c, ctx.Err()
			}
			return c, werr
		}
		if payload != commandID {
			// Someone else's completion — keep waiting.
			continue
		}
		// Our command finished; re-fetch to pick up the result blob.
		c2, err := get(ctx)
		if err != nil {
			return c2, err
		}
		return c2, nil
	}
}

// WaitForPending is the bridge-side counterpart: blocks up to maxHold for
// a cmd_pending NOTIFY whose payload matches collectorID. Returns nil when
// either a matching NOTIFY arrives or the internal deadline passes — the
// caller then re-runs ClaimPending. A non-matching payload (another
// collector's row) is ignored and we keep waiting. The listener MUST have
// been opened BEFORE the caller's most-recent ClaimPending so no signal
// is missed between "empty claim" and "start waiting".
func WaitForPending(ctx context.Context, l *db.Listener, collectorID string, maxHold time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, maxHold)
	defer cancel()
	for {
		payload, err := l.Wait(waitCtx)
		if err != nil {
			// Internal timeout is expected and non-error; outer ctx cancel
			// is the caller's problem to surface.
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if payload == collectorID {
			return nil
		}
	}
}

// ClaimPending atomically marks up to max commands as in_progress for the given
// collector and returns them with reported_id joined in. Uses SKIP LOCKED so
// concurrent pollers don't fight. Caller is in a WithTenant tx for the
// collector's customer.
func ClaimPending(ctx context.Context, tx pgx.Tx, collectorID string, max int) ([]Command, error) {
	if max <= 0 {
		return nil, nil
	}
	// claim_count tracks re-issues across sweeper requeues so a flapping bridge
	// can't trap a command in an infinite retry loop (see Sweeper).
	rows, err := tx.Query(ctx, `
		WITH claimed AS (
		  UPDATE commands
		     SET status='in_progress',
		         claimed_at=now(),
		         claim_count = claim_count + 1
		   WHERE id IN (
		     SELECT id FROM commands
		      WHERE collector_id = $1 AND status = 'pending'
		      ORDER BY submitted_at
		      LIMIT $2 FOR UPDATE SKIP LOCKED
		   )
		  RETURNING id, device_id, name, args, claimed_at, submitted_at
		)
		SELECT c.id::text, c.device_id::text, COALESCE(d.reported_id,''),
		       c.name, c.args, c.claimed_at, c.submitted_at
		  FROM claimed c JOIN devices d ON d.id = c.device_id AND d.deleted_at IS NULL
		 ORDER BY c.submitted_at`,
		collectorID, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		c := Command{Status: StatusInProgress}
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.ReportedID, &c.Name, &c.Args, &c.Claimed, &c.Submitted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Complete writes the terminal status + result/error for a command. The caller
// must be the same collector that claimed it (we verify collector_id in the
// UPDATE WHERE clause so a misrouted result can't write to another collector's
// command).
//
// result and errStr are exclusive: pass result on success, errStr on failure.
func Complete(ctx context.Context, tx pgx.Tx, commandID, collectorID string, result []byte, errStr string) error {
	final := StatusSucceeded
	if errStr != "" {
		final = StatusFailed
	}
	var resultParam any = nil
	if len(result) > 0 {
		resultParam = string(result)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE commands SET status = $3, result = $4::jsonb, error = NULLIF($5,''), completed_at = now()
		 WHERE id = $1 AND collector_id = $2 AND status = 'in_progress'`,
		commandID, collectorID, string(final), resultParam, errStr)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("command not in_progress for this collector (already completed, stolen, or not yours)")
	}
	// Wake any submitAndWait / nightly executor blocked on this command.
	// Same tx as the UPDATE so the notification only fires after commit.
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, ChannelDone, commandID); err != nil {
		return fmt.Errorf("notify %s: %w", ChannelDone, err)
	}
	return nil
}

// ErrInvalidStatus is returned when a status arrives that isn't in the CHECK list.
var ErrInvalidStatus = errors.New("invalid status")

// FormatStatus is a defensive helper for the rare case a row scan needs validation.
func FormatStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusPending, StatusInProgress, StatusSucceeded, StatusFailed, StatusCancelled:
		return Status(s), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
	}
}
