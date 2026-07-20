import type {
  AlertItem,
  AlertsSummary,
  AssetRow,
  AuditEntry,
  BuildingRow,
  BulkCommandResponse,
  CollectorSummary,
  CommandRequest,
  CommandResponse,
  CreateAssetBody,
  CreateCustomerBody,
  CreateCustomerResponse,
  CreateDeviceBody,
  CreateRoleBody,
  CreateUserBody,
  DeviceDetail,
  DeviceSummary,
  DeviceUptimeRow,
  FirmwareRow,
  FirmwareTarget,
  FirmwareTargetBody,
  FleetStatus,
  HealthResponse,
  NamedRow,
  NotificationChannel,
  NotificationChannelBody,
  RoleRow,
  RoomActivityRow,
  Telemetry,
  UpdateAssetBody,
  UpdateDeviceBody,
  UpdateRoleBody,
  UpdateUserBody,
  UserRow,
} from "./types";

// HTTP requests go to the Next.js dev server, which proxies to av-bridge via
// rewrites in next.config.mjs. This avoids CORS in the browser. To talk to a
// remote av-bridge, override the upstream when starting next: AV_BRIDGE_UPSTREAM=...
export const API_BASE = process.env.NEXT_PUBLIC_AV_BRIDGE_HTTP ?? "";

// WebSocket bypasses the browser's same-origin policy and connects directly,
// so when the bridge runs on another host point this at it explicitly.
export const WS_BASE =
  process.env.NEXT_PUBLIC_AV_BRIDGE_WS ?? "ws://localhost:8080";

// Bearer token comes from the active session set on sign-in. Falls back to
// the env-supplied key so smoke scripts and CI keep working when no browser
// session exists.
import { getScope, getToken, signOut } from "./session";
const FALLBACK_KEY = process.env.NEXT_PUBLIC_AV_BRIDGE_API_KEY ?? "";

export function currentToken(): string {
  return getToken() ?? FALLBACK_KEY;
}

// 401 on any authenticated call means the session is dead — expired,
// revoked, or the server was restarted. Clear the local session so AuthGuard
// bounces the user to /sign-in. Login itself is exempt because 401 there is
// a bad-password signal, not a session-death signal.
function handle401IfSessionDeath(path: string) {
  if (path.startsWith("/api/v1/auth/login")) return;
  if (typeof window === "undefined") return;
  signOut();
}

class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  const tok = currentToken();
  if (tok) headers["Authorization"] = `Bearer ${tok}`;
  // Vendor-acting-as-customer: only sent if a scope is set in session. The
  // cloud middleware rejects this from non-vendor users with 403.
  const scope = getScope();
  if (scope) headers["X-Customer-Scope"] = scope;
  return headers;
}

async function request<T>(
  path: string,
  init?: RequestInit & { signal?: AbortSignal }
): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(init?.headers ?? {}),
    },
    cache: "no-store",
  });
  if (!res.ok) {
    if (res.status === 401) handle401IfSessionDeath(path);
    let body = "";
    try {
      body = await res.text();
    } catch {}
    throw new ApiError(
      `${res.status} ${res.statusText}${body ? `: ${body}` : ""}`,
      res.status
    );
  }
  // 204 and any zero-length body — callers that use request<void> for
  // side-effect endpoints (PATCH /branding etc.) would otherwise trip
  // "Unexpected end of JSON input" from res.json(). Resolve with
  // undefined so the caller can ignore the return value.
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  if (text.length === 0) {
    return undefined as T;
  }
  return JSON.parse(text) as T;
}

async function requestText(
  path: string,
  init?: RequestInit & { signal?: AbortSignal }
): Promise<string> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { ...authHeaders(), ...(init?.headers ?? {}) },
    cache: "no-store",
  });
  if (!res.ok) {
    throw new ApiError(`${res.status} ${res.statusText}`, res.status);
  }
  return res.text();
}

export interface WhoamiResponse {
  user_id?: string;
  email?: string;
  name?: string;
  customer_id?: string;
  role: string;
  is_vendor?: boolean;
}

export interface HelpdeskCustomer {
  id: string;
  name: string;
  entra_tenant_id?: string;
}

