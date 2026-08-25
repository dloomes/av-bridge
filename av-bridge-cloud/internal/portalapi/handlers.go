// Package portalapi implements the cloud-side read API the web portal calls.
// Every handler runs through Store.WithTenant so tenant isolation is enforced
// by RLS regardless of auth source. JSON shapes match the bridge's existing
// /api/v1/... contract so the portal can be re-pointed without code changes.
package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/nightly"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	store      *db.Store
	cipher     secrets.Cipher
	dispatcher *notify.Dispatcher
	digest     *nightly.DigestSender
	executor   *nightly.Executor
	log        *slog.Logger
}

// New. dispatcher may be nil — the notification channel endpoints still
// serve reads and CRUD but the test-send endpoint returns 503. digest may
// be nil — the nightly send-now endpoint returns 503 in that case.
// executor may be nil or disabled — the run-now endpoint returns 503.
func New(store *db.Store, cipher secrets.Cipher, dispatcher *notify.Dispatcher, digest *nightly.DigestSender, executor *nightly.Executor, log *slog.Logger) *Handler {
	return &Handler{store: store, cipher: cipher, dispatcher: dispatcher, digest: digest, executor: executor, log: log}
}

// withTenant runs fn under the caller's customer scope. On error it writes a
// 500 itself and returns false; on success returns true and the handler writes
// its own response. The closure may return pgx.ErrNoRows freely — that maps to
// the handler's own "not found" handling below; only unexpected errors are
// treated as 500.
func (h *Handler) withTenant(w http.ResponseWriter, r *http.Request, fn func(context.Context, pgx.Tx) error) bool {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no principal in context")
		return false
	}
	// Vendor callers bypass physical scope by design — they act as
	// unscoped admins inside whichever customer they're currently
	// acting-as. Non-vendor callers honour their user's building
	// restriction: empty slice = full tenant, non-empty = only rows
	// hanging off those buildings (enforced by migration 0019's
	// RESTRICTIVE RLS policies on devices/telemetry/events/alerts/
	// commands).
	scope := p.BuildingScopeIDs
	if p.IsVendor {
		scope = nil
	}
	err := h.store.WithTenantScoped(r.Context(), p.CustomerID, scope, func(tx pgx.Tx) error {
		return fn(r.Context(), tx)
	})
	if err != nil {
		h.log.Error("portal query failed", "path", r.URL.Path, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return false
	}
	return true
}

// principalScope returns the effective physical-scope for a Principal:
// nil (unscoped) for vendor callers, the user's building_scope_ids
// otherwise. Handlers calling store.WithTenantScoped directly (audit
// writes after a non-tenant operation, for example) should route their
// scope choice through here so the vendor-bypass rule stays in one place.
func principalScope(p portalauth.Principal) []string {
	if p.IsVendor {
		return nil
	}
	return p.BuildingScopeIDs
}

// stampActor fills the actor-context fields on an audit.Entry from the
// caller's Principal so every audit row carries a snapshot of the
// authorization state at write time (role, physical scope, vendor flag).
// Roles and scope drift; the row is frozen. Handlers should compose their
// entry then pass it through this before audit.Record.
//
// A nil BuildingScopeIDs is normalized to an empty slice: it distinguishes
// "explicitly unscoped / full-tenant" (stored as {}) from the pre-slice-7
// legacy where the column is NULL because the row was written before this
// field existed.
func stampActor(p portalauth.Principal, e audit.Entry) audit.Entry {
	if e.Actor == "" {
		e.Actor = p.ActorLabel()
	}
	e.ActorRole = p.Role
	if p.BuildingScopeIDs == nil {
		e.ActorScope = []string{}
	} else {
		e.ActorScope = p.BuildingScopeIDs
	}
	e.ActorIsVendor = p.IsVendor
	return e
}

