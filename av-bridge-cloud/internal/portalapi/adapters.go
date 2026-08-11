package portalapi

import (
	"context"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/adapters"
	"github.com/jackc/pgx/v5"
)

// ListAdapters — GET /api/v1/adapters
//
// Returns the static adapter catalogue (see internal/adapters/catalogue.go),
// enriched with a per-adapter count of how many devices in this tenant are
// currently configured to use it. The portal renders this as an /adapters
// page — one card per adapter, "N devices" pill sourced from device_count.
//
// The count join runs under RLS so cross-tenant leakage is impossible even
// if a handler-level bug were to serve the wrong scope. Adapters with zero
// devices still appear (this endpoint is a catalogue, not an inventory).
func (h *Handler) ListAdapters(w http.ResponseWriter, r *http.Request) {
	cat := adapters.Catalogue()

	counts := map[string]int{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT protocol, COUNT(*)::int
			  FROM devices
			 WHERE protocol IS NOT NULL AND protocol <> ''
			 GROUP BY protocol`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var proto string
			var n int
			if err := rows.Scan(&proto, &n); err != nil {
				return err
			}
			counts[proto] = n
		}
		return rows.Err()
	})
	if !ok {
		return
	}

	for i := range cat {
		cat[i].DeviceCount = counts[cat[i].ID]
	}

	writeJSON(w, http.StatusOK, cat)
}