export interface HelpdeskOverviewItem {
  id: string;
  name: string;
  entra_tenant_id?: string;
  devices_total: number;
  devices_online: number;
  devices_offline: number;
  devices_degraded: number;
  alerts_open: number;
  alerts_critical: number;
  collectors_total: number;
  last_bridge_seen?: string;
}

export interface LoginResponse {
  token: string;
  expires_at: string;
  role: string;
}

// Per-tenant branding, returned by GET /api/v1/branding. All fields are
// optional — an unbranded customer sends {} and the portal falls back to
// its default look. logo_data_url is a "data:image/...;base64,..." URI
// so it can be used directly as an <img src=>.
export interface Branding {
  display_name?: string;
  accent_color?: string;
  logo_data_url?: string;
}

export interface UpdateBrandingBody {
  display_name?: string;
  accent_color?: string;
  logo_data_url?: string;
}

// Nightly Room Readiness schedule — the customer-level default row. See
// docs/nightly-lifecycle-spec.md for the full data model. Slice 1 exposes
// only this row; per-room overrides and recipe CRUD land in later slices.
//
// GET /api/v1/nightly/schedule auto-provisions defaults on first read, so
// the portal never needs a "create schedule" flow.
export interface NightlySchedule {
  power_off_time: string;      // HH:MM (24h)
  power_on_time: string;       // HH:MM (24h)
  days_of_week: number[];      // ISO weekdays 1 (Mon) … 7 (Sun)
  timezone: string;            // IANA name
  test_recipe_id?: string;     // recipe not yet wired — slice 2 territory
  helpdesk_email?: string;
  retention_days: number;      // floor of 30
  enabled: boolean;
  updated_at?: string;
}

// UpdateNightlyScheduleBody — pointer-per-field so an omitted field leaves
// the stored value alone, and an empty string clears the two nullable text
// fields (helpdesk_email, test_recipe_id).
export interface UpdateNightlyScheduleBody {
  power_off_time?: string;
  power_on_time?: string;
  days_of_week?: number[];
  timezone?: string;
  test_recipe_id?: string;
  helpdesk_email?: string;
  retention_days?: number;
  enabled?: boolean;
}

// Nightly-lifecycle per-room override row. Returned by
// GET /api/v1/nightly/rooms. Effective values are already resolved against
// the customer default; `has_override` badges rooms whose behaviour
// diverges from inherit.
export interface NightlyRoomRow {
  room_id: string;
  room_name: string;
  building_id: string;
  building_name: string;
  location_name?: string;
  region_name?: string;
  effective_power_off_time: string;   // HH:MM
  effective_power_on_time: string;    // HH:MM
  effective_days_of_week: number[];   // ISO 1-7
  has_override: boolean;
  override_power_off_time?: string;   // present only when override sets it
  override_power_on_time?: string;
  override_days_of_week?: number[];
  excluded_until?: string;            // YYYY-MM-DD, if excluded through a date
}

// UpdateRoomOverrideBody — three-state fields.
//   - undefined  → field omitted from request; server leaves stored value alone
//   - null       → explicit clear; server drops the override, room inherits
//                  the customer default for that field
//   - value      → set the override to this value
//
// TypeScript's `| null` on optional fields models this exactly.
export interface UpdateRoomOverrideBody {
  power_off_time?: string | null;
  power_on_time?: string | null;
  days_of_week?: number[] | null;
  excluded_until?: string | null;    // YYYY-MM-DD
}

// Nightly test recipe — reusable step sequence executed after power-on.
// Slice 2B handles storage only; step execution is Phase B territory.
export interface NightlyRecipeRow {
  id: string;
  name: string;
  description?: string;
  step_count: number;
  updated_at: string;
}

export interface NightlyRecipeDetail {
  id: string;
  name: string;
  description?: string;
  // Steps are an opaque JSON array here; a structured builder can layer
  // types on later. `unknown[]` (not `any[]`) so consumers must narrow.
  steps: unknown[];
  created_at: string;
  updated_at: string;
}

export interface CreateNightlyRecipeBody {
  name: string;
  description?: string;
  steps: unknown[];
}

export interface UpdateNightlyRecipeBody {
  name?: string;
  description?: string;
  steps?: unknown[];
}

