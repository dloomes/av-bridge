package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/jackc/pgx/v5"
)

// alertOutcome reports what the alert-handler did with an event. Used by
// the ingest handler to decide whether to fan out a notification dispatch
// after the tx commits — only newly-opened alerts notify, re-fires of an
// already-open one don't.
type alertOutcome struct {
	notifyEvent *notify.AlertEvent // non-nil when a new alert was opened
}

// handleAlertEvent persists alert-typed events into the alerts table. Event
// types prefixed "alert:" map to a single alert row keyed on (device, alert_
// key); subsequent fires of the same alert update the existing open row (via
// the partial unique index) instead of duplicating it.
//
// Recovery events ("alert:device_recovered") are treated as auto-resolves —
// any open device_offline or device_degraded for the same device flips to
// "resolved". The recovery event itself is not stored as its own alert row;
// it lives in the events stream for audit purposes.
//
// Other event_types (e.g. tesira_subscription:*, poly_call_active) are
// returned with an empty outcome — alerts subsystem ignores them.
func handleAlertEvent(ctx context.Context, tx pgx.Tx, customerID, deviceID string, e eventDTO) (alertOutcome, error) {
	const prefix = "alert:"
	if !strings.HasPrefix(e.EventType, prefix) {
		return alertOutcome{}, nil
	}
	alertKey := strings.TrimPrefix(e.EventType, prefix)
	if alertKey == "" {
		return alertOutcome{}, nil
	}

	if alertKey == "device_recovered" {
		_, err := tx.Exec(ctx, `
			UPDATE alerts
			   SET status = 'resolved',
			       resolved_at = now(),
			       resolved_by = 'auto:recovered'
			 WHERE device_id = $1
			   AND status = 'open'
			   AND alert_key IN ('device_offline','device_degraded')`,
			deviceID)
		return alertOutcome{}, err
	}

	severity, _ := e.Payload["severity"].(string)
	if severity == "" {
		severity = "warning"
	}
	switch severity {
	case "info", "warning", "critical":
	default:
		// Coerce unknown values to warning rather than fail the whole tx;
		// the CHECK constraint would otherwise reject the row.
		severity = "warning"
	}
	message, _ := e.Payload["message"].(string)

	// Stash the full payload so the portal can inspect any extra metadata
	// the bridge attached (location, durations, raw blobs, etc).
	payloadBytes, err := json.Marshal(e.Payload)
	if err != nil {
		payloadBytes = nil
	}
	var payloadParam any
	if len(payloadBytes) > 0 && string(payloadBytes) != "null" {
		payloadParam = string(payloadBytes)
	}

	// `xmax = 0` is true on the row Postgres just inserted (no prior tuple),
	// false when the row came from the DO UPDATE branch. Lets us distinguish
	// "alert just opened" (worth notifying) from "already-open alert re-fired"
	// (already notified — don't spam).
	var inserted bool
	var openedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, payload, status)
		VALUES ($1, $2, $3, $4, COALESCE($5,''), $6::jsonb, 'open')
		ON CONFLICT (device_id, alert_key) WHERE status = 'open' DO UPDATE
		SET severity = EXCLUDED.severity,
		    message  = EXCLUDED.message,
		    payload  = EXCLUDED.payload
		RETURNING (xmax = 0) AS inserted, opened_at`,
		customerID, deviceID, alertKey, severity, message, payloadParam).
		Scan(&inserted, &openedAt)
	if err != nil {
		// A no-row return shouldn't happen for an UPSERT, but be defensive.
		if errors.Is(err, pgx.ErrNoRows) {
			return alertOutcome{}, nil
		}
		return alertOutcome{}, err
	}

	if !inserted {
		return alertOutcome{}, nil
	}
	return alertOutcome{
		notifyEvent: &notify.AlertEvent{
			CustomerID: customerID,
			DeviceID:   deviceID,
			DeviceName: e.DeviceName,
			AlertKey:   alertKey,
			Severity:   severity,
			Message:    message,
			OpenedAt:   openedAt,
			Payload:    e.Payload,
		},
	}, nil
}
