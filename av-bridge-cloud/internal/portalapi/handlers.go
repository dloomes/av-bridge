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
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	store  *db.Store
	cipher secrets.Cipher
	log    *slog.Logger
}

func New(store *db.Store, cipher secrets.Cipher, log *slog.Logger) *Handler {
	return &Handler{store: store, cipher: cipher, log: log}
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
	err := h.store.WithTenant(r.Context(), p.CustomerID, func(tx pgx.Tx) error {
		return fn(r.Context(), tx)
	})
	if err != nil {
		h.log.Error("portal query failed", "path", r.URL.Path, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return false
	}
	return true
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
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, COALESCE(bridge_collector_id,''), name, status, last_seen_at
			   FROM collectors ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c item
			if err := rows.Scan(&c.ID, &c.BridgeCollectorID, &c.Name, &c.Status, &c.LastSeenAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListDevices — GET /api/v1/devices. Shape matches the bridge's DeviceSummary.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID       string            `json:"id"`
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Protocol string            `json:"protocol"`
		Location string            `json:"location"`
		Address  string            `json:"address,omitempty"`
		Status   string            `json:"status"`
		Tags     map[string]string `json:"tags,omitempty"`
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
			       COALESCE(d.ip_address, ''),
			       COALESCE(d.latest_status, 'unknown'),
			       d.tags
			  FROM devices d
			  LEFT JOIN rooms r ON r.id = d.room_id
			  LEFT JOIN buildings b ON b.id = r.building_id
			 ORDER BY d.name NULLS LAST, d.reported_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			var tags []byte
			if err := rows.Scan(&it.ID, &it.Name, &it.Type, &it.Protocol, &it.Location, &it.Address, &it.Status, &tags); err != nil {
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
			Scan(&o.ID, &o.CollectorID, &roomID, &o.ReportedID,
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
