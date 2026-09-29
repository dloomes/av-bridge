package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ticketing channels (ServiceNow) differ from the fire-and-forget ones: an
// incident opened for an alert must also be resolved when the alert
// resolves. Alerts resolve in several places (device recovery, collector
// check-in, manual resolve, device deletion), so rather than hook each one,
// SyncTickets runs on a timer and reconciles alert_tickets with alerts:
//
//   - state 'open'    + alert resolved        → resolve the incident
//   - state 'pending' + alert still unresolved → retry the open (capped)
//   - state 'pending' + alert resolved         → 'skipped', nothing to raise
//
// A pending row is written before the first attempt, so an open that fails
// (ServiceNow down, bad credentials) is retried rather than lost, and the
// unique (alert, channel) key means a ticket is never raised twice.

const maxTicketAttempts = 10

// SetServiceNow enables the servicenow channel type.
func (d *Dispatcher) SetServiceNow(s *ServiceNow) { d.snow = s }

// openTicket raises the incident for a newly opened alert on one channel.
func (d *Dispatcher) openTicket(ctx context.Context, ch Channel, evt AlertEvent) error {
	if d.snow == nil {
		return ErrUnsupportedChannel
	}
	if evt.AlertID == "" {
		_, err := d.snow.Open(ctx, ch, evt)
		return err
	}
	var ticketID string
	err := d.pool.QueryRow(ctx, `
		INSERT INTO alert_tickets (customer_id, alert_id, channel_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (alert_id, channel_id) DO NOTHING
		RETURNING id::text`, evt.CustomerID, evt.AlertID, ch.ID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already raised (or being retried) for this alert
	}
	if err != nil {
		return err
	}
	return d.attemptOpen(ctx, ticketID, ch, evt)
}

func (d *Dispatcher) attemptOpen(ctx context.Context, ticketID string, ch Channel, evt AlertEvent) error {
	inc, err := d.snow.Open(ctx, ch, evt)
	if err != nil {
		_, uerr := d.pool.Exec(ctx, `
			UPDATE alert_tickets
			   SET attempts = attempts + 1, last_error = $2, updated_at = now()
			 WHERE id = $1`, ticketID, truncate(err.Error(), 1000))
		return errors.Join(err, uerr)
	}
	_, err = d.pool.Exec(ctx, `
		UPDATE alert_tickets
		   SET state = 'open', external_id = $2, number = $3,
		       attempts = attempts + 1, last_error = NULL, updated_at = now()
		 WHERE id = $1`, ticketID, inc.SysID, inc.Number)
	if err == nil {
		d.log.Info("notify: servicenow incident opened",
			"channel", ch.ID, "alert", evt.AlertID, "incident", inc.Number)
	}
	return err
}

// testServiceNow opens a test incident and resolves it straight away, so one
// click proves both halves: create and update rights.
func (d *Dispatcher) testServiceNow(ctx context.Context, ch Channel, evt AlertEvent) error {
	if d.snow == nil {
		return ErrUnsupportedChannel
	}
	inc, err := d.snow.Open(ctx, ch, evt)
	if err != nil {
		return err
	}
	if err := d.snow.Resolve(ctx, ch, inc.SysID, "Test incident from M.A.R.C.U.S., resolved automatically."); err != nil {
		return fmt.Errorf("created %s but couldn't resolve it: %w", inc.Number, err)
	}
	return nil
}

