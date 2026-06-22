export type DeviceStatus = "online" | "offline" | "degraded" | "unknown";

export type DeviceType = "display" | "conferencing" | "audio" | "camera" | "control";

export interface DeviceSummary {
  id: string;
  name: string;
  type: DeviceType;
  protocol: string;
  location: string;
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

export interface HealthResponse {
  status: string;
  time: string;
}
