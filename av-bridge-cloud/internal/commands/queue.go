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

	"github.com/jackc/pgx/v5"
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
	return id, err
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
