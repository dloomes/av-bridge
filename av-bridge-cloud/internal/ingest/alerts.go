package ingest

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

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
// returned unchanged — alerts subsystem ignores them.
func handleAlertEvent(ctx context.Context, tx pgx.Tx, customerID, deviceID string, e eventDTO) error {
	const prefix = "alert:"
	if !strings.HasPrefix(e.EventType, prefix) {
		return nil
	}
	alertKey := strings.TrimPrefix(e.EventType, prefix)
	if alertKey == "" {
		return nil
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
		return err
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

	// ON CONFLICT on the partial unique index: an already-open alert of this
	// kind on this device gets its severity/message/payload refreshed and
	// opened_at bumped, but stays "open". This means a long-running offline
	// device shows the most recent message + the original opened_at... wait —
	// we deliberately DO NOT bump opened_at so dashboards reflect when the
	// problem actually started, not the latest re-fire.
	_, err = tx.Exec(ctx, `
		INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, payload, status)
		VALUES ($1, $2, $3, $4, COALESCE($5,''), $6::jsonb, 'open')
		ON CONFLICT (device_id, alert_key) WHERE status = 'open' DO UPDATE
		SET severity = EXCLUDED.severity,
		    message  = EXCLUDED.message,
		    payload  = EXCLUDED.payload`,
		customerID, deviceID, alertKey, severity, message, payloadParam)
	return err
}