// Nightly run — one row per scheduled cycle. Written by the scheduler
// (see internal/nightly.Scheduler); the portal is read-only here.
export interface NightlyRunRow {
  id: string;
  room_id: string;
  room_name: string;
  building_id: string;
  building_name: string;
  location_name?: string;
  region_name?: string;
  recipe_id?: string;
  recipe_name?: string;
  phase: NightlyPhase;
  status: NightlyStatus;
  scheduled_at: string;
  started_at?: string;
  completed_at?: string;
  duration_seconds?: number;
  failure_reason?: string;
}

// Every phase the state machine can be in. Kept in sync with the CHECK
// constraint in migration 0023 — a new phase there means adding here too.
export type NightlyPhase =
  | "pending"
  | "scheduled_off"
  | "off"
  | "scheduled_on"
  | "waking"
  | "warming"
  | "testing"
  | "ready"
  | "failed";

export type NightlyStatus =
  | "pending"
  | "in_progress"
  | "succeeded"
  | "failed"
  | "skipped";

// Step result inside a run. Phase B populates these; slice 3 dry-run
// leaves them empty (the runs list still works, detail page shows a
// "no step results yet" hint until the recipe runner lands).
export interface NightlyStepRow {
  step_index: number;
  step_name: string;
  step_type: string;
  device_id?: string;
  device_name?: string;
  expected?: unknown;
  actual?: unknown;
  passed: boolean;
  error?: string;
  started_at?: string;
  completed_at?: string;
}

export interface NightlyRunDetail extends NightlyRunRow {
  steps: NightlyStepRow[];
}

export interface ListRunsOpts {
  from?: string;         // RFC 3339
  to?: string;           // RFC 3339
  room_id?: string;
  status?: string;       // csv of NightlyStatus values
  limit?: number;
}

