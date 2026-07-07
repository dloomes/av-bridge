export type DeviceStatus = "online" | "offline" | "degraded" | "unknown";

export type DeviceType = "display" | "conferencing" | "audio" | "camera" | "control";

export interface DeviceSummary {
  id: string;
  name: string;
  type: DeviceType;
  protocol: string;
  location: string;
  region?: string;
  location_name?: string;
  building?: string;
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

// DeviceAssetInput carries the standard CMDB asset fields when the caller
// wants to create-or-patch the linked asset alongside a device write.
// Mirrors the backend's deviceAssetInput struct in admin_handlers.go. All
// fields optional; any non-empty triggers the create-or-patch path.
export interface DeviceAssetInput {
  asset_tag?: string;
  category?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  status?: string;
  purchase_date?: string;
  warranty_end?: string;
  notes?: string;
}

// DeviceDetail returns the full editable config (creds excluded — write-only).
// The fields beyond DeviceSummary are what the edit form needs to pre-fill.
// asset (embedded) is present when this device is linked to a CMDB row —
// lets the edit form fill its Physical inventory section without a
// second /assets fetch.
export interface DeviceDetail extends DeviceSummary {
  collector_id: string;
  room_id?: string | null;
  asset_id?: string | null;
  asset?: DeviceAssetInput;
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
  asset_id?: string;
  asset?: DeviceAssetInput;
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

// AssetStatus + AssetCategory mirror the backend's allowlists in
// portalapi/assets.go. Keep both in sync when adding new options.
export type AssetStatus = "in_service" | "in_storage" | "retired" | "in_repair";
export type AssetCategory =
  | "display"
  | "camera"
  | "audio"
  | "conferencing"
  | "control_panel"
  | "touch_panel"
  | "cable"
  | "mount"
  | "rack"
  | "remote"
  | "microphone"
  | "speaker"
  | "projector"
  | "screen"
  | "computer"
  | "furniture"
  | "storage"
  | "other";

export interface AssetRow {
  id: string;
  asset_tag?: string;
  name: string;
  category: AssetCategory;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  status: AssetStatus;
  room_id?: string | null;
  room?: string;
  building?: string;
  location?: string;
  region?: string;
  purchase_date?: string;
  warranty_end?: string;
  notes?: string;
  device_id?: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateAssetBody {
  name: string;
  category: AssetCategory;
  asset_tag?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  status?: AssetStatus;
  room_id?: string;
  purchase_date?: string;
  warranty_end?: string;
  notes?: string;
}

export interface UpdateAssetBody {
  name?: string;
  category?: AssetCategory;
  asset_tag?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  status?: AssetStatus;
  room_id?: string;
  purchase_date?: string;
  warranty_end?: string;
  notes?: string;
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

// UserRow — a tenant user. role is legacy (a "primary role" name the
// backend derives from the highest-privilege system-default role the
// user holds; falls back to the first custom role's name). role_ids +
// role_names are the authoritative multi-role assignment.
// building_scope_ids empty = full-tenant scope; non-empty = the user
// only sees/acts on those buildings once the physical scope engine is
// live (backend enforcement lands in a later slice).
export interface UserRow {
  id: string;
  email: string;
  full_name?: string;
  role: string; // legacy primary-role display name
  role_ids: string[];
  role_names: string[];
  building_scope_ids: string[];
  disabled: boolean;
  created_at?: string;
  last_login_at?: string;
}

// CreateUserBody — role_ids is required and must be non-empty. Password
// is required on create only; reset flow is separate. building_scope_ids
// empty/omitted = unscoped (full tenant).
export interface CreateUserBody {
  email: string;
  password: string;
  full_name?: string;
  role_ids: string[];
  building_scope_ids?: string[];
}

// UpdateUserBody — every field optional; PATCH semantics on the cloud
// touch only the columns whose value is provided. role_ids replaces the
// user's full role assignment set (a set is a set). building_scope_ids
// replaces the scope wholesale — pass [] to clear.
export interface UpdateUserBody {
  full_name?: string;
  role_ids?: string[];
  building_scope_ids?: string[];
  disabled?: boolean;
}

// CreateCustomerBody — vendor-only helpdesk call. initial_admin is
// optional but strongly recommended; without it the new tenant has no
// way for anyone to log in until a second call creates a user.
export interface CreateCustomerBody {
  name: string;
  entra_tenant_id?: string;
  initial_admin?: {
    email: string;
    password: string;
    full_name?: string;
  };
}

export interface CreateCustomerResponse {
  customer_id: string;
  region_id: string;
  location_id: string;
  building_id: string;
  room_id: string;
  admin_user_id: string;
}

// Role catalogue — per-tenant. System defaults have is_system_default=true
// and reject writes at the backend. Permissions are strings drawn from the
// catalogue in lib/permissions.ts; the backend validates against its Go
// mirror and rejects unknown keys.
export interface RoleRow {
  id: string;
  name: string;
  description?: string;
  is_system_default: boolean;
  permissions: string[];
  assigned_users: number;
  created_at?: string;
}

export interface CreateRoleBody {
  name: string;
  description?: string;
  permissions: string[];
}

// UpdateRoleBody — PATCH semantics. Passing `permissions` REPLACES the
// role's full permission set (a set is a set — additive edits get confusing
// when the client isn't sure of the current state).
export interface UpdateRoleBody {
  name?: string;
  description?: string;
  permissions?: string[];
}

export interface HealthResponse {
  status: string;
  time: string;
}