// RunTicketSync reconciles tickets every interval until ctx ends.
func (d *Dispatcher) RunTicketSync(ctx context.Context, interval time.Duration) {
	if d == nil || d.snow == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		d.SyncTickets(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type ticketRow struct {
	id, externalID, alertStatus, resolvedBy string
	ch                                      Channel
	evt                                     AlertEvent
}

// SyncTickets runs one reconcile pass. Exposed for tests.
func (d *Dispatcher) SyncTickets(ctx context.Context) {
	if d.snow == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rows, err := d.activeTickets(ctx)
	if err != nil {
		d.log.Warn("notify: ticket sync query failed", "error", err)
		return
	}
	for _, t := range rows {
		resolved := t.alertStatus == "resolved"
		switch {
		case t.externalID != "" && resolved:
			err := d.snow.Resolve(ctx, t.ch, t.externalID, resolveNote(t.resolvedBy))
			if err != nil {
				d.recordTicketError(ctx, t.id, err)
				d.log.Warn("notify: servicenow resolve failed", "ticket", t.id, "error", err)
				continue
			}
			if _, err := d.pool.Exec(ctx, `
				UPDATE alert_tickets
				   SET state = 'resolved', resolved_at = now(), last_error = NULL, updated_at = now()
				 WHERE id = $1`, t.id); err != nil {
				d.log.Warn("notify: ticket update failed", "ticket", t.id, "error", err)
			}
		case t.externalID == "" && resolved:
			_, _ = d.pool.Exec(ctx, `UPDATE alert_tickets SET state = 'skipped', updated_at = now() WHERE id = $1`, t.id)
		case t.externalID == "":
			if err := d.attemptOpen(ctx, t.id, t.ch, t.evt); err != nil {
				d.log.Warn("notify: servicenow open retry failed", "ticket", t.id, "error", err)
			}
		}
	}
}

func (d *Dispatcher) activeTickets(ctx context.Context) ([]ticketRow, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT t.id::text, COALESCE(t.external_id,''), a.status, COALESCE(a.resolved_by,''),
		       c.id::text, c.type, c.name, c.target, c.config, c.min_severity,
		       a.id::text, a.customer_id::text,
		       COALESCE(a.device_id::text,''), COALESCE(dv.name,''),
		       COALESCE(a.collector_id::text,''), COALESCE(col.name,''),
		       a.alert_key, a.severity, a.message, a.opened_at, a.payload
		  FROM alert_tickets t
		  JOIN alerts a                 ON a.id = t.alert_id
		  JOIN notification_channels c  ON c.id = t.channel_id
		  LEFT JOIN devices dv          ON dv.id = a.device_id
		  LEFT JOIN collectors col      ON col.id = a.collector_id
		 WHERE (t.state = 'open' AND a.status = 'resolved')
		    OR (t.state = 'pending' AND (a.status = 'resolved'
		        OR (c.enabled AND t.attempts < $1)))
		 ORDER BY t.created_at
		 LIMIT 200`, maxTicketAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ticketRow
	for rows.Next() {
		var t ticketRow
		var cfg, payload []byte
		if err := rows.Scan(&t.id, &t.externalID, &t.alertStatus, &t.resolvedBy,
			&t.ch.ID, &t.ch.Type, &t.ch.Name, &t.ch.Target, &cfg, &t.ch.MinSeverity,
			&t.evt.AlertID, &t.evt.CustomerID, &t.evt.DeviceID, &t.evt.DeviceName,
			&t.evt.CollectorID, &t.evt.CollectorName,
			&t.evt.AlertKey, &t.evt.Severity, &t.evt.Message, &t.evt.OpenedAt, &payload); err != nil {
			return nil, err
		}
		if len(cfg) > 0 {
			_ = json.Unmarshal(cfg, &t.ch.Config)
		}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &t.evt.Payload)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *Dispatcher) recordTicketError(ctx context.Context, ticketID string, err error) {
	_, _ = d.pool.Exec(ctx, `UPDATE alert_tickets SET last_error = $2, updated_at = now() WHERE id = $1`,
		ticketID, truncate(err.Error(), 1000))
}

func resolveNote(resolvedBy string) string {
	switch resolvedBy {
	case "auto:recovered":
		return "The device recovered; the alert cleared automatically in M.A.R.C.U.S."
	case "system:device-deleted":
		return "The device was removed from M.A.R.C.U.S., so the alert was closed."
	case "":
		return "The alert was resolved in M.A.R.C.U.S."
	}
	return fmt.Sprintf("The alert was resolved in M.A.R.C.U.S. (%s).", resolvedBy)
}