export const api = {
  // -- auth --------------------------------------------------------------------
  //
  // Login is intentionally NOT guarded by AuthGuard — the caller has no token
  // yet. The returned token goes into localStorage via signIn() before the
  // portal reads /whoami to hydrate the rest of the session.
  login: (email: string, password: string, signal?: AbortSignal) =>
    request<LoginResponse>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
      signal,
    }),

  logout: async (signal?: AbortSignal): Promise<void> => {
    // Best-effort — tell the server to revoke the session so a leaked token
    // can't outlive the sign-out click. A network failure here still clears
    // local session state; the caller does that after this resolves.
    try {
      await fetch(`${API_BASE}/api/v1/auth/logout`, {
        method: "POST",
        headers: authHeaders(),
        signal,
        cache: "no-store",
      });
    } catch {
      // ignore — we still want to clear the local token
    }
  },

  changePassword: (
    current_password: string,
    new_password: string,
    signal?: AbortSignal
  ): Promise<void> =>
    fetch(`${API_BASE}/api/v1/auth/change-password`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ current_password, new_password }),
      signal,
      cache: "no-store",
    }).then(async (res) => {
      if (!res.ok) {
        let body = "";
        try { body = await res.text(); } catch {}
        throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
      }
    }),

  whoami: (signal?: AbortSignal) =>
    request<WhoamiResponse>("/api/v1/whoami", { signal }),

  getBranding: (signal?: AbortSignal) =>
    request<Branding>("/api/v1/branding", { signal }),

  updateBranding: (body: UpdateBrandingBody) =>
    request<void>("/api/v1/branding", { method: "PATCH", body: JSON.stringify(body) }),

  getNightlySchedule: (signal?: AbortSignal) =>
    request<NightlySchedule>("/api/v1/nightly/schedule", { signal }),

  updateNightlySchedule: (body: UpdateNightlyScheduleBody) =>
    request<void>("/api/v1/nightly/schedule", {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  listNightlyRooms: (signal?: AbortSignal) =>
    request<NightlyRoomRow[]>("/api/v1/nightly/rooms", { signal }),

  updateRoomOverride: (roomID: string, body: UpdateRoomOverrideBody) =>
    request<void>(`/api/v1/nightly/rooms/${encodeURIComponent(roomID)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deleteRoomOverride: (roomID: string) =>
    request<void>(`/api/v1/nightly/rooms/${encodeURIComponent(roomID)}`, {
      method: "DELETE",
    }),

  listNightlyRecipes: (signal?: AbortSignal) =>
    request<NightlyRecipeRow[]>("/api/v1/nightly/recipes", { signal }),

  getNightlyRecipe: (id: string, signal?: AbortSignal) =>
    request<NightlyRecipeDetail>(
      `/api/v1/nightly/recipes/${encodeURIComponent(id)}`,
      { signal }
    ),

  createNightlyRecipe: (body: CreateNightlyRecipeBody) =>
    request<{ id: string }>("/api/v1/nightly/recipes", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updateNightlyRecipe: (id: string, body: UpdateNightlyRecipeBody) =>
    request<void>(`/api/v1/nightly/recipes/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deleteNightlyRecipe: (id: string) =>
    request<void>(`/api/v1/nightly/recipes/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  listNightlyRuns: (opts?: ListRunsOpts, signal?: AbortSignal) => {
    const params = new URLSearchParams();
    if (opts?.from) params.set("from", opts.from);
    if (opts?.to) params.set("to", opts.to);
    if (opts?.room_id) params.set("room_id", opts.room_id);
    if (opts?.status) params.set("status", opts.status);
    if (opts?.limit) params.set("limit", String(opts.limit));
    const q = params.toString();
    return request<NightlyRunRow[]>(
      "/api/v1/nightly/runs" + (q ? `?${q}` : ""),
      { signal }
    );
  },

  getNightlyRun: (id: string, signal?: AbortSignal) =>
    request<NightlyRunDetail>(
      `/api/v1/nightly/runs/${encodeURIComponent(id)}`,
      { signal }
    ),

  helpdeskCustomers: (signal?: AbortSignal) =>
    request<HelpdeskCustomer[]>("/api/v1/helpdesk/customers", { signal }),

  helpdeskOverview: (signal?: AbortSignal) =>
    request<HelpdeskOverviewItem[]>("/api/v1/helpdesk/overview", { signal }),

  health: (signal?: AbortSignal) =>
    request<HealthResponse>("/healthz", { signal }),

  fleetStatus: (signal?: AbortSignal) =>
    request<FleetStatus>("/api/v1/status", { signal }),

  listDevices: (signal?: AbortSignal) =>
    request<DeviceSummary[]>("/api/v1/devices", { signal }),

  getDevice: (id: string, signal?: AbortSignal) =>
    request<DeviceDetail>(`/api/v1/devices/${encodeURIComponent(id)}`, {
      signal,
    }),

  getTelemetry: (id: string, signal?: AbortSignal) =>
    request<Telemetry>(
      `/api/v1/devices/${encodeURIComponent(id)}/telemetry`,
      { signal }
    ),

  sendCommand: (id: string, body: CommandRequest, signal?: AbortSignal) =>
    request<CommandResponse>(
      `/api/v1/devices/${encodeURIComponent(id)}/command`,
      { method: "POST", body: JSON.stringify(body), signal }
    ),

  sendBulkCommand: (
    body: { device_ids: string[]; name: string; args?: Record<string, unknown> },
    signal?: AbortSignal
  ) =>
    request<BulkCommandResponse>("/api/v1/commands/bulk", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  firmware: (signal?: AbortSignal) =>
    request<FirmwareRow[]>("/api/v1/firmware", { signal }),

  listFirmwareTargets: (signal?: AbortSignal) =>
    request<FirmwareTarget[]>("/api/v1/firmware/targets", { signal }),

  upsertFirmwareTarget: (body: FirmwareTargetBody, signal?: AbortSignal) =>
    request<{ id: string }>("/api/v1/firmware/targets", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  deleteFirmwareTarget: async (id: string, signal?: AbortSignal): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/firmware/targets/${encodeURIComponent(id)}`,
      { method: "DELETE", headers: authHeaders(), signal, cache: "no-store" }
    );
    if (!res.ok) {
      let body = "";
      try { body = await res.text(); } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  reconnectDevice: (id: string, signal?: AbortSignal) =>
    request<{ status: string; device_id: string }>(
      `/api/v1/devices/${encodeURIComponent(id)}/reconnect`,
      { method: "POST", signal }
    ),

  // -- device CRUD (admin-only on the cloud side; the dev portal token is
  // -- admin-roled by default, so all three work locally) -----------------------

  listCollectors: (signal?: AbortSignal) =>
    request<CollectorSummary[]>("/api/v1/collectors", { signal }),

  listRegions: (signal?: AbortSignal) =>
    request<NamedRow[]>("/api/v1/regions", { signal }),

  listLocations: (signal?: AbortSignal) =>
    request<NamedRow[]>("/api/v1/locations", { signal }),

  listBuildings: (signal?: AbortSignal) =>
    request<BuildingRow[]>("/api/v1/buildings", { signal }),

  listRooms: (signal?: AbortSignal) =>
    request<NamedRow[]>("/api/v1/rooms", { signal }),

  // Assets — CMDB. filters is an optional dict of query params; any
  // provided keys go on the URL as-is. Backend expects lowercase snake_case
  // keys (room_id, building_id, category, status, unplaced, q).
  listAssets: (
    filters?: Record<string, string | undefined>,
    signal?: AbortSignal
  ) => {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(filters ?? {})) {
      if (v !== undefined && v !== "") qs.set(k, v);
    }
    const query = qs.toString();
    return request<AssetRow[]>(
      `/api/v1/assets${query ? `?${query}` : ""}`,
      { signal }
    );
  },

  getAsset: (id: string, signal?: AbortSignal) =>
    request<AssetRow>(`/api/v1/assets/${encodeURIComponent(id)}`, { signal }),

  createAsset: (body: CreateAssetBody) =>
    request<{ id: string }>("/api/v1/assets", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updateAsset: (id: string, body: UpdateAssetBody) =>
    request<{ id: string }>(`/api/v1/assets/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deleteAsset: (id: string) =>
    request<void>(`/api/v1/assets/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  // Asset CSV round-trip. exportAssetsCSV returns the raw text so the
  // caller can either offer a download or diff it. importAssets streams
  // a File via multipart; on validation error the backend still returns
  // JSON with per-row detail (status 400) which fetch surfaces as
  // ApiError — the caller catches, parses, and shows the errors.
  exportAssetsCSV: async (signal?: AbortSignal): Promise<string> => {
    const res = await fetch(`${API_BASE}/api/v1/assets/export.csv`, {
      headers: authHeaders(),
      signal,
      cache: "no-store",
    });
    if (!res.ok) {
      if (res.status === 401) handle401IfSessionDeath("/api/v1/assets/export.csv");
      const body = await res.text().catch(() => "");
      throw new ApiError(
        `${res.status} ${res.statusText}${body ? `: ${body}` : ""}`,
        res.status
      );
    }
    return res.text();
  },

  importAssets: async (
    file: File
  ): Promise<{
    processed: number;
    created: number;
    updated: number;
    errors: { row: number; asset_tag?: string; message: string }[];
  }> => {
    const form = new FormData();
    form.append("file", file);
    const res = await fetch(`${API_BASE}/api/v1/assets/import`, {
      method: "POST",
      headers: authHeaders(), // NB: no Content-Type — fetch sets multipart boundary
      body: form,
      cache: "no-store",
    });
    // Both 200 and 400 return the same JSON shape (400 carries row
    // errors, 200 means clean). Anything else is a real failure.
    if (res.status !== 200 && res.status !== 400) {
      if (res.status === 401) handle401IfSessionDeath("/api/v1/assets/import");
      const body = await res.text().catch(() => "");
      throw new ApiError(
        `${res.status} ${res.statusText}${body ? `: ${body}` : ""}`,
        res.status
      );
    }
    return res.json();
  },

  createRegion: (name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>("/api/v1/regions", {
      method: "POST",
      body: JSON.stringify({ name }),
      signal,
    }),

  createLocation: (region_id: string, name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>("/api/v1/locations", {
      method: "POST",
      body: JSON.stringify({ region_id, name }),
      signal,
    }),

  createBuilding: (
    body: { location_id: string; name: string; address?: string; timezone?: string },
    signal?: AbortSignal
  ) =>
    request<{ id: string; name: string }>("/api/v1/buildings", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  createRoom: (building_id: string, name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>("/api/v1/rooms", {
      method: "POST",
      body: JSON.stringify({ building_id, name }),
      signal,
    }),

  updateRegion: (id: string, name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>(
      `/api/v1/regions/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify({ name }), signal }
    ),

  updateLocation: (id: string, name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>(
      `/api/v1/locations/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify({ name }), signal }
    ),

  updateBuilding: (
    id: string,
    body: { name?: string; address?: string; timezone?: string },
    signal?: AbortSignal
  ) =>
    request<{ id: string }>(`/api/v1/buildings/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
      signal,
    }),

  updateRoom: (id: string, name: string, signal?: AbortSignal) =>
    request<{ id: string; name: string }>(
      `/api/v1/rooms/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify({ name }), signal }
    ),

  // Hierarchy deletes — cascade down (region → location → building → room),
  // devices in affected rooms are orphaned (room_id set to NULL, device row
  // preserved). The cloud's audit metadata captures the cascade counts.
  deleteHierarchy: async (
    kind: "regions" | "locations" | "buildings" | "rooms",
    id: string,
    signal?: AbortSignal
  ): Promise<void> => {
    const res = await fetch(`${API_BASE}/api/v1/${kind}/${encodeURIComponent(id)}`, {
      method: "DELETE",
      headers: authHeaders(),
      signal,
      cache: "no-store",
    });
    if (!res.ok) {
      let body = "";
      try {
        body = await res.text();
      } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  createDevice: (body: CreateDeviceBody, signal?: AbortSignal) =>
    request<{ id: string }>("/api/v1/devices", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  updateDevice: (id: string, body: UpdateDeviceBody, signal?: AbortSignal) =>
    request<{ id: string; updated: string }>(
      `/api/v1/devices/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify(body), signal }
    ),

  deleteDevice: async (id: string, signal?: AbortSignal): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/devices/${encodeURIComponent(id)}`,
      {
        method: "DELETE",
        headers: authHeaders(),
        signal,
        cache: "no-store",
      }
    );
    if (!res.ok) {
      let body = "";
      try {
        body = await res.text();
      } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  // -- alerts ------------------------------------------------------------------

  listAlerts: (
    params: { status?: string; limit?: number } = {},
    signal?: AbortSignal
  ) => {
    const qs = new URLSearchParams();
    if (params.status) qs.set("status", params.status);
    if (params.limit) qs.set("limit", String(params.limit));
    const q = qs.toString();
    return request<AlertItem[]>(`/api/v1/alerts${q ? `?${q}` : ""}`, { signal });
  },

  alertsSummary: (signal?: AbortSignal) =>
    request<AlertsSummary>("/api/v1/alerts/summary", { signal }),

  acknowledgeAlert: (id: string, signal?: AbortSignal) =>
    request<{ id: string; status: string }>(
      `/api/v1/alerts/${encodeURIComponent(id)}/acknowledge`,
      { method: "POST", signal }
    ),

  resolveAlert: (id: string, signal?: AbortSignal) =>
    request<{ id: string; status: string }>(
      `/api/v1/alerts/${encodeURIComponent(id)}/resolve`,
      { method: "POST", signal }
    ),

  listNotificationChannels: (signal?: AbortSignal) =>
    request<NotificationChannel[]>("/api/v1/notifications/channels", { signal }),

  createNotificationChannel: (body: NotificationChannelBody, signal?: AbortSignal) =>
    request<{ id: string }>("/api/v1/notifications/channels", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  updateNotificationChannel: (
    id: string,
    body: NotificationChannelBody,
    signal?: AbortSignal
  ) =>
    request<{ id: string }>(
      `/api/v1/notifications/channels/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify(body), signal }
    ),

  deleteNotificationChannel: async (id: string, signal?: AbortSignal): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/notifications/channels/${encodeURIComponent(id)}`,
      { method: "DELETE", headers: authHeaders(), signal, cache: "no-store" }
    );
    if (!res.ok) {
      let body = "";
      try { body = await res.text(); } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  testNotificationChannel: (id: string, signal?: AbortSignal) =>
    request<{ status: string }>(
      `/api/v1/notifications/channels/${encodeURIComponent(id)}/test`,
      { method: "POST", signal }
    ),

  // -- reports -----------------------------------------------------------------

  deviceUptimeReport: (days: number, signal?: AbortSignal) =>
    request<DeviceUptimeRow[]>(
      `/api/v1/reports/device-uptime?days=${days}`,
      { signal }
    ),

  roomActivityReport: (days: number, signal?: AbortSignal) =>
    request<RoomActivityRow[]>(
      `/api/v1/reports/room-activity?days=${days}`,
      { signal }
    ),

  // reportCSVUrl builds the URL for a downloadable CSV including the bearer
  // token as a query param. Used directly in <a download> so the browser
  // handles the file save dialog.
  reportCSVUrl: (kind: "device-uptime" | "room-activity", days: number): string => {
    const tok = currentToken();
    const auth = tok ? `&token=${encodeURIComponent(tok)}` : "";
    return `${API_BASE}/api/v1/reports/${kind}?days=${days}&format=csv${auth}`;
  },

  // -- audit -------------------------------------------------------------------

  listAudit: (
    params: { targetKind?: string; targetId?: string; limit?: number } = {},
    signal?: AbortSignal
  ) => {
    const qs = new URLSearchParams();
    if (params.targetKind) qs.set("target_kind", params.targetKind);
    if (params.targetId) qs.set("target_id", params.targetId);
    if (params.limit) qs.set("limit", String(params.limit));
    const q = qs.toString();
    return request<AuditEntry[]>(`/api/v1/audit${q ? `?${q}` : ""}`, { signal });
  },

  metrics: (signal?: AbortSignal) => requestText("/metrics", { signal }),

  // -- users (customer tenant, admin-scoped) -----------------------------------
  //
  // Vendor callers can act on any customer by setting the scope in the user-
  // menu picker — the api client sends X-Customer-Scope automatically. Non-
  // vendor callers can only see their own tenant's users.

  listUsers: (signal?: AbortSignal) =>
    request<UserRow[]>("/api/v1/users", { signal }),

  createUser: (body: CreateUserBody, signal?: AbortSignal) =>
    request<{ id: string }>("/api/v1/users", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  updateUser: (id: string, body: UpdateUserBody, signal?: AbortSignal) =>
    request<{ id: string }>(`/api/v1/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
      signal,
    }),

  resetUserPassword: async (
    id: string,
    new_password: string,
    signal?: AbortSignal
  ): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/users/${encodeURIComponent(id)}/reset-password`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ new_password }),
        signal,
        cache: "no-store",
      }
    );
    if (!res.ok) {
      let body = "";
      try { body = await res.text(); } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  deleteUser: async (id: string, signal?: AbortSignal): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/users/${encodeURIComponent(id)}`,
      { method: "DELETE", headers: authHeaders(), signal, cache: "no-store" }
    );
    if (!res.ok) {
      let body = "";
      try { body = await res.text(); } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },

  // -- helpdesk customer create (vendor-only) ----------------------------------

  createCustomer: (body: CreateCustomerBody, signal?: AbortSignal) =>
    request<CreateCustomerResponse>("/api/v1/helpdesk/customers", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  // -- roles (per-tenant catalogue) --------------------------------------------

  listRoles: (signal?: AbortSignal) =>
    request<RoleRow[]>("/api/v1/roles", { signal }),

  getRole: (id: string, signal?: AbortSignal) =>
    request<RoleRow>(`/api/v1/roles/${encodeURIComponent(id)}`, { signal }),

  createRole: (body: CreateRoleBody, signal?: AbortSignal) =>
    request<{ id: string }>("/api/v1/roles", {
      method: "POST",
      body: JSON.stringify(body),
      signal,
    }),

  updateRole: (id: string, body: UpdateRoleBody, signal?: AbortSignal) =>
    request<{ id: string }>(`/api/v1/roles/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
      signal,
    }),

  deleteRole: async (id: string, signal?: AbortSignal): Promise<void> => {
    const res = await fetch(
      `${API_BASE}/api/v1/roles/${encodeURIComponent(id)}`,
      { method: "DELETE", headers: authHeaders(), signal, cache: "no-store" }
    );
    if (!res.ok) {
      let body = "";
      try { body = await res.text(); } catch {}
      throw new ApiError(`${res.status} ${res.statusText}${body ? `: ${body}` : ""}`, res.status);
    }
  },
};

export { ApiError };
