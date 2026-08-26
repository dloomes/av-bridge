package pubapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Physical hierarchy: buildings + rooms. Regions + locations are
// omitted from v1 — most integrators care about the two innermost
// layers, and adding them later is additive. If a customer wants the
// full tree, we can layer /pub/v1/regions and /pub/v1/locations onto
// the same shape without breaking existing calls.
//
// These endpoints do NOT paginate: a customer typically has tens to
// hundreds of buildings/rooms — well below the point where cursoring
// pays for itself. If a large tenant blows past that in future,
// switching to cursor pagination is a one-line addition to the
// existing pattern.

type publicBuilding struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Address      string   `json:"address,omitempty"`
	Timezone     string   `json:"timezone,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	LocationName string   `json:"location,omitempty"`
	RegionName   string   `json:"region,omitempty"`
}

type publicRoom struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BuildingID   string `json:"building_id"`
	BuildingName string `json:"building"`
}

// ListBuildings — GET /pub/v1/buildings
//
// Requires view.dashboard. Returns every building in the tenant with
// address + timezone + optional coords. Location + region names come
// along so a caller can group by the wider hierarchy without a
// follow-up call.
func (h *Handler) ListBuildings(w http.ResponseWriter, r *http.Request) {
	out := []publicBuilding{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id::text,
			       b.name,
			       COALESCE(b.address, ''),
			       COALESCE(b.timezone, ''),
			       b.latitude,
			       b.longitude,
			       COALESCE(l.name, ''),
			       COALESCE(reg.name, '')
			  FROM buildings b
			  LEFT JOIN locations l  ON l.id = b.location_id
			  LEFT JOIN regions reg  ON reg.id = l.region_id
			 ORDER BY b.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b publicBuilding
			if err := rows.Scan(
				&b.ID, &b.Name, &b.Address, &b.Timezone,
				&b.Latitude, &b.Longitude, &b.LocationName, &b.RegionName,
			); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListRooms — GET /pub/v1/rooms
//
// Requires view.dashboard. Filter: ?building_id=<uuid>. Every room
// carries its parent building's id + name so a caller building a
// picker or CMDB sync can render "Building - Room" without a join
// on the client side.
func (h *Handler) ListRooms(w http.ResponseWriter, r *http.Request) {
	buildingFilter := r.URL.Query().Get("building_id")

	out := []publicRoom{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		sql := `
			SELECT r.id::text,
			       r.name,
			       r.building_id::text,
			       COALESCE(b.name, '')
			  FROM rooms r
			  LEFT JOIN buildings b ON b.id = r.building_id`
		args := []any{}
		if buildingFilter != "" {
			args = append(args, buildingFilter)
			sql += " WHERE r.building_id::text = $1"
		}
		sql += " ORDER BY b.name, r.name"

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rm publicRoom
			if err := rows.Scan(&rm.ID, &rm.Name, &rm.BuildingID, &rm.BuildingName); err != nil {
				return err
			}
			out = append(out, rm)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
