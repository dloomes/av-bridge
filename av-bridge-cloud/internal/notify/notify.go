// Package notify dispatches new alerts to a customer's configured outbound
// channels (email / Teams / webhook). Triggered from the ingest path when an
// alert row is first inserted (re-fires of an already-open alert don't
// re-notify — operators see updates in-portal but their phones don't keep
// buzzing).
//
// Dispatch is fire-and-forget on a background goroutine so the ingest tx
// returns quickly. Per-channel send results are written back to the channel
// row (last_sent_at + last_error) so the portal can show "last delivery
// failed at X" without us building a separate outbox table for v1.
//
// Real send transports live in senders.go; this file owns the dispatcher
// lifecycle, channel lookup, and severity filtering.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertEvent is the minimal shape the dispatcher needs to format and send a
// notification. Constructed at open-time so we don't have to re-query.
//
// The subject of the alert is either a device or a collector — exactly one
// of DeviceID / CollectorID is populated. Renderers branch on which is set
// to pick the correct label and to include the right identifiers in the
// outbound webhook payload.
type AlertEvent struct {
	CustomerID    string
	DeviceID      string
	DeviceName    string
	CollectorID   string
	CollectorName string
	AlertKey      string
	Severity      string
	Message       string
	OpenedAt      time.Time
	Payload       map[string]any
}

// SubjectName returns whichever of DeviceName / CollectorName is set. Used
// by the senders so their subject-line construction doesn't have to branch.
func (a AlertEvent) SubjectName() string {
	if a.CollectorID != "" {
		return a.CollectorName
	}
	return a.DeviceName
}

// SubjectLabel returns "Collector" or "Device" depending on which subject
// the alert is about. Used to render "Device: X" / "Collector: X" lines.
func (a AlertEvent) SubjectLabel() string {
	if a.CollectorID != "" {
		return "Collector"
	}
	return "Device"
}

// Channel is the in-memory shape of a notification_channels row. Kept small
// so the dispatcher can iterate quickly without dragging the DB tags around.
type Channel struct {
	ID          string
	Type        string // email | teams | webhook
	Name        string
	Target      string
	Config      map[string]any
	MinSeverity string
}

// SenderRegistry maps channel type → send function. Senders own any
// transport-specific state (SMTP client, HTTP client, etc).
type SenderRegistry interface {
	Send(ctx context.Context, ch Channel, evt AlertEvent) error
}

// Dispatcher fans new alerts out to configured channels.
type Dispatcher struct {
	pool     *pgxpool.Pool
	senders  SenderRegistry
	log      *slog.Logger
	deadline time.Duration
}

func NewDispatcher(pool *pgxpool.Pool, senders SenderRegistry, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		pool:     pool,
		senders:  senders,
		log:      log,
		deadline: 10 * time.Second,
	}
}

// Dispatch is the public entry point — call once per newly-opened alert.
// Non-blocking: spawns a goroutine that does the channel lookup + sends.
// Errors are logged + persisted to the channel row; the caller never sees
// them (alerts don't need to fail just because Teams is offline).
func (d *Dispatcher) Dispatch(evt AlertEvent) {
	if d == nil {
		return
	}
	go d.run(evt)
}

func (d *Dispatcher) run(evt AlertEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), d.deadline)
	defer cancel()

	channels, err := d.listChannels(ctx, evt.CustomerID, evt.Severity)
	if err != nil {
		d.log.Error("notify: list channels failed",
			"customer", evt.CustomerID, "error", err)
		return
	}
	if len(channels) == 0 {
		return
	}

	for _, ch := range channels {
		err := d.senders.Send(ctx, ch, evt)
		d.recordResult(ctx, ch.ID, err)
		if err != nil {
			d.log.Warn("notify: send failed",
				"channel", ch.ID, "type", ch.Type, "error", err)
		} else {
			d.log.Info("notify: sent",
				"channel", ch.ID, "type", ch.Type,
				"customer", evt.CustomerID, "alert", evt.AlertKey)
		}
	}
}

// listChannels queries active channels for a customer that should receive
// the given severity. Runs as app_admin (BYPASSRLS) because the dispatcher
// is invoked from the ingest path, which has no tenant session set.
//
// Severity gate is inline CASE — info=1, warning=2, critical=3. A channel
// with min_severity='warning' receives warning + critical; 'info' channels
// receive everything; 'critical' channels only get critical.
func (d *Dispatcher) listChannels(ctx context.Context, customerID, severity string) ([]Channel, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id::text, type, name, target, config, min_severity
		  FROM notification_channels
		 WHERE customer_id = $1
		   AND enabled = true
		   AND CASE $2 WHEN 'critical' THEN 3 WHEN 'warning' THEN 2 ELSE 1 END
		    >= CASE min_severity WHEN 'critical' THEN 3 WHEN 'warning' THEN 2 ELSE 1 END`,
		customerID, severity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannels(rows)
}

func scanChannels(rows pgx.Rows) ([]Channel, error) {
	var out []Channel
	for rows.Next() {
		var c Channel
		var cfg []byte
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &c.Target, &cfg, &c.MinSeverity); err != nil {
			return nil, err
		}
		if len(cfg) > 0 {
			_ = json.Unmarshal(cfg, &c.Config)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SendToChannel dispatches a single event to one specific channel — used by
// the portal's "test send" button so an operator can validate a freshly-
// configured channel without waiting for an alert to fire. Honours the same
// recording semantics (last_sent_at + last_error) as the alert path.
//
// Synchronous (returns the send error) so the portal can show pass/fail
// inline. Caller must enforce that the channel belongs to the right tenant
// — this function takes the customer_id explicitly and the lookup query
// scopes by both id + customer_id.
func (d *Dispatcher) SendToChannel(ctx context.Context, customerID, channelID string, evt AlertEvent) error {
	var ch Channel
	var cfg []byte
	err := d.pool.QueryRow(ctx, `
		SELECT id::text, type, name, target, config, min_severity
		  FROM notification_channels
		 WHERE id = $1 AND customer_id = $2`,
		channelID, customerID).
		Scan(&ch.ID, &ch.Type, &ch.Name, &ch.Target, &cfg, &ch.MinSeverity)
	if err != nil {
		return err
	}
	if len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &ch.Config)
	}
	sendErr := d.senders.Send(ctx, ch, evt)
	d.recordResult(ctx, ch.ID, sendErr)
	return sendErr
}

func (d *Dispatcher) recordResult(ctx context.Context, channelID string, sendErr error) {
	var errMsg any
	if sendErr != nil {
		errMsg = truncate(sendErr.Error(), 1000)
	}
	if _, err := d.pool.Exec(ctx, `
		UPDATE notification_channels
		   SET last_sent_at = now(),
		       last_error   = $2
		 WHERE id = $1`,
		channelID, errMsg); err != nil {
		// Logging-only — failing to record the result shouldn't cascade.
		d.log.Warn("notify: record result failed", "channel", channelID, "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ErrUnsupportedChannel is returned by SenderRegistry when no transport is
// wired for the channel's type. Caller treats it as a soft skip.
var ErrUnsupportedChannel = errors.New("unsupported channel type")