func queryInt(r *http.Request, key string, def, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// ---- handlers ----------------------------------------------------------------

// Status — GET /api/v1/status
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	type out struct {
		Total    int    `json:"total"`
		Online   int    `json:"online"`
		Offline  int    `json:"offline"`
		Degraded int    `json:"degraded"`
		Time     string `json:"time"`
	}
	var o out
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			  count(*),
			  count(*) FILTER (WHERE latest_status = 'online'),
			  count(*) FILTER (WHERE latest_status = 'offline'),
			  count(*) FILTER (WHERE latest_status = 'degraded')
			FROM devices
			WHERE deleted_at IS NULL`).Scan(&o.Total, &o.Online, &o.Offline, &o.Degraded)
	})
	if !ok {
		return
	}
	o.Time = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, o)
}

// Collector health thresholds. Bridges poll every ~30s by default, so a
// 2-minute gap is the first signal something's off, and 5 minutes is enough
// for a power blip or restart to recover from.
const (
	collectorDegradedAfter = 2 * time.Minute
	collectorOfflineAfter  = 5 * time.Minute
)

// ListCollectors — GET /api/v1/collectors
//
// Returns one row per collector visible under RLS, joined against the
// collector's building for context and with a device count so the /collectors
// fleet page can render without a second round-trip. Ordered so ops sees the
// broken ones first: offline → degraded → unknown → online, then by name.
func (h *Handler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID                string     `json:"id"`
		BridgeCollectorID string     `json:"bridge_collector_id"`
		Name              string     `json:"name"`
		BuildingName      string     `json:"building_name,omitempty"`
		Status            string     `json:"status"`
		LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
		DeviceCount       int        `json:"device_count"`
		BridgeVersion     string     `json:"bridge_version,omitempty"`
		BridgeBuildTime   *time.Time `json:"bridge_build_time,omitempty"`
		LastConfigPullAt  *time.Time `json:"last_config_pull_at,omitempty"`
		ConfigSyncStatus  string     `json:"config_sync_status"`
	}
	out := []item{}
	now := time.Now()
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.id::text,
			       COALESCE(c.bridge_collector_id, ''),
			       c.name,
			       COALESCE(b.name, ''),
			       c.last_seen_at,
			       (SELECT count(*) FROM devices d WHERE d.collector_id = c.id AND d.deleted_at IS NULL) AS device_count,
			       COALESCE(c.bridge_version, ''),
			       c.bridge_build_time,
			       c.last_config_pull_at
			  FROM collectors c
			  LEFT JOIN buildings b ON b.id = c.building_id
			 ORDER BY c.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c item
			if err := rows.Scan(&c.ID, &c.BridgeCollectorID, &c.Name, &c.BuildingName,
				&c.LastSeenAt, &c.DeviceCount,
				&c.BridgeVersion, &c.BridgeBuildTime, &c.LastConfigPullAt,
			); err != nil {
				return err
			}
			c.Status = computeCollectorStatus(c.LastSeenAt, now)
			c.ConfigSyncStatus = computeConfigSyncStatus(c.LastConfigPullAt, now)
			out = append(out, c)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	// Sort ops-first: broken collectors bubble to the top. SQL ORDER BY
	// alone can't do this cleanly because status is derived in Go from
	// last_seen_at + wall clock, not stored on the row.
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := statusPriority(out[i].Status), statusPriority(out[j].Status)
		if si != sj {
			return si < sj
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

// statusPriority orders collector statuses for the /collectors table so the
// worst state floats to the top. Lower number = higher priority.
func statusPriority(status string) int {
	switch status {
	case "offline":
		return 0
	case "degraded":
		return 1
	case "unknown":
		return 2
	default:
		return 3
	}
}

func computeCollectorStatus(lastSeen *time.Time, now time.Time) string {
	if lastSeen == nil {
		return "unknown"
	}
	age := now.Sub(*lastSeen)
	switch {
	case age >= collectorOfflineAfter:
		return "offline"
	case age >= collectorDegradedAfter:
		return "degraded"
	default:
		return "online"
	}
}

// Config-pull freshness threshold. Bridges reconcile config on a
// device_sync_interval tick (default 5m), so 15m without a pull means the
// bridge is either stopped or unable to reach the cloud even though it
// might still have pushed stale telemetry from its in-memory queue.
const configPullStaleAfter = 15 * time.Minute

func computeConfigSyncStatus(lastPull *time.Time, now time.Time) string {
	if lastPull == nil {
		return "unknown"
	}
	if now.Sub(*lastPull) >= configPullStaleAfter {
		return "stale"
	}
	return "current"
}

// ListDevices — GET /api/v1/devices. Shape matches the bridge's DeviceSummary.
//
// room_id is included so callers (e.g. the locations page's delete-impact
// preview) can group devices by their placement without an extra fetch.
//
// region / location_name / building expose the full hierarchy so the portal
// can breadcrumb it in group headers. Location (the existing "Building / Room"
// display string) is kept for back-compat with the bridge's own DeviceSummary
// contract.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	// Optional filter: ?collector_id=<uuid>. Empty string means "all
	// devices". Validated only loosely — a garbage value returns an empty
	// list rather than 400, which is friendlier for /devices deep-links.
	collectorFilter := strings.TrimSpace(r.URL.Query().Get("collector_id"))

	type item struct {
		ID           string            `json:"id"`
		Name         string            `json:"name"`
		Type         string            `json:"type"`
		Protocol     string            `json:"protocol"`
		Location     string            `json:"location"`
		Region       string            `json:"region,omitempty"`
		LocationName string            `json:"location_name,omitempty"`
		Building     string            `json:"building,omitempty"`
		RoomID       *string           `json:"room_id,omitempty"`
		CollectorID  string            `json:"collector_id,omitempty"`
		Address      string            `json:"address,omitempty"`
		Status       string            `json:"status"`
		Tags         map[string]string `json:"tags,omitempty"`
		// Capabilities is the adapter-declared shape (power/commands/metrics)
		// stored on the devices row via the ingest handler. Included in the
		// listing so the routine builder's palette can gate step types
		// against what's actually available in the picked room.
		Capabilities json.RawMessage `json:"capabilities,omitempty"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		sql := `
			SELECT d.id::text,
			       COALESCE(d.name, d.reported_id, ''),
			       COALESCE(d.type, ''), COALESCE(d.protocol, ''),
			       CASE
			         WHEN b.name IS NOT NULL AND r.name IS NOT NULL THEN b.name || ' / ' || r.name
			         WHEN r.name IS NOT NULL THEN r.name
			         ELSE ''
			       END,
			       COALESCE(reg.name, ''),
			       COALESCE(loc.name, ''),
			       COALESCE(b.name, ''),
			       d.room_id::text,
			       d.collector_id::text,
			       COALESCE(d.ip_address, ''),
			       COALESCE(d.latest_status, 'unknown'),
			       d.tags,
			       d.capabilities
			  FROM devices d
			  LEFT JOIN rooms r      ON r.id   = d.room_id
			  LEFT JOIN buildings b  ON b.id   = r.building_id
			  LEFT JOIN locations loc ON loc.id = b.location_id
			  LEFT JOIN regions reg  ON reg.id = loc.region_id
			 WHERE d.deleted_at IS NULL`
		args := []any{}
		if collectorFilter != "" {
			args = append(args, collectorFilter)
			sql += " AND d.collector_id::text = $1"
		}
		sql += " ORDER BY d.name NULLS LAST, d.reported_id"

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			var tags []byte
			var caps []byte
			if err := rows.Scan(&it.ID, &it.Name, &it.Type, &it.Protocol,
				&it.Location, &it.Region, &it.LocationName, &it.Building,
				&it.RoomID, &it.CollectorID, &it.Address, &it.Status, &tags, &caps); err != nil {
				return err
			}
			if len(tags) > 0 {
				_ = json.Unmarshal(tags, &it.Tags)
			}
			if len(caps) > 0 && string(caps) != "null" {
				it.Capabilities = json.RawMessage(caps)
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetDevice — GET /api/v1/devices/{id}
//
// Returns the full editable config so the portal's edit form can render with
// current values. Credentials (username/password) are intentionally omitted —
// they're write-only via PATCH; the form treats them as "leave blank to keep
// current".
func (h *Handler) GetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	type subscription struct {
		Tag       string `json:"tag"`
		Attribute string `json:"attribute"`
		Channel   int    `json:"channel"`
		Label     string `json:"label"`
		Rate      int    `json:"rate,omitempty"`
	}
	// deviceAsset mirrors the standard asset fields, embedded so the edit
	// form can pre-fill the Physical inventory section without a follow-
	// up /assets fetch.
	type deviceAsset struct {
		AssetTag     string `json:"asset_tag,omitempty"`
		Category     string `json:"category,omitempty"`
		Manufacturer string `json:"manufacturer,omitempty"`
		Model        string `json:"model,omitempty"`
		SerialNumber string `json:"serial_number,omitempty"`
		Status       string `json:"status,omitempty"`
		PurchaseDate string `json:"purchase_date,omitempty"`
		WarrantyEnd  string `json:"warranty_end,omitempty"`
		Notes        string `json:"notes,omitempty"`
	}
	type out struct {
		ID          string            `json:"id"`
		CollectorID string            `json:"collector_id"`
		RoomID      *string           `json:"room_id,omitempty"`
		AssetID     *string           `json:"asset_id,omitempty"`
		Asset       *deviceAsset      `json:"asset,omitempty"`
		ReportedID  string            `json:"reported_id"`
		Name        string            `json:"name"`
		Type        string            `json:"type"`
		Protocol    string            `json:"protocol"`
		Location    string            `json:"location"`
		// Address mirrors the address column (the config-pull truth) so the
		// edit form sees what the bridge sees. The old GET used ip_address
		// (an inventory column populated from telemetry) — kept the column
		// separately exposed below as "ip_address" for back-compat.
		Address          string            `json:"address,omitempty"`
		IPAddress        string            `json:"ip_address,omitempty"`
		BaudRate         int               `json:"baud_rate,omitempty"`
		PollRate         int               `json:"poll_rate_seconds,omitempty"`
		Status           string            `json:"status"`
		Tags             map[string]string `json:"tags,omitempty"`
		Commands         map[string]string `json:"commands,omitempty"`
		Subscriptions    []subscription    `json:"subscriptions,omitempty"`
		// Capabilities is the adapter-declared shape (power/commands/metrics)
		// stored on the row by the ingest handler. Surfaced so the device
		// detail page's Command panel can render the correct buttons for
		// the adapter in use, rather than falling back to category defaults.
		Capabilities json.RawMessage `json:"capabilities,omitempty"`
	}
	var (
		o        out
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var (
			tags, cmds, subs, caps []byte
			roomID                 *string
			baudRate               *int
			pollRate               *int
		)
		err := tx.QueryRow(ctx, `
			SELECT d.id::text,
			       d.collector_id::text,
			       d.room_id::text,
			       d.asset_id::text,
			       COALESCE(d.reported_id, ''),
			       COALESCE(d.name, d.reported_id, ''),
			       COALESCE(d.type, ''), COALESCE(d.protocol, ''),
			       CASE
			         WHEN b.name IS NOT NULL AND r.name IS NOT NULL THEN b.name || ' / ' || r.name
			         WHEN r.name IS NOT NULL THEN r.name
			         ELSE ''
			       END,
			       COALESCE(d.address, ''),
			       COALESCE(d.ip_address, ''),
			       d.baud_rate,
			       d.poll_rate_seconds,
			       COALESCE(d.latest_status, 'unknown'),
			       d.tags, d.commands, d.subscriptions, d.capabilities
			  FROM devices d
			  LEFT JOIN rooms r ON r.id = d.room_id
			  LEFT JOIN buildings b ON b.id = r.building_id
			 WHERE d.id = $1 AND d.deleted_at IS NULL`, id).
			Scan(&o.ID, &o.CollectorID, &roomID, &o.AssetID, &o.ReportedID,
				&o.Name, &o.Type, &o.Protocol, &o.Location,
				&o.Address, &o.IPAddress, &baudRate, &pollRate,
				&o.Status, &tags, &cmds, &subs, &caps)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		o.RoomID = roomID
		if baudRate != nil {
			o.BaudRate = *baudRate
		}
		if pollRate != nil {
			o.PollRate = *pollRate
		}
		if len(caps) > 0 && string(caps) != "null" {
			o.Capabilities = json.RawMessage(caps)
		}
		// If the device is linked to a CMDB asset, embed the standard
		// fields so the edit form's Physical inventory section can
		// pre-fill in one round-trip. Missing (device has no asset_id)
		// leaves the field omitted via the omitempty tag.
		if o.AssetID != nil && *o.AssetID != "" {
			var (
				assetTag     *string
				category     string
				manufacturer *string
				model        *string
				serial       *string
				assetStatus  string
				purchase     *time.Time
				warranty     *time.Time
				notes        *string
			)
			if err := tx.QueryRow(ctx, `
				SELECT asset_tag, category, manufacturer, model, serial_number,
				       status, purchase_date, warranty_end, notes
				  FROM assets WHERE id = $1`, *o.AssetID).
				Scan(&assetTag, &category, &manufacturer, &model, &serial,
					&assetStatus, &purchase, &warranty, &notes); err == nil {
				a := deviceAsset{Category: category, Status: assetStatus}
				if assetTag != nil {
					a.AssetTag = *assetTag
				}
				if manufacturer != nil {
					a.Manufacturer = *manufacturer
				}
				if model != nil {
					a.Model = *model
				}
				if serial != nil {
					a.SerialNumber = *serial
				}
				if purchase != nil {
					a.PurchaseDate = purchase.Format("2006-01-02")
				}
				if warranty != nil {
					a.WarrantyEnd = warranty.Format("2006-01-02")
				}
				if notes != nil {
					a.Notes = *notes
				}
				o.Asset = &a
			}
			// Silent failure on the asset lookup — the device row still
			// renders; the operator just doesn't see the pre-filled
			// inventory section. Better than 500-ing the whole page.
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &o.Tags)
		}
		if len(cmds) > 0 {
			_ = json.Unmarshal(cmds, &o.Commands)
		}
		if len(subs) > 0 {
			_ = json.Unmarshal(subs, &o.Subscriptions)
		}
		return nil
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// GetTelemetry — GET /api/v1/devices/{id}/telemetry
// Returns the latest persisted snapshot shaped like the bridge's Telemetry.
// Slice 2 is read-only — there's no live re-poll.
func (h *Handler) GetTelemetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	type out struct {
		DeviceID    string          `json:"device_id"`
		DeviceName  string          `json:"device_name"`
		DeviceType  string          `json:"device_type"`
		Location    string          `json:"location,omitempty"`
		Protocol    string          `json:"protocol"`
		Status      string          `json:"status"`
		Timestamp   *time.Time      `json:"timestamp"`
		Metrics     json.RawMessage `json:"metrics,omitempty"`
		LensMetrics json.RawMessage `json:"lens_metrics,omitempty"`
		Tags        json.RawMessage `json:"tags,omitempty"`
		Error       string          `json:"error,omitempty"`
	}
	var (
		o        out
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT d.id::text,
			       COALESCE(d.name, d.reported_id, ''),
			       COALESCE(d.type, ''), COALESCE(d.protocol, ''),
			       CASE
			         WHEN b.name IS NOT NULL AND r.name IS NOT NULL THEN b.name || ' / ' || r.name
			         WHEN r.name IS NOT NULL THEN r.name
			         ELSE ''
			       END,
			       COALESCE(d.latest_status, 'unknown'),
			       d.last_seen_at, d.latest_metrics, d.tags
			  FROM devices d
			  LEFT JOIN rooms r ON r.id = d.room_id
			  LEFT JOIN buildings b ON b.id = r.building_id
			 WHERE d.id = $1 AND d.deleted_at IS NULL`, id).
			Scan(&o.DeviceID, &o.DeviceName, &o.DeviceType, &o.Protocol, &o.Location, &o.Status, &o.Timestamp, &o.Metrics, &o.Tags)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		return err
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// TelemetryHistory — GET /api/v1/devices/{id}/telemetry/history?limit=N
func (h *Handler) TelemetryHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := queryInt(r, "limit", 50, 1000)
	type row struct {
		Status  string          `json:"status"`
		Metrics json.RawMessage `json:"metrics,omitempty"`
		Error   string          `json:"error,omitempty"`
		Ts      time.Time       `json:"ts"`
	}
	out := []row{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT COALESCE(status,''), metrics, COALESCE(error,''), ts
			  FROM telemetry WHERE device_id = $1 ORDER BY ts DESC LIMIT $2`, id, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.Status, &rr.Metrics, &rr.Error, &rr.Ts); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeviceEvents — GET /api/v1/devices/{id}/events?limit=N
//
// Returns the last N events emitted by a specific device, most recent
// first. Powers the "Recent events" history panel on the device detail
// page. Shape matches the top-level ListEvents response (same device_id/
// device_name/device_type/event_type/payload/timestamp fields) so the
// portal can render both feeds through the same row component.
//
// Historical view — unlike the WebSocket /ws/events feed which only
// pushes events as they arrive, this endpoint hits the events table
// directly. Runs under RLS so cross-tenant leakage is impossible.
func (h *Handler) DeviceEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := queryInt(r, "limit", 50, 500)
	type item struct {
		DeviceID   string          `json:"device_id"`
		DeviceName string          `json:"device_name"`
		DeviceType string          `json:"device_type"`
		EventType  string          `json:"event_type"`
		Payload    json.RawMessage `json:"payload,omitempty"`
		Timestamp  time.Time       `json:"timestamp"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id::text,
			       COALESCE(d.name, d.reported_id, ''),
			       COALESCE(d.type, ''),
			       COALESCE(e.event_type, ''),
			       e.payload, e.ts
			  FROM events e JOIN devices d ON d.id = e.device_id
			 WHERE e.device_id = $1 AND d.deleted_at IS NULL
			 ORDER BY e.ts DESC LIMIT $2`, id, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.DeviceID, &it.DeviceName, &it.DeviceType, &it.EventType, &it.Payload, &it.Timestamp); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListEvents — GET /api/v1/events?limit=N
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100, 1000)
	type item struct {
		DeviceID   string          `json:"device_id"`
		DeviceName string          `json:"device_name"`
		DeviceType string          `json:"device_type"`
		EventType  string          `json:"event_type"`
		Payload    json.RawMessage `json:"payload,omitempty"`
		Timestamp  time.Time       `json:"timestamp"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id::text,
			       COALESCE(d.name, d.reported_id, ''),
			       COALESCE(d.type, ''),
			       COALESCE(e.event_type, ''),
			       e.payload, e.ts
			  FROM events e JOIN devices d ON d.id = e.device_id
			 WHERE d.deleted_at IS NULL
			 ORDER BY e.ts DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.DeviceID, &it.DeviceName, &it.DeviceType, &it.EventType, &it.Payload, &it.Timestamp); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAudit — GET /api/v1/audit?limit=N&target_kind=device&target_id=<uuid>
//
// Returns recent audit entries for the caller's tenant, most recent first.
// Filtering: target_kind + target_id let the portal show a per-target
// history pane (e.g. "every change to this device"). All filters are
// optional; without them you get the customer-wide feed.
//
// Any authenticated portal user can read their own tenant's audit — this is
// transparency, not privilege escalation. Sensitive columns
// (username_enc / password_enc) are never in the snapshots in the first
// place (excluded at write time, see audit.SnapshotDevice).
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100, 500)
	targetKind := r.URL.Query().Get("target_kind")
	targetID := r.URL.Query().Get("target_id")

	type entry struct {
		ID         int64           `json:"id"`
		Actor      string          `json:"actor"`
		Action     string          `json:"action"`
		TargetKind string          `json:"target_kind"`
		TargetID   string          `json:"target_id,omitempty"`
		Before     json.RawMessage `json:"before,omitempty"`
		After      json.RawMessage `json:"after,omitempty"`
		Metadata   json.RawMessage `json:"metadata,omitempty"`
		Ts         time.Time       `json:"ts"`
	}
	out := []entry{}

	// Build the query with optional filters. Always tenant-scoped via RLS;
	// no need to repeat customer_id in the WHERE.
	//
	// target_id filter matches either target_id OR related_target_id so a
	// per-device feed picks up entries where the device is the secondary
	// subject (today: command.submit). target_kind, when supplied, applies
	// to whichever side the id matched on.
	sql := `SELECT id, actor, action, target_kind, COALESCE(target_id,''),
	               before, "after", metadata, ts
	          FROM audit_log`
	conds := []string{}
	args := []any{}
	if targetID != "" {
		args = append(args, targetID)
		idIdx := strconv.Itoa(len(args))
		if targetKind != "" {
			args = append(args, targetKind)
			kindIdx := strconv.Itoa(len(args))
			conds = append(conds,
				"((target_id = $"+idIdx+" AND target_kind = $"+kindIdx+")"+
					" OR (related_target_id = $"+idIdx+" AND related_target_kind = $"+kindIdx+"))")
		} else {
			conds = append(conds,
				"(target_id = $"+idIdx+" OR related_target_id = $"+idIdx+")")
		}
	} else if targetKind != "" {
		args = append(args, targetKind)
		conds = append(conds, "target_kind = $"+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	sql += " ORDER BY ts DESC LIMIT $" + strconv.Itoa(len(args))

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.TargetKind, &e.TargetID,
				&e.Before, &e.After, &e.Metadata, &e.Ts); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAlerts — GET /api/v1/alerts?status=open&limit=N
//
// Returns alerts for the caller's tenant, most recently opened first.
// `status` filters to open / acknowledged / resolved; omit for all states.
// Acknowledged + resolved share a recency bias too — useful for a "what
// happened recently" view alongside the live open list.
func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100, 500)
	status := r.URL.Query().Get("status")

	type item struct {
		ID              string          `json:"id"`
		SubjectKind     string          `json:"subject_kind"` // "device" | "collector"
		DeviceID        string          `json:"device_id,omitempty"`
		DeviceName      string          `json:"device_name,omitempty"`
		CollectorID     string          `json:"collector_id,omitempty"`
		CollectorName   string          `json:"collector_name,omitempty"`
		AlertKey        string          `json:"alert_key"`
		Severity        string          `json:"severity"`
		Message         string          `json:"message"`
		Payload         json.RawMessage `json:"payload,omitempty"`
		Status          string          `json:"status"`
		OpenedAt        time.Time       `json:"opened_at"`
		AcknowledgedAt  *time.Time      `json:"acknowledged_at,omitempty"`
		AcknowledgedBy  string          `json:"acknowledged_by,omitempty"`
		ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
		ResolvedBy      string          `json:"resolved_by,omitempty"`
	}
	out := []item{}

	// LEFT JOIN both device and collector tables so alerts of either
	// subject appear. CASE picks the subject kind based on which column
	// is populated — the CHECK constraint guarantees exactly one is set.
	sql := `SELECT a.id::text,
	               CASE WHEN a.collector_id IS NOT NULL THEN 'collector' ELSE 'device' END AS subject_kind,
	               COALESCE(a.device_id::text, ''),
	               COALESCE(d.name, d.reported_id, ''),
	               COALESCE(a.collector_id::text, ''),
	               COALESCE(c.name, ''),
	               a.alert_key, a.severity, a.message, a.payload, a.status,
	               a.opened_at, a.acknowledged_at, COALESCE(a.acknowledged_by,''),
	               a.resolved_at, COALESCE(a.resolved_by,'')
	          FROM alerts a
	          LEFT JOIN devices d ON d.id = a.device_id
	          LEFT JOIN collectors c ON c.id = a.collector_id`
	args := []any{}
	if status != "" {
		args = append(args, status)
		sql += " WHERE a.status = $1"
	}
	args = append(args, limit)
	sql += " ORDER BY a.opened_at DESC LIMIT $" + strconv.Itoa(len(args))

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.ID, &it.SubjectKind,
				&it.DeviceID, &it.DeviceName,
				&it.CollectorID, &it.CollectorName,
				&it.AlertKey, &it.Severity, &it.Message, &it.Payload, &it.Status,
				&it.OpenedAt, &it.AcknowledgedAt, &it.AcknowledgedBy,
				&it.ResolvedAt, &it.ResolvedBy); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AlertsSummary — GET /api/v1/alerts/summary
//
// Cheap counts for header badges. Returns open / acknowledged totals so the
// dashboard can show an at-a-glance indicator without paging through the
// full list. Resolved is omitted — it grows without bound and isn't useful
// as a count.
func (h *Handler) AlertsSummary(w http.ResponseWriter, r *http.Request) {
	type out struct {
		Open         int `json:"open"`
		Acknowledged int `json:"acknowledged"`
		Critical     int `json:"critical_open"`
	}
	var o out
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			  COUNT(*) FILTER (WHERE status = 'open'),
			  COUNT(*) FILTER (WHERE status = 'acknowledged'),
			  COUNT(*) FILTER (WHERE status = 'open' AND severity = 'critical')
			FROM alerts`).Scan(&o.Open, &o.Acknowledged, &o.Critical)
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// AcknowledgeAlert — POST /api/v1/alerts/{id}/acknowledge
// Marks an open alert as acknowledged by the caller. Idempotent — a 200 is
// returned even if the alert was already acknowledged (the row's actor +
// timestamp stay on the first ack).
func (h *Handler) AcknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts
			   SET status          = 'acknowledged',
			       acknowledged_at = now(),
			       acknowledged_by = $2
			 WHERE id = $1
			   AND status = 'open'`,
			id, p.Role)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		return nil
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		// Either not found, not visible to this tenant, or already past "open".
		writeErr(w, http.StatusNotFound, "alert not found or not open")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "acknowledged"})
}

// ResolveAlert — POST /api/v1/alerts/{id}/resolve
// Manually closes an alert. Use for cases where the bridge can't auto-resolve
// (e.g. a "transient_glitch" event that doesn't have a matching recovery).
func (h *Handler) ResolveAlert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := portalauth.From(r.Context())

	var rowsAffected int64
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE alerts
			   SET status      = 'resolved',
			       resolved_at = now(),
			       resolved_by = $2
			 WHERE id = $1
			   AND status IN ('open','acknowledged')`,
			id, p.Role)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		return nil
	})
	if !ok {
		return
	}
	if rowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "alert not found or already resolved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "resolved"})
}

