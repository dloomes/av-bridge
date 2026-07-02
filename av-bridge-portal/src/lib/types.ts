export type DeviceStatus = "online" | "offline" | "degraded" | "unknown";

export type DeviceType = "display" | "conferencing" | "audio" | "camera" | "control";

export interface DeviceSummary {
  id: string;
  name: string;
  type: DeviceType;
  protocol: string;
  location: string;
  room_id?: string | null;
  address?: string;
  status: DeviceStatus;
  tags?: Record<string, string>;
}

export interface Subscription {
  tag: string;
  attribute: string;
  channel: number;
  label: string;
  rate?: number;
}

// DeviceDetail returns the full editable config (creds excluded — write-only).
// The fields beyond DeviceSummary are what the edit form needs to pre-fill.
export interface DeviceDetail extends DeviceSummary {
  collector_id: string;
  room_id?: string | null;
  reported_id: string;
  ip_address?: string;
  baud_rate?: number;
  poll_rate_seconds?: number;
  commands?: Record<string, string>;
  subscriptions?: Subscription[];
}

// Wire shape for POST /api/v1/devices — collector_id + reported_id required.
export interface CreateDeviceBody {
  collector_id: string;
  reported_id: string;
  name?: string;
  type?: string;
  protocol?: string;
  address?: string;
  baud_rate?: number;
  username?: string;
  password?: string;
  poll_rate_seconds?: number;
  commands?: Record<string, string>;
  tags?: Record<string, string>;
  subscriptions?: Subscription[];
  room_id?: string;
}

// PATCH body. Every field optional — only the ones present are written.
// password: blank string clears, omit-field leaves alone.
export type UpdateDeviceBody = Partial<Omit<CreateDeviceBody, "collector_id" | "reported_id">>;

export interface CollectorSummary {
  id: string;
  bridge_collector_id: string;
  name: string;
  status: string;
  last_seen_at?: string;
}

export interface NamedRow {
  id: string;
  name: string;
  parent_id?: string;
}

// BuildingRow extends NamedRow with the optional address + timezone the
// buildings list endpoint returns alongside the shared fields. Keeping it
// separate so the simpler tables don't grow phantom optional columns.
export interface BuildingRow extends NamedRow {
  address?: string;
  timezone?: string;
}

// AuditEntry mirrors the JSON shape returned by GET /api/v1/audit.
// before/after are intentionally `unknown` — the cloud returns whatever
// row_to_json produced for the target table, and the shape varies by
// target_kind. The UI renders these as JSON, not typed objects.
export interface AuditEntry {
  id: number;
  actor: string;
  action: string;
  target_kind: string;
  target_id?: string;
  before?: unknown;
  after?: unknown;
  metadata?: Record<string, unknown>;
  ts: string;
}

export interface Telemetry {
  device_id: string;
  device_name: string;
  device_type: DeviceType;
  location?: string;
  protocol: string;
  status: DeviceStatus;
  timestamp: string;
  metrics?: Record<string, unknown>;
  lens_metrics?: Record<string, unknown>;
  tags?: Record<string, string>;
  error?: string;
}

export interface DeviceEvent {
  device_id: string;
  device_name: string;
  device_type: DeviceType;
  event_type: string;
  payload?: Record<string, unknown>;
  timestamp: string;
}

export interface CommandRequest {
  name: string;
  args?: Record<string, unknown>;
}

export interface CommandResponse {
  raw: string;
  parsed?: Record<string, unknown>;
  latency_ms: number;
}

export interface FleetStatus {
  total: number;
  online: number;
  offline: number;
  degraded: number;
  time: string;
}

export type AlertSeverity = "info" | "warning" | "critical";
export type AlertStatus = "open" | "acknowledged" | "resolved";

export interface AlertItem {
  id: string;
  device_id: string;
  device_name: string;
  alert_key: string;
  severity: AlertSeverity;
  message: string;
  payload?: unknown;
  status: AlertStatus;
  opened_at: string;
  acknowledged_at?: string;
  acknowledged_by?: string;
  resolved_at?: string;
  resolved_by?: string;
}

export interface AlertsSummary {
  open: number;
  acknowledged: number;
  critical_open: number;
}

export type NotificationChannelType = "email" | "teams" | "webhook";

export interface NotificationChannel {
  id: string;
  name: string;
  type: NotificationChannelType;
  target: string;
  min_severity: AlertSeverity;
  enabled: boolean;
  last_sent_at?: string;
  last_error?: string;
}

export interface NotificationChannelBody {
  name: string;
  type?: NotificationChannelType; // omit on PATCH (type is immutable)
  target: string;
  min_severity: AlertSeverity;
  enabled?: boolean;
}

export interface DeviceUptimeRow {
  device_id: string;
  name: string;
  location: string;
  samples: number;
  online_samples: number;
  uptime_pct?: number;
  current_status: DeviceStatus;
  last_seen_at?: string;
}

export interface RoomActivityRow {
  room_id: string;
  room_name: string;
  building_name: string;
  device_count: number;
  event_count: number;
  last_event_at?: string;
}

export interface FirmwareRow {
  device_id: string;
  name: string;
  location: string;
  make?: string;
  model?: string;
  firmware_version?: string;
  // Per-(make, model) reference data — populated from firmware_targets when
  // an admin has curated one. Empty when no target is set; the UI falls back
  // to a neutral fleet breakdown per model group.
  target_version?: string;
  docs_url?: string;
}

export interface FirmwareTarget {
  id: string;
  make: string;
  model: string;
  target_version?: string;
  docs_url?: string;
  notes?: string;
  updated_at?: string;
  updated_by?: string;
}

export interface FirmwareTargetBody {
  make: string;
  model: string;
  target_version?: string;
  docs_url?: string;
  notes?: string;
}

export interface BulkCommandResult {
  device_id: string;
  command_id?: string;
  error?: string;
}

export interface BulkCommandResponse {
  results: BulkCommandResult[];
}

export interface HealthResponse {
  status: string;
  time: string;
}
