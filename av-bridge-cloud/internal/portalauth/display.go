package portalauth

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DisplayTokenPrefix marks a display link's token (dashboard wall screens).
const DisplayTokenPrefix = "dsp_"

// DisplayPermissions is everything a display can do: read what dashboard
// widgets show. No writes, and no audit log (it names people).
var DisplayPermissions = map[string]struct{}{
	PermViewDashboard: {},
	PermViewReports:   {},
	PermViewFirmware:  {},
	PermNightlyView:   {},
}

// DisplayScope is a display link's copy of its creator's physical scope.
type DisplayScope struct {
	BusinessUnits []string `json:"business_units,omitempty"`
	Regions       []string `json:"regions,omitempty"`
	Locations     []string `json:"locations,omitempty"`
	Buildings     []string `json:"buildings,omitempty"`
	Rooms         []string `json:"rooms,omitempty"`
}

// ScopeOf is the physical scope a display link created by p should carry:
// p's own, or none for vendor callers (who act unscoped).
func ScopeOf(p Principal) DisplayScope {
	if p.IsVendor {
		return DisplayScope{}
	}
	return DisplayScope{
		BusinessUnits: p.BusinessUnitScopeIDs,
		Regions:       p.RegionScopeIDs,
		Locations:     p.LocationScopeIDs,
		Buildings:     p.BuildingScopeIDs,
		Rooms:         p.RoomScopeIDs,
	}
}

// DisplayResolver signs in display links (see migration 0059): a revoked
// link stops resolving at once.
type DisplayResolver struct {
	pool *pgxpool.Pool
}

func NewDisplayResolver(pool *pgxpool.Pool) *DisplayResolver {
	return &DisplayResolver{pool: pool}
}

func (d *DisplayResolver) Resolve(token string) (Principal, bool) {
	if !strings.HasPrefix(token, DisplayTokenPrefix) {
		return Principal{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var (
		id, customerID, name string
		dashboards           []string
		scopeRaw             []byte
	)
	err := d.pool.QueryRow(ctx, `
		UPDATE dashboard_display_links
		   SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at < now() - interval '5 minutes'
		                           THEN now() ELSE last_used_at END
		 WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING id::text, customer_id::text, name, dashboard_ids::text[], scope`,
		HashToken(token)).Scan(&id, &customerID, &name, &dashboards, &scopeRaw)
	if err != nil {
		return Principal{}, false
	}
	var scope DisplayScope
	_ = json.Unmarshal(scopeRaw, &scope)
	return Principal{
		CustomerID:           customerID,
		Name:                 name,
		Role:                 "display",
		Permissions:          DisplayPermissions,
		DisplayLinkID:        id,
		DisplayDashboardIDs:  dashboards,
		BusinessUnitScopeIDs: scope.BusinessUnits,
		RegionScopeIDs:       scope.Regions,
		LocationScopeIDs:     scope.Locations,
		BuildingScopeIDs:     scope.Buildings,
		RoomScopeIDs:         scope.Rooms,
	}, true
}