// Whoami — GET /api/v1/whoami
//
// Returns the resolved Principal so the portal can show the signed-in user
// and gate UI to their role. Empty fields are omitted from the JSON to keep
// the response readable for the legacy static-token path (which has no
// email/name).
func (h *Handler) Whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	type out struct {
		UserID           string   `json:"user_id,omitempty"`
		Email            string   `json:"email,omitempty"`
		Name             string   `json:"name,omitempty"`
		CustomerID       string   `json:"customer_id,omitempty"`
		Role             string   `json:"role"`
		IsVendor         bool     `json:"is_vendor,omitempty"`
		Permissions      []string `json:"permissions"`
		BuildingScopeIDs []string `json:"building_scope_ids"`
		LandingPage      string   `json:"landing_page"`
	}
	// Portal uses this list to gate UI (show/hide buttons) — vendor bypass
	// still applies server-side, but for the UI we surface the effective
	// permission set including "all" for vendor calls so the portal
	// doesn't have to encode the bypass rule twice.
	perms := make([]string, 0, len(p.Permissions))
	if p.IsVendor {
		for k := range portalauth.KnownPermissions {
			perms = append(perms, k)
		}
	} else {
		for k := range p.Permissions {
			perms = append(perms, k)
		}
	}
	// Callers key off length: [] means unscoped (full tenant), non-empty means
	// restricted to those buildings. json marshalling of a nil slice would
	// emit null, breaking JS `scope.length`.
	scope := p.BuildingScopeIDs
	if scope == nil {
		scope = []string{}
	}
	// Preference lookup is best-effort — a mock-JWT dev user has no users
	// row so we default to "overview" rather than failing whoami.
	landingPage := "overview"
	_ = h.store.AdminPool().QueryRow(r.Context(),
		`SELECT landing_page FROM users WHERE id::text = $1`, p.UserID).Scan(&landingPage)

	writeJSON(w, http.StatusOK, out{
		UserID:           p.UserID,
		Email:            p.Email,
		Name:             p.Name,
		CustomerID:       p.CustomerID,
		Role:             p.Role,
		IsVendor:         p.IsVendor,
		Permissions:      perms,
		BuildingScopeIDs: scope,
		LandingPage:      landingPage,
	})
}

