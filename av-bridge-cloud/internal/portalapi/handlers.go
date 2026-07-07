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
	"strconv"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	store      *db.Store
	cipher     secrets.Cipher
	dispatcher *notify.Dispatcher
	log        *slog.Logger
}

// New. dispatcher may be nil — the notification channel endpoints still
// serve reads and CRUD but the test-send endpoint returns 503.
func New(store *db.Store, cipher secrets.Cipher, dispatcher *notify.Dispatcher, log *slog.Logger) *Handler {
	return &Handler{store: store, cipher: cipher, dispatcher: dispatcher, log: log}
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
			FROM devices`).Scan(&o.Total, &o.Online, &o.Offline, &o.Degraded)
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
func (h *Handler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID                string     `json:"id"`
		BridgeCollectorID string     `json:"bridge_collector_id"`
		Name              string     `json:"name"`
		Status            string     `json:"status"`
		LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	}
	out := []item{}
	now := time.Now()
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, COALESCE(bridge_collector_id,''), name, last_seen_at
			   FROM collectors ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c item
			if err := rows.Scan(&c.ID, &c.BridgeCollectorID, &c.Name, &c.LastSeenAt); err != nil {
				return err
			}
			c.Status = computeCollectorStatus(c.LastSeenAt, now)
			out = append(out, c)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
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
		Address      string            `json:"address,omitempty"`
		Status       string            `json:"status"`
		Tags         map[string]string `json:"tags,omitempty"`
	}
	out := []item{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
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
			       COALESCE(d.ip_address, ''),
			       COALESCE(d.latest_status, 'unknown'),
			       d.tags
			  FROM devices d
			  LEFT JOIN rooms r      ON r.id   = d.room_id
			  LEFT JOIN buildings b  ON b.id   = r.building_id
			  LEFT JOIN locations loc ON loc.id = b.location_id
			  LEFT JOIN regions reg  ON reg.id = loc.region_id
			 ORDER BY d.name NULLS LAST, d.reported_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			var tags []byte
			if err := rows.Scan(&it.ID, &it.Name, &it.Type, &it.Protocol,
				&it.Location, &it.Region, &it.LocationName, &it.Building,
				&it.RoomID, &it.Address, &it.Status, &tags); err != nil {
				return err
			}
			if len(tags) > 0 {
				_ = json.Unmarshal(tags, &it.Tags)
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
	type out struct {
		ID          string            `json:"id"`
		CollectorID string            `json:"collector_id"`
		RoomID      *string           `json:"room_id,omitempty"`
		AssetID     *string           `json:"asset_id,omitempty"`
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
	}
	var (
		o        out
		notFound bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var (
			tags, cmds, subs []byte
			roomID           *string
			baudRate         *int
			pollRate         *int
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
			       d.tags, d.commands, d.subscriptions
			  FROM devices d
			  LEFT JOIN rooms r ON r.id = d.room_id
			  LEFT JOIN buildings b ON b.id = r.building_id
			 WHERE d.id = $1`, id).
			Scan(&o.ID, &o.CollectorID, &roomID, &o.AssetID, &o.ReportedID,
				&o.Name, &o.Type, &o.Protocol, &o.Location,
				&o.Address, &o.IPAddress, &baudRate, &pollRate,
				&o.Status, &tags, &cmds, &subs)
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
			 WHERE d.id = $1`, id).
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
		DeviceID        string          `json:"device_id"`
		DeviceName      string          `json:"device_name"`
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

	sql := `SELECT a.id::text, a.device_id::text,
	               COALESCE(d.name, d.reported_id, ''),
	               a.alert_key, a.severity, a.message, a.payload, a.status,
	               a.opened_at, a.acknowledged_at, COALESCE(a.acknowledged_by,''),
	               a.resolved_at, COALESCE(a.resolved_by,'')
	          FROM alerts a
	          JOIN devices d ON d.id = a.device_id`
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
			if err := rows.Scan(&it.ID, &it.DeviceID, &it.DeviceName,
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
	writeJSON(w, http.StatusOK, out{
		UserID:           p.UserID,
		Email:            p.Email,
		Name:             p.Name,
		CustomerID:       p.CustomerID,
		Role:             p.Role,
		IsVendor:         p.IsVendor,
		Permissions:      perms,
		BuildingScopeIDs: scope,
	})
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
		ID               string     `json:"id"`
		Name             string     `json:"name"`
		EntraTenantID    string     `json:"entra_tenant_id,omitempty"`
		DevicesTotal     int        `json:"devices_total"`
		DevicesOnline    int        `json:"devices_online"`
		DevicesOffline   int        `json:"devices_offline"`
		DevicesDegraded  int        `json:"devices_degraded"`
		AlertsOpen       int        `json:"alerts_open"`
		AlertsCritical   int        `json:"alerts_critical"`
		CollectorsTotal  int        `json:"collectors_total"`
		LastBridgeSeen   *time.Time `json:"last_bridge_seen,omitempty"`
	}
	out := []item{}

	rows, err := h.store.AdminPool().Query(r.Context(), `
		SELECT
		  c.id::text,
		  c.name,
		  COALESCE(c.entra_tenant_id,''),
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
		  FROM devices GROUP BY customer_id
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
		if err := rows.Scan(&it.ID, &it.Name, &it.EntraTenantID,
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
