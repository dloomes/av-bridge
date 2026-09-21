package portalapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/devicestatus"
	"github.com/jackc/pgx/v5"
)

// Reports — read-only rollups against existing telemetry/events/alerts data.
// Two v1 reports cover the most-asked-for views from the original scope:
//
//   - Device uptime: % of polled samples in the window where the device
//     reported "online". Approximate by design (poll rate varies per device)
//     but transparent and easy to explain to a customer.
//   - Room activity: count of device events per room, useful for spotting
//     idle rooms and busy ones at a glance.
//
// Both endpoints accept ?days=N (default 7, max 365) and ?format=csv to
// switch the response between JSON and a downloadable CSV file. Charts are
// the portal's job — these endpoints just return rows.

const (
	defaultReportDays = 7
	maxReportDays     = 365
)

func reportDays(r *http.Request) int {
	v := r.URL.Query().Get("days")
	if v == "" {
		return defaultReportDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultReportDays
	}
	if n > maxReportDays {
		return maxReportDays
	}
	return n
}

// DeviceUptimeReport — GET /api/v1/reports/device-uptime?days=N[&format=csv]
//
// Returns one row per device with its sampled uptime percentage over the
// requested window. Devices with zero samples in the window (newly added,
// or never polled successfully) get a null uptime_pct rather than 0% — they
// haven't been observed, not necessarily down.
func (h *Handler) DeviceUptimeReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"

	type row struct {
		DeviceID      string     `json:"device_id"`
		Name          string     `json:"name"`
		Location      string     `json:"location"`
		Samples       int        `json:"samples"`
		OnlineSamples int        `json:"online_samples"`
		UptimePct     *float64   `json:"uptime_pct,omitempty"`
		CurrentStatus string     `json:"current_status"`
		LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	}
	out := []row{}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// We cast the parameter to text + concat with '::interval' inline so
		// pgx doesn't need to know about the interval type explicitly. Window
		// is bounded so the cast is safe.
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			WITH win AS (SELECT now() - interval '%d days' AS since)
			SELECT
			  d.id::text,
			  COALESCE(d.name, d.reported_id, '') AS dev_name,
			  CASE
			    WHEN b.name IS NOT NULL AND r.name IS NOT NULL THEN b.name || ' / ' || r.name
			    WHEN r.name IS NOT NULL THEN r.name
			    ELSE ''
			  END AS dev_location,
			  COUNT(t.*)::int AS samples,
			  COUNT(t.*) FILTER (WHERE t.status = 'online')::int AS online_samples,
			  CASE WHEN COUNT(t.*) > 0
			    THEN ROUND(100.0 * COUNT(t.*) FILTER (WHERE t.status = 'online') / COUNT(t.*), 2)
			    ELSE NULL END AS uptime_pct,
			  ` + devicestatus.EffectiveStatusSQL + ` AS dev_current_status,
			  d.last_seen_at
			FROM devices d
			LEFT JOIN rooms r ON r.id = d.room_id
			LEFT JOIN buildings b ON b.id = r.building_id
			LEFT JOIN collectors c ON c.id = d.collector_id
			LEFT JOIN telemetry t ON t.device_id = d.id AND t.ts >= (SELECT since FROM win)
			WHERE d.deleted_at IS NULL
			GROUP BY d.id, d.name, d.reported_id, r.name, b.name, d.latest_status, d.last_seen_at, c.last_seen_at
			ORDER BY uptime_pct ASC NULLS LAST, dev_name`, days))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				rr  row
				pct *float64
			)
			if err := rows.Scan(&rr.DeviceID, &rr.Name, &rr.Location,
				&rr.Samples, &rr.OnlineSamples, &pct,
				&rr.CurrentStatus, &rr.LastSeenAt); err != nil {
				return err
			}
			rr.UptimePct = pct
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("device-uptime-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"device_id", "name", "location", "samples",
			"online_samples", "uptime_pct", "current_status", "last_seen_at"})
		for _, rr := range out {
			pct := ""
			if rr.UptimePct != nil {
				pct = strconv.FormatFloat(*rr.UptimePct, 'f', 2, 64)
			}
			lastSeen := ""
			if rr.LastSeenAt != nil {
				lastSeen = rr.LastSeenAt.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{
				rr.DeviceID, rr.Name, rr.Location,
				strconv.Itoa(rr.Samples), strconv.Itoa(rr.OnlineSamples),
				pct, rr.CurrentStatus, lastSeen,
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// RoomActivityReport — GET /api/v1/reports/room-activity?days=N[&format=csv]
//
// Counts device events per room over the window. Rooms with zero events
// still appear (left join) so an empty room is visible as "unused". Useful
// for spotting underutilised rooms and load-balancing schedules.
func (h *Handler) RoomActivityReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"

	type row struct {
		RoomID       string     `json:"room_id"`
		RoomName     string     `json:"room_name"`
		BuildingName string     `json:"building_name"`
		DeviceCount  int        `json:"device_count"`
		EventCount   int        `json:"event_count"`
		LastEventAt  *time.Time `json:"last_event_at,omitempty"`
	}
	out := []row{}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			WITH win AS (SELECT now() - interval '%d days' AS since)
			SELECT
			  r.id::text,
			  r.name,
			  COALESCE(b.name, ''),
			  COUNT(DISTINCT d.id)::int AS device_count,
			  COUNT(e.*)::int AS event_count,
			  MAX(e.ts) AS last_event_at
			FROM rooms r
			LEFT JOIN buildings b ON b.id = r.building_id
			LEFT JOIN devices d ON d.room_id = r.id AND d.deleted_at IS NULL
			LEFT JOIN events e ON e.device_id = d.id AND e.ts >= (SELECT since FROM win)
			GROUP BY r.id, r.name, b.name
			ORDER BY event_count DESC, r.name`, days))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName,
				&rr.DeviceCount, &rr.EventCount, &rr.LastEventAt); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("room-activity-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"room_id", "room_name", "building_name",
			"device_count", "event_count", "last_event_at"})
		for _, rr := range out {
			lastEvent := ""
			if rr.LastEventAt != nil {
				lastEvent = rr.LastEventAt.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{
				rr.RoomID, rr.RoomName, rr.BuildingName,
				strconv.Itoa(rr.DeviceCount), strconv.Itoa(rr.EventCount), lastEvent,
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// WarrantyReport — GET /api/v1/reports/warranty[?format=csv]
//
// One row per asset. `bucket` classifies each warranty end date into a
// severity band the portal renders as a chip: expired < 30d < 90d < 365d
// < later. `no_date` covers assets without a warranty_end recorded so the
// operator can see where the data gap is.
//
// Not windowed — warranty is a lifetime state, not a rolling metric.
func (h *Handler) WarrantyReport(w http.ResponseWriter, r *http.Request) {
	wantCSV := r.URL.Query().Get("format") == "csv"

	type row struct {
		AssetID       string `json:"asset_id"`
		AssetTag      string `json:"asset_tag,omitempty"`
		Name          string `json:"name"`
		Category      string `json:"category"`
		Manufacturer  string `json:"manufacturer,omitempty"`
		Model         string `json:"model,omitempty"`
		SerialNumber  string `json:"serial_number,omitempty"`
		Status        string `json:"status"`
		Location      string `json:"location,omitempty"`
		WarrantyEnd   string `json:"warranty_end,omitempty"`
		DaysRemaining *int   `json:"days_remaining,omitempty"`
		Bucket        string `json:"bucket"`
	}
	out := []row{}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id::text,
			       COALESCE(a.asset_tag, ''),
			       a.name,
			       a.category,
			       COALESCE(a.manufacturer, ''),
			       COALESCE(a.model, ''),
			       COALESCE(a.serial_number, ''),
			       a.status,
			       CASE
			         WHEN b.name IS NOT NULL AND rm.name IS NOT NULL
			              THEN b.name || ' / ' || rm.name
			         WHEN rm.name IS NOT NULL THEN rm.name
			         ELSE ''
			       END AS location,
			       a.warranty_end,
			       CASE
			         WHEN a.warranty_end IS NULL THEN NULL
			         ELSE (a.warranty_end - CURRENT_DATE)
			       END AS days_remaining
			  FROM assets a
			  LEFT JOIN rooms rm     ON rm.id = a.room_id
			  LEFT JOIN buildings b  ON b.id  = rm.building_id
			 WHERE a.status <> 'retired'
			 ORDER BY a.warranty_end NULLS LAST, a.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				rr       row
				warranty *time.Time
				daysLeft *int
			)
			if err := rows.Scan(&rr.AssetID, &rr.AssetTag, &rr.Name, &rr.Category,
				&rr.Manufacturer, &rr.Model, &rr.SerialNumber, &rr.Status, &rr.Location,
				&warranty, &daysLeft); err != nil {
				return err
			}
			if warranty != nil {
				rr.WarrantyEnd = warranty.Format("2006-01-02")
			}
			rr.DaysRemaining = daysLeft
			rr.Bucket = warrantyBucket(daysLeft)
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("warranty-%s.csv", time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"asset_id", "asset_tag", "name", "category",
			"manufacturer", "model", "serial_number", "status", "location",
			"warranty_end", "days_remaining", "bucket"})
		for _, rr := range out {
			daysStr := ""
			if rr.DaysRemaining != nil {
				daysStr = strconv.Itoa(*rr.DaysRemaining)
			}
			_ = cw.Write([]string{
				rr.AssetID, rr.AssetTag, rr.Name, rr.Category,
				rr.Manufacturer, rr.Model, rr.SerialNumber, rr.Status, rr.Location,
				rr.WarrantyEnd, daysStr, rr.Bucket,
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func warrantyBucket(daysLeft *int) string {
	if daysLeft == nil {
		return "no_date"
	}
	switch {
	case *daysLeft < 0:
		return "expired"
	case *daysLeft <= 30:
		return "lt_30d"
	case *daysLeft <= 90:
		return "lt_90d"
	case *daysLeft <= 365:
		return "lt_365d"
	default:
		return "later"
	}
}

// RoomUtilisationReport — GET /api/v1/reports/room-utilisation?days=N[&format=csv]
//
// "Utilisation" defined as active-hours-per-day: for each room in the window,
// the count of distinct (day, hour) buckets where any device in the room
// fired an event. Averaged over the window this gives a comparable "hours in
// active use per day" figure per room. Approximate (device chatter can
// inflate idle rooms slightly; sparse-event integrations can undercount) but
// requires no per-tenant business-hours config to run.
//
// Rooms with zero events still appear so operators can see under-used rooms.
func (h *Handler) RoomUtilisationReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"

	type row struct {
		RoomID         string  `json:"room_id"`
		RoomName       string  `json:"room_name"`
		BuildingName   string  `json:"building_name"`
		DeviceCount    int     `json:"device_count"`
		ActiveHours    int     `json:"active_hours"`
		ActiveDays     int     `json:"active_days"`
		AvgHoursPerDay float64 `json:"avg_hours_per_day"`
	}
	out := []row{}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			WITH win AS (SELECT now() - interval '%d days' AS since),
			active AS (
			    SELECT d.room_id,
			           COUNT(DISTINCT date_trunc('hour', e.ts)) AS active_hours,
			           COUNT(DISTINCT date_trunc('day',  e.ts)) AS active_days
			      FROM events e
			      JOIN devices d ON d.id = e.device_id
			     WHERE e.ts >= (SELECT since FROM win)
			       AND d.room_id IS NOT NULL
			     GROUP BY d.room_id
			)
			SELECT r.id::text,
			       r.name,
			       COALESCE(b.name, ''),
			       COUNT(DISTINCT d.id)::int AS device_count,
			       COALESCE(a.active_hours, 0)::int,
			       COALESCE(a.active_days,  0)::int,
			       ROUND(COALESCE(a.active_hours, 0)::numeric / %d, 2)::float8 AS avg_hours_per_day
			  FROM rooms r
			  LEFT JOIN buildings b ON b.id = r.building_id
			  LEFT JOIN devices  d  ON d.room_id = r.id AND d.deleted_at IS NULL
			  LEFT JOIN active   a  ON a.room_id = r.id
			 GROUP BY r.id, r.name, b.name, a.active_hours, a.active_days
			 ORDER BY avg_hours_per_day DESC, r.name`, days, days))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName,
				&rr.DeviceCount, &rr.ActiveHours, &rr.ActiveDays, &rr.AvgHoursPerDay); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("room-utilisation-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"room_id", "room_name", "building_name",
			"device_count", "active_hours", "active_days", "avg_hours_per_day"})
		for _, rr := range out {
			_ = cw.Write([]string{
				rr.RoomID, rr.RoomName, rr.BuildingName,
				strconv.Itoa(rr.DeviceCount),
				strconv.Itoa(rr.ActiveHours),
				strconv.Itoa(rr.ActiveDays),
				strconv.FormatFloat(rr.AvgHoursPerDay, 'f', 2, 64),
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PowerReport — GET /api/v1/reports/power?days=N[&format=csv]
//
// Per-room kWh estimate for the window, plus kWh saved via the nightly
// power-down routine. Only devices with a `power_watts_on` value contribute;
// devices without a rating are counted in `unrated_devices` so the operator
// can see the data gap.
//
// Consumed model (approximate — labelled as such in the response):
//   consumed_kWh = (uptime_pct × window_hours × watts_on
//                 + downtime_pct × window_hours × watts_standby) / 1000
//
// Saved model (via nightly power-down):
//   saved_kWh = successful_nightly_runs × avg_off_hours
//                × Σ(watts_on - watts_standby) / 1000
//
// Both approximate; the portal captions the numbers accordingly. Standby
// defaults to zero when not set, which slightly overstates savings but keeps
// the report useful for devices with only a nameplate on-figure.
func (h *Handler) PowerReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"

	type row struct {
		RoomID         string  `json:"room_id"`
		RoomName       string  `json:"room_name"`
		BuildingName   string  `json:"building_name"`
		RatedDevices   int     `json:"rated_devices"`
		UnratedDevices int     `json:"unrated_devices"`
		ConsumedKWh    float64 `json:"consumed_kwh"`
		SavedKWh       float64 `json:"saved_kwh"`
		NightlyRuns    int     `json:"nightly_runs"`
		AvgOffHours    float64 `json:"avg_off_hours"`
	}
	out := []row{}

	windowHours := float64(days) * 24.0

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			WITH win AS (SELECT now() - interval '%d days' AS since),
			room_devices AS (
			    SELECT d.id AS device_id,
			           d.room_id,
			           d.power_watts_on,
			           COALESCE(d.power_watts_standby, 0)::numeric AS standby
			      FROM devices d
			     WHERE d.deleted_at IS NULL
			       AND d.room_id IS NOT NULL
			),
			dev_uptime AS (
			    SELECT rd.device_id,
			           rd.room_id,
			           rd.power_watts_on,
			           rd.standby,
			           COUNT(t.*)::numeric                                    AS samples,
			           COUNT(t.*) FILTER (WHERE t.status = 'online')::numeric AS online_samples
			      FROM room_devices rd
			      LEFT JOIN telemetry t
			             ON t.device_id = rd.device_id
			            AND t.ts >= (SELECT since FROM win)
			     GROUP BY rd.device_id, rd.room_id, rd.power_watts_on, rd.standby
			),
			dev_consumed AS (
			    SELECT du.room_id,
			           du.device_id,
			           du.power_watts_on,
			           du.standby,
			           CASE
			             WHEN du.power_watts_on IS NULL THEN NULL
			             WHEN du.samples = 0 THEN
			                 du.power_watts_on * %f / 1000.0
			             ELSE (
			                 du.power_watts_on * (du.online_samples / du.samples) * %f
			               + du.standby        * (1 - du.online_samples / du.samples) * %f
			             ) / 1000.0
			           END AS kwh
			      FROM dev_uptime du
			),
			room_nightly AS (
			    SELECT nr.room_id,
			           COUNT(*)::int AS runs,
			           AVG(EXTRACT(EPOCH FROM
			               CASE
			                 WHEN COALESCE(rnc.power_on_time, ns.power_on_time)
			                      > COALESCE(rnc.power_off_time, ns.power_off_time)
			                 THEN COALESCE(rnc.power_on_time, ns.power_on_time)
			                      - COALESCE(rnc.power_off_time, ns.power_off_time)
			                 ELSE ('24:00:00'::interval
			                      - (COALESCE(rnc.power_off_time, ns.power_off_time)
			                      -  COALESCE(rnc.power_on_time, ns.power_on_time)))
			               END
			           )) / 3600.0 AS avg_off_hours
			      FROM nightly_run nr
			      JOIN nightly_schedule ns ON ns.customer_id = nr.customer_id
			      LEFT JOIN room_nightly_config rnc ON rnc.room_id = nr.room_id
			     WHERE nr.scheduled_at >= (SELECT since FROM win)
			       AND nr.phase IN ('off', 'scheduled_on', 'waking', 'warming', 'testing', 'ready')
			       AND nr.status <> 'failed'
			     GROUP BY nr.room_id
			),
			room_rollup AS (
			    SELECT dc.room_id,
			           SUM(CASE WHEN dc.power_watts_on IS NOT NULL THEN 1 ELSE 0 END)::int AS rated,
			           SUM(CASE WHEN dc.power_watts_on IS NULL     THEN 1 ELSE 0 END)::int AS unrated,
			           COALESCE(SUM(dc.kwh), 0)                                            AS consumed,
			           COALESCE(SUM(dc.power_watts_on - dc.standby)
			                    FILTER (WHERE dc.power_watts_on IS NOT NULL), 0)           AS on_minus_standby_sum
			      FROM dev_consumed dc
			     GROUP BY dc.room_id
			)
			SELECT r.id::text,
			       r.name,
			       COALESCE(b.name, ''),
			       COALESCE(rr.rated, 0)          AS rated,
			       COALESCE(rr.unrated, 0)        AS unrated,
			       ROUND(COALESCE(rr.consumed, 0)::numeric, 2)::float8 AS consumed_kwh,
			       ROUND(
			           (COALESCE(rn.runs, 0) * COALESCE(rn.avg_off_hours, 0)
			              * COALESCE(rr.on_minus_standby_sum, 0) / 1000.0)::numeric,
			           2
			       )::float8                       AS saved_kwh,
			       COALESCE(rn.runs, 0)           AS nightly_runs,
			       ROUND(COALESCE(rn.avg_off_hours, 0)::numeric, 2)::float8 AS avg_off_hours
			  FROM rooms r
			  LEFT JOIN buildings b   ON b.id = r.building_id
			  LEFT JOIN room_rollup rr ON rr.room_id = r.id
			  LEFT JOIN room_nightly rn ON rn.room_id = r.id
			 ORDER BY consumed_kwh DESC, r.name`,
			days, windowHours, windowHours, windowHours))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName,
				&rr.RatedDevices, &rr.UnratedDevices,
				&rr.ConsumedKWh, &rr.SavedKWh,
				&rr.NightlyRuns, &rr.AvgOffHours); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("power-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"room_id", "room_name", "building_name",
			"rated_devices", "unrated_devices", "consumed_kwh", "saved_kwh",
			"nightly_runs", "avg_off_hours"})
		for _, rr := range out {
			_ = cw.Write([]string{
				rr.RoomID, rr.RoomName, rr.BuildingName,
				strconv.Itoa(rr.RatedDevices), strconv.Itoa(rr.UnratedDevices),
				strconv.FormatFloat(rr.ConsumedKWh, 'f', 2, 64),
				strconv.FormatFloat(rr.SavedKWh, 'f', 2, 64),
				strconv.Itoa(rr.NightlyRuns),
				strconv.FormatFloat(rr.AvgOffHours, 'f', 2, 64),
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, out)
}
