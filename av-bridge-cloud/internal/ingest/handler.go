package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/auth"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
	"github.com/jackc/pgx/v5"
)

const maxBody = 8 << 20 // 8 MiB

// Wire DTOs — redeclared (not imported from the bridge module) so the cloud
// service stays decoupled from device adapters. Field tags must match the
// bridge's cloud.Payload / device.Telemetry / device.Event JSON exactly.

type telemetryDTO struct {
	DeviceID     string            `json:"device_id"`
	DeviceName   string            `json:"device_name"`
	DeviceType   string            `json:"device_type"`
	Location     string            `json:"location"`
	Protocol     string            `json:"protocol"`
	Status       string            `json:"status"`
	Timestamp    time.Time         `json:"timestamp"`
	Metrics      map[string]any    `json:"metrics"`
	LensMetrics  map[string]any    `json:"lens_metrics"`
	Tags         map[string]string `json:"tags"`
	Error        string            `json:"error"`
	// Capabilities is the adapter's static declaration of what this
	// device supports (power, commands, metrics). Persisted as-is to
	// devices.capabilities so the portal + routine executor can gate
	// authoring + validate targets. Nullable: transport-only adapters
	// omit it; the writer stores NULL and dependent features degrade
	// gracefully ("cannot power-cycle; skip in lifecycle").
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type eventDTO struct {
	DeviceID   string         `json:"device_id"`
	DeviceName string         `json:"device_name"`
	DeviceType string         `json:"device_type"`
	EventType  string         `json:"event_type"`
	Payload    map[string]any `json:"payload"`
	Timestamp  time.Time      `json:"timestamp"`
}

type payloadDTO struct {
	Source      string         `json:"source"`
	Timestamp   time.Time      `json:"timestamp"`
	CollectorID string         `json:"collector_id"`
	SiteID      string         `json:"site_id"`
	// BridgeVersion + BridgeBuildTime are what the bridge reports about its
	// own binary — set from -ldflags at build. Empty on older bridges;
	// MarkCollectorSeen preserves prior values when empty.
	BridgeVersion   string         `json:"bridge_version,omitempty"`
	BridgeBuildTime string         `json:"bridge_build_time,omitempty"`
	Telemetry       []telemetryDTO `json:"telemetry"`
	Events          []eventDTO     `json:"events"`
}

type Handler struct {
	store      *db.Store
	cipher     secrets.Cipher
	hub        *wsfanout.Hub
	dispatcher *notify.Dispatcher
	log        *slog.Logger
}

// NewHandler. hub and dispatcher may be nil — if so, events still persist
// but no live fan-out and no outbound notifications happen. Handler
// degrades gracefully so both side-effects are optional.
func NewHandler(store *db.Store, cipher secrets.Cipher, hub *wsfanout.Hub, dispatcher *notify.Dispatcher, log *slog.Logger) *Handler {
	return &Handler{store: store, cipher: cipher, hub: hub, dispatcher: dispatcher, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read body")
		return
	}

	var p payloadDTO
	if err := json.Unmarshal(body, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.CollectorID == "" {
		writeErr(w, http.StatusBadRequest, "collector_id is required")
		return
	}

	ctx := r.Context()

	// 1. Privileged lookup (cross-tenant): resolve the collector + its secret.
	col, err := h.store.LookupCollectorByBridgeID(ctx, p.CollectorID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Don't reveal whether the collector exists.
		writeErr(w, http.StatusUnauthorized, "authentication failed")
		return
	} else if err != nil {
		h.log.Error("collector lookup failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	secret, err := h.cipher.Decrypt(col.SecretEnc)
	if err != nil {
		h.log.Error("secret decrypt failed", "collector", col.ID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !auth.VerifySignature(string(secret), r.Header.Get("X-Signature"), body) {
		writeErr(w, http.StatusUnauthorized, "authentication failed")
		return
	}
	if err := h.store.MarkCollectorSeen(ctx, col.ID, p.BridgeVersion, p.BridgeBuildTime); err != nil {
		h.log.Warn("mark collector seen failed", "collector", col.ID, "error", err)
	}

	// 2. Tenant-scoped writes (RLS-enforced as app_tenant).
	// Collect newly-opened alerts so the dispatcher can fan them out to
	// configured channels after the tx commits (don't dispatch on rollback).
	var newAlerts []notify.AlertEvent
	err = h.store.WithTenant(ctx, col.CustomerID, func(tx pgx.Tx) error {
		newAlerts = newAlerts[:0]
		deviceIDs := make(map[string]string, len(p.Telemetry))

		for _, t := range p.Telemetry {
			devID, err := upsertDeviceFromTelemetry(ctx, tx, col.CustomerID, col.ID, t)
			if err != nil {
				return err
			}
			deviceIDs[t.DeviceID] = devID
			if err := insertTelemetry(ctx, tx, col.CustomerID, devID, t); err != nil {
				return err
			}
		}

		for _, e := range p.Events {
			devID, ok := deviceIDs[e.DeviceID]
			if !ok {
				var err error
				devID, err = upsertDeviceMinimal(ctx, tx, col.CustomerID, col.ID, e.DeviceID, e.DeviceName, e.DeviceType)
				if err != nil {
					return err
				}
				deviceIDs[e.DeviceID] = devID
			}
			if err := insertEvent(ctx, tx, col.CustomerID, devID, e); err != nil {
				return err
			}
			// Side-effect of an alert event: persist (or update) the alert row
			// in the dedicated alerts table so the portal can show actionable
			// state. Recovery events auto-resolve still-open alerts for the
			// same device. Non-alert events are ignored here.
			outcome, err := handleAlertEvent(ctx, tx, col.CustomerID, devID, e)
			if err != nil {
				return err
			}
			if outcome.notifyEvent != nil {
				newAlerts = append(newAlerts, *outcome.notifyEvent)
			}
		}

		_, err := tx.Exec(ctx,
			`UPDATE collectors SET last_seen_at = now(), status = 'online' WHERE id = $1`, col.ID)
		return err
	})
	if err != nil {
		h.log.Error("ingest write failed", "collector", col.ID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Live fan-out happens after persist so a rolled-back tx never produces
	// phantom WS frames. Loop is cheap when no subscribers — Publish is a
	// read-locked map lookup with an empty result.
	if h.hub != nil {
		for _, e := range p.Events {
			h.hub.Publish(col.CustomerID, wsfanout.Event{
				DeviceID:   e.DeviceID,
				DeviceName: e.DeviceName,
				DeviceType: e.DeviceType,
				EventType:  e.EventType,
				Payload:    e.Payload,
				Timestamp:  e.Timestamp,
			})
		}
	}

	// Outbound notifications — only for alerts that just opened (re-fires of
	// an already-open alert don't notify, per the alertOutcome logic). Each
	// Dispatch call is non-blocking; the dispatcher manages its own
	// goroutines + per-channel timeouts.
	if h.dispatcher != nil {
		for i := range newAlerts {
			h.dispatcher.Dispatch(newAlerts[i])
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":  true,
		"telemetry": len(p.Telemetry),
		"events":    len(p.Events),
	})
}

const upsertDeviceSQL = `
INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol,
                     make, model, serial_number, firmware_version, mac_address, ip_address,
                     tags, latest_status, latest_metrics, last_seen_at, capabilities)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15::jsonb,$16,$17::jsonb)
ON CONFLICT (collector_id, reported_id) DO UPDATE SET
  name             = COALESCE(EXCLUDED.name, devices.name),
  type             = COALESCE(EXCLUDED.type, devices.type),
  protocol         = COALESCE(EXCLUDED.protocol, devices.protocol),
  make             = COALESCE(EXCLUDED.make, devices.make),
  model            = COALESCE(EXCLUDED.model, devices.model),
  serial_number    = COALESCE(EXCLUDED.serial_number, devices.serial_number),
  firmware_version = COALESCE(EXCLUDED.firmware_version, devices.firmware_version),
  mac_address      = COALESCE(EXCLUDED.mac_address, devices.mac_address),
  ip_address       = COALESCE(EXCLUDED.ip_address, devices.ip_address),
  tags             = COALESCE(EXCLUDED.tags, devices.tags),
  latest_status    = COALESCE(EXCLUDED.latest_status, devices.latest_status),
  latest_metrics   = COALESCE(EXCLUDED.latest_metrics, devices.latest_metrics),
  last_seen_at     = COALESCE(EXCLUDED.last_seen_at, devices.last_seen_at),
  capabilities     = COALESCE(EXCLUDED.capabilities, devices.capabilities)
RETURNING id::text`

func upsertDeviceFromTelemetry(ctx context.Context, tx pgx.Tx, customerID, collectorID string, t telemetryDTO) (string, error) {
	var id string
	err := tx.QueryRow(ctx, upsertDeviceSQL,
		customerID,
		collectorID,
		t.DeviceID,
		nilIfEmpty(t.DeviceName),
		nilIfEmpty(t.DeviceType),
		nilIfEmpty(t.Protocol),
		// Tag-supplied make/model take priority (customer can override via YAML/portal),
		// then fall back to what the adapter emits via metrics/lens_metrics. Poly, for
		// example, gets its make/model from Lens (manufacturer + hardware_model) rather
		// than being set as tags on the device row.
		nilIfEmpty(firstNonEmpty(t.Tags["make"], pick(t.LensMetrics, t.Metrics, "manufacturer", "make"))),
		nilIfEmpty(firstNonEmpty(t.Tags["model"], pick(t.LensMetrics, t.Metrics, "hardware_model", "model", "product_model"))),
		nilIfEmpty(pick(t.LensMetrics, t.Metrics, "serial_number", "serial")),
		// software_version covers Poly / Cisco / other vendors that report firmware
		// under a "software" naming; firmware_version and sw_version stay for the
		// tesira / display drivers that use those keys.
		nilIfEmpty(pick(t.Metrics, t.LensMetrics, "firmware_version", "firmware", "sw_version", "software_version", "software_build")),
		nilIfEmpty(pick(t.LensMetrics, t.Metrics, "mac_address", "mac")),
		nilIfEmpty(firstNonEmpty(t.Tags["ip_address"], pick(t.Metrics, t.LensMetrics, "ip_address", "ip"))),
		jsonbStringMap(t.Tags),
		nilIfEmpty(t.Status),
		jsonbMap(t.Metrics),
		nilIfZeroTime(t.Timestamp),
		nilIfEmptyRaw(t.Capabilities),
	).Scan(&id)
	return id, err
}

func upsertDeviceMinimal(ctx context.Context, tx pgx.Tx, customerID, collectorID, reportedID, name, typ string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, upsertDeviceSQL,
		customerID, collectorID, reportedID,
		nilIfEmpty(name), nilIfEmpty(typ), nil,
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
	).Scan(&id)
	return id, err
}

func insertTelemetry(ctx context.Context, tx pgx.Tx, customerID, deviceID string, t telemetryDTO) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO telemetry (customer_id, device_id, status, metrics, error, ts)
		 VALUES ($1,$2,$3,$4::jsonb,$5,$6)`,
		customerID, deviceID, nilIfEmpty(t.Status), jsonbMap(t.Metrics), nilIfEmpty(t.Error), t.Timestamp)
	return err
}

func insertEvent(ctx context.Context, tx pgx.Tx, customerID, deviceID string, e eventDTO) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO events (customer_id, device_id, event_type, payload, ts)
		 VALUES ($1,$2,$3,$4::jsonb,$5)`,
		customerID, deviceID, nilIfEmpty(e.EventType), jsonbMap(e.Payload), e.Timestamp)
	return err
}

// ---- helpers ----

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZeroTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nilIfEmptyRaw treats a zero-length or JSON-null RawMessage as SQL
// NULL — otherwise passes the bytes through as-is. Used for the
// capabilities column so transport-only adapters (which don't declare
// capabilities) don't stomp existing values with an empty payload.
func nilIfEmptyRaw(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	// Trim whitespace-ish payloads and treat literal "null" as nil.
	// Any non-null JSON — including "{}" — passes through so callers
	// can deliberately clear individual capability sub-fields by
	// sending an object with missing keys.
	if string(b) == "null" {
		return nil
	}
	return []byte(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// pick returns the first key found across the given maps, stringified.
func pick(primary, secondary map[string]any, keys ...string) string {
	for _, m := range []map[string]any{primary, secondary} {
		for _, k := range keys {
			if v, ok := m[k]; ok && v != nil {
				if s, ok := v.(string); ok {
					return s
				}
				return toString(v)
			}
		}
	}
	return ""
}

func toString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	// strip surrounding quotes for plain scalars
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// jsonbMap returns a JSON string for a map (cast ::jsonb in SQL), or nil.
func jsonbMap(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

func jsonbStringMap(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
