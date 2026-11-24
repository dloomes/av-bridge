package integrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

// condition is one alertable state of a Lens-managed device.
type condition struct {
	key       string
	hold      time.Duration // how long it must persist before an alert opens
	severity  string
	message   func(r Readings) string
	recovered func(r Readings) string
	// state reports whether the condition holds. known is false when the
	// readings do not say, which neither starts nor clears it.
	state func(r Readings) (active, known bool)
}

// label is the kind of room the readings are for ("Teams Room" by default).
func (r Readings) label() string {
	if l := strings.TrimSpace(r.Label); l != "" {
		return l
	}
	return "Teams Room"
}

func fixed(s string) func(Readings) string { return func(Readings) string { return s } }

var conditions = []condition{
	{
		key: "device_offline", hold: 5 * time.Minute, severity: "critical",
		recovered: func(r Readings) string { return r.label() + " back online" },
		message:   func(r Readings) string { return r.label() + " offline" },
		state: func(r Readings) (bool, bool) {
			switch r.Status {
			case "offline":
				return true, true
			case "online":
				return false, true
			}
			return false, false
		},
	},
	{
		key: "teams_signed_out", recovered: fixed("Teams signed in again"), hold: 10 * time.Minute, severity: "critical",
		message: func(Readings) string { return "Teams signed out" },
		state: func(r Readings) (bool, bool) {
			if r.SignedIn == nil {
				return false, false
			}
			return !*r.SignedIn, true
		},
	},
	{
		key: "teams_unhealthy", recovered: fixed("Teams health recovered"), hold: 10 * time.Minute, severity: "warning",
		message: func(r Readings) string { return "Teams health: " + strings.TrimSpace(r.TeamsHealth) },
		state: func(r Readings) (bool, bool) {
			h := strings.ToLower(strings.TrimSpace(r.TeamsHealth))
			switch h {
			case "", "unknown":
				return false, false
			// Still unverified: teamsDeviceHealthStatus was empty on every Teams Room in
			// the real tenant checked in Task 11, so these values are a guess.
			case "healthy", "ok", "normal", "good":
				return false, true
			}
			return true, true
		},
	},
	{
		key: "peripherals_missing", recovered: fixed("Peripherals linked again"), hold: 10 * time.Minute, severity: "warning",
		message: func(r Readings) string {
			n := len(r.MissingPeripherals)
			if n == 0 {
				return "Peripheral missing"
			}
			shown := r.MissingPeripherals
			if n > 3 {
				shown = shown[:3]
			}
			msg := "Peripheral missing: " + strings.Join(shown, ", ")
			if n > 3 {
				msg += fmt.Sprintf(" +%d more", n-3)
			}
			return msg
		},
		state: func(r Readings) (bool, bool) {
			if r.LinkedDevices > 0 {
				return len(r.MissingPeripherals) > 0, true
			}
			if r.PeripheralsLinked == nil {
				return false, false
			}
			return !*r.PeripheralsLinked, true
		},
	},
}

// Maintenance window: alerts are held (not opened) from 01:00 to 05:00 local
// time and open afterwards if the condition is still present.
const (
	holdFromHour = 1
	holdToHour   = 5
)

const defaultZone = "Europe/London"

// Alerter opens and clears alerts for Teams Rooms from their latest readings:
// Lens-managed ones (from the poller) and ones a Collector reads directly
// (mtr_windows, from ingest). State lives in integration_alert_state so it
// survives restarts.
type Alerter struct {
	admin *pgxpool.Pool
	now   func() time.Time
	log   *slog.Logger
	// dispatch sends a notification; defaults to the dispatcher's Dispatch.
	dispatch func(notify.AlertEvent)
}

func NewAlerter(admin *pgxpool.Pool, dispatcher *notify.Dispatcher, now func() time.Time, log *slog.Logger) *Alerter {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	a := &Alerter{admin: admin, now: now, log: log}
	a.dispatch = func(e notify.AlertEvent) { dispatcher.Dispatch(e) } // nil-safe
	return a
}

// inMaintenanceWindow reports whether t falls in 01:00-05:00 in zone.
func inMaintenanceWindow(t time.Time, zone string) bool {
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil || strings.TrimSpace(zone) == "" {
		loc, err = time.LoadLocation(defaultZone)
		if err != nil {
			loc = time.UTC
		}
	}
	h := t.In(loc).Hour()
	return h >= holdFromHour && h < holdToHour
}

