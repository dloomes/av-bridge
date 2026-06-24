package portalapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

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
			  COALESCE(d.latest_status, 'unknown') AS dev_current_status,
			  d.last_seen_at
			FROM devices d
			LEFT JOIN rooms r ON r.id = d.room_id
			LEFT JOIN buildings b ON b.id = r.building_id
			LEFT JOIN telemetry t ON t.device_id = d.id AND t.ts >= (SELECT since FROM win)
			GROUP BY d.id, d.name, d.reported_id, r.name, b.name, d.latest_status, d.last_seen_at
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
			LEFT JOIN devices d ON d.room_id = r.id
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
