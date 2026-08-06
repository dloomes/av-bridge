// Package collectorhealth watches the collectors table and opens/resolves
// alerts when a bridge stops phoning home.
//
// Runs as app_admin (cross-tenant) — one goroutine covers every customer.
// Each tick:
//
//  1. Any collector whose last_seen_at is older than OfflineAfter and does
//     NOT already have an open collector_offline alert gets one opened.
//  2. Any open collector_offline alert whose collector has since checked
//     in (last_seen_at within the offline window) is auto-resolved.
//
// Newly-opened alerts are dispatched through the notify.Dispatcher so the
// customer's configured email/Teams/webhook channels fire, matching
// device-level alert behaviour.
package collectorhealth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

type Watcher struct {
	admin        *pgxpool.Pool
	dispatcher   *notify.Dispatcher
	interval     time.Duration
	offlineAfter time.Duration
	log          *slog.Logger
}

// NewWatcher. dispatcher may be nil — the DB writes still happen but no
// outbound email/Teams/webhook goes out. That matches the ingest handler's
// treatment of the dispatcher and keeps tests / minimal deployments simple.
func NewWatcher(admin *pgxpool.Pool, dispatcher *notify.Dispatcher, interval, offlineAfter time.Duration, log *slog.Logger) *Watcher {
	return &Watcher{
		admin:        admin,
		dispatcher:   dispatcher,
		interval:     interval,
		offlineAfter: offlineAfter,
		log:          log,
	}
}

func (w *Watcher) Run(ctx context.Context) {
	if w.interval <= 0 || w.offlineAfter <= 0 {
		w.log.Warn("collector-health watcher disabled",
			"interval", w.interval, "offline_after", w.offlineAfter)
		return
	}
	w.log.Info("collector-health watcher started",
		"interval", w.interval, "offline_after", w.offlineAfter)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	// Fire once immediately so a cloud restart doesn't wait a full interval
	// before catching whatever's already offline.
	w.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Sweep(ctx)
		}
	}
}

// Sweep runs one open+resolve pass. Exposed so tests can drive it directly.
func (w *Watcher) Sweep(ctx context.Context) {
	if err := w.autoResolve(ctx); err != nil {
		w.log.Warn("collector-health auto-resolve failed", "error", err)
	}
	newAlerts, err := w.openStale(ctx)
	if err != nil {
		w.log.Warn("collector-health open-stale failed", "error", err)
		return
	}
	if w.dispatcher != nil {
		for _, evt := range newAlerts {
			w.dispatcher.Dispatch(evt)
		}
	}
}

// autoResolve closes open collector_offline alerts whose collector has
// checked in again. resolved_by = 'auto:recovered' matches the tag the
// device-side auto-resolve uses so the audit trail reads consistently.
func (w *Watcher) autoResolve(ctx context.Context) error {
	_, err := w.admin.Exec(ctx, `
		UPDATE alerts
		   SET status = 'resolved',
		       resolved_at = now(),
		       resolved_by = 'auto:recovered'
		 WHERE status = 'open'
		   AND alert_key = 'collector_offline'
		   AND collector_id IN (
		       SELECT id FROM collectors
		        WHERE last_seen_at IS NOT NULL
		          AND last_seen_at > now() - make_interval(secs => $1)
		   )`,
		int(w.offlineAfter.Seconds()))
	return err
}

// openStale inserts a collector_offline alert for every collector past the
// threshold that doesn't already have one open. The partial unique index
// on (collector_id, alert_key) WHERE status='open' would collide on a
// re-fire, but the WHERE-NOT-EXISTS pre-check keeps INSERT clean and lets
// us return the freshly-inserted rows for notify dispatch.
func (w *Watcher) openStale(ctx context.Context) ([]notify.AlertEvent, error) {
	rows, err := w.admin.Query(ctx, `
		WITH stale AS (
		    SELECT c.id, c.customer_id, c.name, c.last_seen_at
		      FROM collectors c
		     WHERE c.last_seen_at IS NOT NULL
		       AND c.last_seen_at <= now() - make_interval(secs => $1)
		       AND NOT EXISTS (
		           SELECT 1 FROM alerts a
		            WHERE a.collector_id = c.id
		              AND a.alert_key = 'collector_offline'
		              AND a.status = 'open'
		       )
		)
		INSERT INTO alerts (customer_id, collector_id, alert_key, severity, message, payload, status)
		SELECT s.customer_id, s.id, 'collector_offline', 'critical',
		       'Collector has not reported telemetry recently',
		       jsonb_build_object(
		           'last_seen_at', to_char(s.last_seen_at AT TIME ZONE 'UTC',
		                                   'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		           'threshold_seconds', $1
		       ),
		       'open'
		  FROM stale s
		RETURNING customer_id::text,
		          collector_id::text,
		          (SELECT name FROM collectors WHERE id = alerts.collector_id),
		          message,
		          opened_at,
		          payload::text`,
		int(w.offlineAfter.Seconds()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var events []notify.AlertEvent
	for rows.Next() {
		var (
			customerID, collectorID, name, message, payloadText string
			openedAt                                            time.Time
		)
		if err := rows.Scan(&customerID, &collectorID, &name, &message, &openedAt, &payloadText); err != nil {
			return nil, err
		}
		payloadMap := map[string]any{}
		_ = json.Unmarshal([]byte(payloadText), &payloadMap)
		events = append(events, notify.AlertEvent{
			CustomerID:    customerID,
			CollectorID:   collectorID,
			CollectorName: name,
			AlertKey:      "collector_offline",
			Severity:      "critical",
			Message:       message,
			OpenedAt:      openedAt,
			Payload:       payloadMap,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(events) > 0 {
		w.log.Info("collector-health opened alerts", "count", len(events))
	}
	return events, nil
}