// Evaluate updates alert state for one device. Notifications are sent only
// after the transaction commits.
func (a *Alerter) Evaluate(ctx context.Context, customerID, deviceID string, r Readings) error {
	if r.Status == "unknown" {
		// Lens could not say: keep every state and open alert as it is.
		return nil
	}
	now := a.now()
	var events []notify.AlertEvent

	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_customer', $1, true)", customerID); err != nil {
		return err
	}

	var name, zone string
	var switchedOff bool
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(d.name, ''), COALESCE(b.timezone, ''), d.switched_off_by_run IS NOT NULL
		  FROM devices d
		  LEFT JOIN rooms r ON r.id = d.room_id
		  LEFT JOIN buildings b ON b.id = r.building_id
		 WHERE d.id = $1 AND d.customer_id = $2 AND d.deleted_at IS NULL`, deviceID, customerID).
		Scan(&name, &zone, &switchedOff)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	held := inMaintenanceWindow(now, zone)

	for _, c := range conditions {
		active, known := c.state(r)
		if !known {
			continue
		}
		var since time.Time
		var alertID *string
		err := tx.QueryRow(ctx, `
			SELECT since, alert_id::text FROM integration_alert_state
			 WHERE device_id = $1 AND condition = $2 FOR UPDATE`, deviceID, c.key).Scan(&since, &alertID)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if !active {
			if !exists {
				continue
			}
			if alertID != nil {
				tag, err := tx.Exec(ctx, `
					UPDATE alerts SET status = 'resolved', resolved_at = $2, resolved_by = 'auto:recovered'
					 WHERE id = $1 AND status IN ('open','acknowledged')`, *alertID, now)
				if err != nil {
					return err
				}
				if tag.RowsAffected() > 0 {
					events = append(events, a.event(r.Source, customerID, deviceID, name, *alertID, c.key+"_recovered", c.severity, c.recovered(r), now, true))
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM integration_alert_state WHERE device_id = $1 AND condition = $2`, deviceID, c.key); err != nil {
				return err
			}
			continue
		}

		if !exists {
			since = now
			if _, err := tx.Exec(ctx, `
				INSERT INTO integration_alert_state (device_id, customer_id, condition, since)
				VALUES ($1, $2, $3, $4)`, deviceID, customerID, c.key, since); err != nil {
				return err
			}
		}
		if alertID != nil || held || now.Sub(since) < c.hold {
			continue
		}
		if c.key == "device_offline" && switchedOff {
			continue // expected: Room Readiness switched it off
		}
		msg := c.message(r)
		var inserted bool
		var id string
		var openedAt time.Time
		err = tx.QueryRow(ctx, `
			INSERT INTO alerts (customer_id, device_id, alert_key, severity, message, status, opened_at)
			VALUES ($1, $2, $3, $4, $5, 'open', $6)
			ON CONFLICT (device_id, alert_key) WHERE status = 'open' DO UPDATE
			SET severity = EXCLUDED.severity, message = EXCLUDED.message
			RETURNING (xmax = 0), id::text, opened_at`,
			customerID, deviceID, c.key, c.severity, msg, now).Scan(&inserted, &id, &openedAt)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE integration_alert_state SET alert_id = $3 WHERE device_id = $1 AND condition = $2`,
			deviceID, c.key, id); err != nil {
			return err
		}
		if inserted {
			events = append(events, a.event(r.Source, customerID, deviceID, name, id, c.key, c.severity, msg, openedAt, false))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, e := range events {
		a.dispatch(e)
	}
	return nil
}

func (a *Alerter) event(source, customerID, deviceID, name, alertID, key, severity, msg string, at time.Time, recovered bool) notify.AlertEvent {
	if source == "" {
		source = "poly_lens"
	}
	payload := map[string]any{"severity": severity, "message": msg, "source": source}
	if recovered {
		payload["recovered"] = true
		payload["alert_key"] = strings.TrimSuffix(key, "_recovered")
	}
	return notify.AlertEvent{
		AlertID:    alertID,
		CustomerID: customerID,
		DeviceID:   deviceID,
		DeviceName: name,
		AlertKey:   key,
		Severity:   severity,
		Message:    msg,
		OpenedAt:   at,
		Payload:    payload,
	}
}
