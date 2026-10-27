package pubapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// publicFirmwareRow is one firmware version of one make and model, and
// how many devices run it.
type publicFirmwareRow struct {
	Make            string `json:"make"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version"`
	TargetFirmware  string `json:"target_firmware_version,omitempty"`
	FirmwareStatus  string `json:"firmware_status"`
	DeviceCount     int    `json:"device_count"`
}

// ListFirmware — GET /pub/v1/firmware
//
// The fleet's firmware: one row per make, model and firmware version,
// with how many devices run it and how it compares with the target set
// for that make and model on the Firmware page. The same make, model and
// firmware as the Firmware page and the device endpoints.
//
// Filters (optional): building_id, room_id, firmware_status.
// Requires the view.firmware scope. Not paginated: one row per version.
func (h *Handler) ListFirmware(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	building := strings.TrimSpace(q.Get("building_id"))
	room := strings.TrimSpace(q.Get("room_id"))
	status := strings.TrimSpace(q.Get("firmware_status"))
	if status != "" && !firmwareStatuses[status] {
		writeErr(w, http.StatusBadRequest, "firmware_status must be matches_target, differs_from_target, no_target or not_reported")
		return
	}

	sql := `SELECT fw.make, fw.model, fw.firmware, COALESCE(ft.target_version, ''),
	               ` + firmwareStatusSQL + `, count(*)::int` + publicDeviceFrom + `
	         WHERE d.deleted_at IS NULL`
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}
	if building != "" {
		sql += " AND r.building_id::text = " + arg(building)
	}
	if room != "" {
		sql += " AND d.room_id::text = " + arg(room)
	}
	if status != "" {
		sql += " AND (" + firmwareStatusSQL + ") = " + arg(status)
	}
	sql += ` GROUP BY fw.make, fw.model, fw.firmware, ft.target_version
	         ORDER BY fw.make, fw.model, count(*) DESC, fw.firmware`

	out := []publicFirmwareRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f publicFirmwareRow
			if err := rows.Scan(&f.Make, &f.Model, &f.FirmwareVersion, &f.TargetFirmware, &f.FirmwareStatus, &f.DeviceCount); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