// UpdateMyPreferences — PATCH /api/v1/me/preferences
//
// Currently only the landing_page preference is exposed. Body is a partial
// update: any omitted field is left alone. Whitelisted values are enforced
// here in addition to the CHECK constraint so an invalid value returns 400
// instead of a database error.
func (h *Handler) UpdateMyPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	var req struct {
		LandingPage *string `json:"landing_page,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.LandingPage == nil {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}
	if *req.LandingPage != "overview" && *req.LandingPage != "map" {
		writeErr(w, http.StatusBadRequest, "landing_page must be 'overview' or 'map'")
		return
	}
	tag, err := h.store.AdminPool().Exec(r.Context(),
		`UPDATE users SET landing_page = $1 WHERE id::text = $2`,
		*req.LandingPage, p.UserID)
	if err != nil {
		h.log.Error("update preferences", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		// Mock-JWT dev users have no row — pretend success so the UI toggle
		// still updates the local session even though nothing was persisted.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"landing_page": *req.LandingPage})
}

// HelpdeskOverview — GET /api/v1/helpdesk/overview
//
// Per-customer rollups for the helpdesk landing page. Single query with
// LEFT JOIN aggregates so an empty customer (no devices / no alerts /
// no collectors) still shows up with zeros, not omitted.
//
// Vendor-only — handlers.go wires it behind RequireVendor at route mount
// time. Bypasses RLS (admin pool) because by definition this is a cross-
// tenant view.
func (h *Handler) HelpdeskOverview(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID              string     `json:"id"`
		Name            string     `json:"name"`
		EntraTenantID   string     `json:"entra_tenant_id,omitempty"`
		Slug            string     `json:"slug,omitempty"`
		DevicesTotal    int        `json:"devices_total"`
		DevicesOnline   int        `json:"devices_online"`
		DevicesOffline  int        `json:"devices_offline"`
		DevicesDegraded int        `json:"devices_degraded"`
		AlertsOpen      int        `json:"alerts_open"`
		AlertsCritical  int        `json:"alerts_critical"`
		CollectorsTotal int        `json:"collectors_total"`
		LastBridgeSeen  *time.Time `json:"last_bridge_seen,omitempty"`
	}
	out := []item{}

	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT
		  c.id::text,
		  c.name,
		  COALESCE(c.entra_tenant_id,''),
		  COALESCE(c.slug,''),
		  COALESCE(d.total, 0),
		  COALESCE(d.online, 0),
		  COALESCE(d.offline, 0),
		  COALESCE(d.degraded, 0),
		  COALESCE(a.open, 0),
		  COALESCE(a.critical, 0),
		  COALESCE(col.total, 0),
		  col.last_seen
		FROM customers c
		LEFT JOIN (
		  SELECT customer_id,
		    COUNT(*)::int AS total,
		    COUNT(*) FILTER (WHERE latest_status = 'online')::int AS online,
		    COUNT(*) FILTER (WHERE latest_status = 'offline')::int AS offline,
		    COUNT(*) FILTER (WHERE latest_status = 'degraded')::int AS degraded
		  FROM devices WHERE deleted_at IS NULL GROUP BY customer_id
		) d ON d.customer_id = c.id
		LEFT JOIN (
		  SELECT customer_id,
		    COUNT(*) FILTER (WHERE status = 'open')::int AS open,
		    COUNT(*) FILTER (WHERE status = 'open' AND severity = 'critical')::int AS critical
		  FROM alerts GROUP BY customer_id
		) a ON a.customer_id = c.id
		LEFT JOIN (
		  SELECT customer_id,
		    COUNT(*)::int AS total,
		    MAX(last_seen_at) AS last_seen
		  FROM collectors GROUP BY customer_id
		) col ON col.customer_id = c.id
		ORDER BY c.name`)
	if err != nil {
		h.log.Error("helpdesk overview query", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.EntraTenantID, &it.Slug,
			&it.DevicesTotal, &it.DevicesOnline, &it.DevicesOffline, &it.DevicesDegraded,
			&it.AlertsOpen, &it.AlertsCritical,
			&it.CollectorsTotal, &it.LastBridgeSeen); err != nil {
			h.log.Error("helpdesk overview scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		h.log.Error("helpdesk overview rows", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HelpdeskListCustomers — GET /api/v1/helpdesk/customers
//
// Vendor-only cross-tenant list of every customer. Used by the helpdesk UI
// so support staff can pick which customer to act as via the X-Customer-Scope
// header. Reads via the admin pool because by definition this call is
// unscoped (the principal is a vendor without a CustomerID).
func (h *Handler) HelpdeskListCustomers(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		EntraTenantID string `json:"entra_tenant_id,omitempty"`
	}
	out := []item{}
	rows, err := h.store.AdminPool().Query(r.Context(),
		`SELECT id::text, name, COALESCE(entra_tenant_id,'') FROM customers ORDER BY name`)
	if err != nil {
		h.log.Error("helpdesk customers list", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.EntraTenantID); err != nil {
			h.log.Error("helpdesk customers scan", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

// NotImplemented is the 501 stub for command/reconnect endpoints in Slice 2.
// Cloud-mediated control is Slice 3.
func NotImplemented(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"not yet wired through cloud — use the bridge's local API directly for now"}`))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
