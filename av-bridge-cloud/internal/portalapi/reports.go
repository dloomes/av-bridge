package portalapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// reportTag reads ?tag=<room tag id>: only rooms with that tag (room_tags,
// migration 0070). ok is false, with a 400 written, for a malformed id.
func reportTag(w http.ResponseWriter, r *http.Request) (tag string, ok bool) {
	tag = strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag != "" && !uuidRe.MatchString(tag) {
		writeErr(w, http.StatusBadRequest, "tag must be a room tag id")
		return "", false
	}
	return tag, true
}

// tagRoomFilter is " AND <roomCol> is a room with the tag", or "" for no
// tag. The id is written into the SQL: reportTag has checked it's a UUID,
// and some report queries take no parameters to add it to.
func tagRoomFilter(roomCol, tag string) string {
	if tag == "" {
		return ""
	}
	return " AND " + roomCol + " IN (SELECT room_id FROM room_tag_assignments WHERE tag_id = '" + tag + "')"
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
	tag, valid := reportTag(w, r)
	if !valid {
		return
	}

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
	out, ok := cachedReport(reportKey(r, "device-uptime|"+tag, days), func() ([]row, bool) {
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
				  COALESCE(SUM(t.samples), 0)::int AS samples,
				  COALESCE(SUM(t.online_samples), 0)::int AS online_samples,
				  CASE WHEN SUM(t.samples) > 0
				    THEN ROUND(100.0 * SUM(t.online_samples) / SUM(t.samples), 2)
				    ELSE NULL END AS uptime_pct,
				  ` + devicestatus.EffectiveStatusSQL + ` AS dev_current_status,
				  d.last_seen_at
				FROM devices d
				LEFT JOIN rooms r ON r.id = d.room_id
				LEFT JOIN buildings b ON b.id = r.building_id
				LEFT JOIN collectors c ON c.id = d.collector_id
				-- Daily summaries (internal/telemetry), whole UTC days.
				LEFT JOIN telemetry_daily t ON t.device_id = d.id
				      AND t.day >= ((SELECT since FROM win) AT TIME ZONE 'UTC')::date
				WHERE d.deleted_at IS NULL`+tagRoomFilter("d.room_id", tag)+`
				GROUP BY d.id, d.name, d.reported_id, r.name, b.name, d.latest_status, d.last_seen_at, c.last_seen_at, c.serving_seen_at
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
		return out, ok
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

// DeviceEventsReport — GET /api/v1/reports/device-events?days=N[&format=csv]
//
// Per room: how many events its devices raised over the window, split into
// alerts (offline, faults…) and device messages (unprompted Telnet, serial
// and WebSocket output, DSP subscription updates, call notifications).
// Normal polling isn't an event, so a high count points at a noisy or
// faulty device, not a busy room; room use is RoomUsageReport. Rooms with
// none still appear.
func (h *Handler) DeviceEventsReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"
	tag, valid := reportTag(w, r)
	if !valid {
		return
	}

	type row struct {
		RoomID       string     `json:"room_id"`
		RoomName     string     `json:"room_name"`
		BuildingName string     `json:"building_name"`
		DeviceCount  int        `json:"device_count"`
		EventCount   int        `json:"event_count"`
		AlertCount   int        `json:"alert_count"`
		MessageCount int        `json:"message_count"`
		LastEventAt  *time.Time `json:"last_event_at,omitempty"`
	}
	out := []row{}

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH ev AS (
			    SELECT d.room_id,
			           COUNT(*)                                            AS events,
			           COUNT(*) FILTER (WHERE e.event_type LIKE 'alert:%') AS alerts,
			           MAX(e.ts)                                           AS last_ts
			      FROM events e
			      JOIN devices d ON d.id = e.device_id
			     WHERE e.ts >= now() - make_interval(days => $1)
			       AND d.room_id IS NOT NULL
			     GROUP BY d.room_id
			)
			SELECT r.id::text, r.name, COALESCE(b.name, ''),
			       (SELECT COUNT(*) FROM devices d WHERE d.room_id = r.id AND d.deleted_at IS NULL)::int,
			       COALESCE(ev.events, 0)::int,
			       COALESCE(ev.alerts, 0)::int,
			       ev.last_ts
			  FROM rooms r
			  LEFT JOIN buildings b ON b.id = r.building_id
			  LEFT JOIN ev ON ev.room_id = r.id
			 WHERE true`+tagRoomFilter("r.id", tag)+`
			 ORDER BY COALESCE(ev.events, 0) DESC, r.name`, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName,
				&rr.DeviceCount, &rr.EventCount, &rr.AlertCount, &rr.LastEventAt); err != nil {
				return err
			}
			rr.MessageCount = rr.EventCount - rr.AlertCount
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("device-events-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"room_id", "room_name", "building_name",
			"device_count", "event_count", "alert_count", "message_count", "last_event_at"})
		for _, rr := range out {
			lastEvent := ""
			if rr.LastEventAt != nil {
				lastEvent = rr.LastEventAt.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{
				rr.RoomID, rr.RoomName, rr.BuildingName,
				strconv.Itoa(rr.DeviceCount), strconv.Itoa(rr.EventCount),
				strconv.Itoa(rr.AlertCount), strconv.Itoa(rr.MessageCount), lastEvent,
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
	tag, valid := reportTag(w, r)
	if !valid {
		return
	}

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
			 WHERE a.status <> 'retired'`+tagRoomFilter("a.room_id", tag)+`
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

// RoomUsageReport — GET /api/v1/reports/room-usage?days=N[&format=csv]
//
// How much each room is used, from the readings its devices report every
// poll, as summarised per device and UTC day (internal/telemetry). Windows
// are whole UTC days. Results are shared for reportCacheTTL
// (report_cache.go). A room is in use for any clock hour in which one of its devices
// reported a call (active_calls > 0, Cisco and Poly codecs) or people in
// the room (people_count > 0, Cisco with a camera that counts them).
//
// Display power isn't counted as use: Room Readiness switches displays on
// every morning, so they read "on" whether anyone comes in or not. It's
// reported separately as display_on_hours, leaving out hours a Room
// Readiness run was working on the room. Rooms with no device that reports
// calls or people have has_usage_signal false, so the portal can say "no
// usage data" rather than show an idle room.
func (h *Handler) RoomUsageReport(w http.ResponseWriter, r *http.Request) {
	days := reportDays(r)
	wantCSV := r.URL.Query().Get("format") == "csv"
	tag, valid := reportTag(w, r)
	if !valid {
		return
	}

	type row struct {
		RoomID         string  `json:"room_id"`
		RoomName       string  `json:"room_name"`
		BuildingName   string  `json:"building_name"`
		DeviceCount    int     `json:"device_count"`
		HasUsageSignal bool    `json:"has_usage_signal"`
		HoursInUse     int     `json:"hours_in_use"`
		HoursInCalls   int     `json:"hours_in_calls"`
		HoursOccupied  int     `json:"hours_occupied"`
		DaysInUse      int     `json:"days_in_use"`
		AvgHoursPerDay float64 `json:"avg_hours_per_day"`
		PeakPeople     *int    `json:"peak_people"`
		DisplayOnHours int     `json:"display_on_hours"`
	}
	out, ok := cachedReport(reportKey(r, "room-usage|"+tag, days), func() ([]row, bool) {
		out := []row{}
		ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				WITH since AS (SELECT ((now() - make_interval(days => $1)) AT TIME ZONE 'UTC')::date AS day),
				room_days AS (
				    -- Daily summaries (internal/telemetry): per device and UTC
				    -- day, 24-bit masks of the hours with a call, people, or a
				    -- display on. OR'd per room so each hour counts once.
				    SELECT d.room_id, td.day,
				           bit_or(td.call_hours)       AS calls,
				           bit_or(td.occupied_hours)   AS occupied,
				           bit_or(td.display_on_hours) AS display,
				           bool_or(td.usage_signal)    AS signal,
				           MAX(td.peak_people)         AS peak_people
				      FROM telemetry_daily td
				      JOIN devices d ON d.id = td.device_id
				     WHERE td.day >= (SELECT day FROM since)
				       AND d.room_id IS NOT NULL AND d.deleted_at IS NULL
				     GROUP BY d.room_id, td.day
				),
				run_hours AS (
				    -- Hours a Room Readiness run was working on the room, left
				    -- out of display-on time.
				    SELECT nr.room_id, h::date AS day,
				           bit_or(1 << extract(hour FROM h)::int) AS mask
				      FROM nightly_run nr,
				           generate_series(date_trunc('hour', nr.started_at AT TIME ZONE 'UTC'),
				                           date_trunc('hour', COALESCE(nr.completed_at, now()) AT TIME ZONE 'UTC'),
				                           interval '1 hour') AS h
				     WHERE nr.started_at IS NOT NULL
				       AND COALESCE(nr.completed_at, now()) >= (SELECT day FROM since)
				     GROUP BY nr.room_id, h::date
				),
				per_room AS (
				    SELECT rd.room_id,
				           bool_or(rd.signal)                                            AS signal,
				           SUM(bit_count((rd.calls | rd.occupied)::bit(24)))             AS in_use,
				           SUM(bit_count(rd.calls::bit(24)))                             AS in_calls,
				           SUM(bit_count(rd.occupied::bit(24)))                          AS occupied,
				           COUNT(*) FILTER (WHERE (rd.calls | rd.occupied) <> 0)         AS days_in_use,
				           MAX(rd.peak_people)                                           AS peak_people,
				           SUM(bit_count((rd.display & ~COALESCE(rh.mask, 0))::bit(24))) AS display_on
				      FROM room_days rd
				      LEFT JOIN run_hours rh ON rh.room_id = rd.room_id AND rh.day = rd.day
				     GROUP BY rd.room_id
				)
				SELECT r.id::text, r.name, COALESCE(b.name, ''),
				       (SELECT COUNT(*) FROM devices d WHERE d.room_id = r.id AND d.deleted_at IS NULL)::int,
				       COALESCE(p.signal, false),
				       COALESCE(p.in_use, 0)::int,
				       COALESCE(p.in_calls, 0)::int,
				       COALESCE(p.occupied, 0)::int,
				       COALESCE(p.days_in_use, 0)::int,
				       ROUND(COALESCE(p.in_use, 0)::numeric / $1, 2)::float8,
				       p.peak_people::int,
				       COALESCE(p.display_on, 0)::int
				  FROM rooms r
				  LEFT JOIN buildings b ON b.id = r.building_id
				  LEFT JOIN per_room p ON p.room_id = r.id
				 WHERE true`+tagRoomFilter("r.id", tag)+`
				 ORDER BY COALESCE(p.signal, false) DESC, COALESCE(p.in_use, 0) DESC, r.name`, days)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var rr row
				if err := rows.Scan(&rr.RoomID, &rr.RoomName, &rr.BuildingName, &rr.DeviceCount,
					&rr.HasUsageSignal, &rr.HoursInUse, &rr.HoursInCalls, &rr.HoursOccupied,
					&rr.DaysInUse, &rr.AvgHoursPerDay, &rr.PeakPeople, &rr.DisplayOnHours); err != nil {
					return err
				}
				out = append(out, rr)
			}
			return rows.Err()
		})
		return out, ok
	})
	if !ok {
		return
	}

	if wantCSV {
		filename := fmt.Sprintf("room-usage-%dd-%s.csv", days, time.Now().UTC().Format("20060102"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"room_id", "room_name", "building_name", "device_count",
			"has_usage_signal", "hours_in_use", "hours_in_calls", "hours_occupied",
			"days_in_use", "avg_hours_per_day", "peak_people", "display_on_hours"})
		for _, rr := range out {
			peak := ""
			if rr.PeakPeople != nil {
				peak = strconv.Itoa(*rr.PeakPeople)
			}
			_ = cw.Write([]string{
				rr.RoomID, rr.RoomName, rr.BuildingName, strconv.Itoa(rr.DeviceCount),
				strconv.FormatBool(rr.HasUsageSignal),
				strconv.Itoa(rr.HoursInUse), strconv.Itoa(rr.HoursInCalls), strconv.Itoa(rr.HoursOccupied),
				strconv.Itoa(rr.DaysInUse), strconv.FormatFloat(rr.AvgHoursPerDay, 'f', 2, 64),
				peak, strconv.Itoa(rr.DisplayOnHours),
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
	tag, valid := reportTag(w, r)
	if !valid {
		return
	}

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
			           COALESCE(SUM(t.samples), 0)::numeric        AS samples,
			           COALESCE(SUM(t.online_samples), 0)::numeric AS online_samples
			      FROM room_devices rd
			      LEFT JOIN telemetry_daily t
			             ON t.device_id = rd.device_id
			            AND t.day >= ((SELECT since FROM win) AT TIME ZONE 'UTC')::date
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
			 WHERE true`+tagRoomFilter("r.id", tag)+`
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
