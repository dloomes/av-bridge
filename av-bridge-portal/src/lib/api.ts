import type {
  AdapterInfo,
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
  UpdateCustomerBody,
  CreateCustomerResponse,
  CreateDeviceBody,
  CreateRoleBody,
  CreateUserBody,
  DeviceDetail,
  DeviceEvent,
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

// Statuses that indicate a transient upstream failure (Amplify SSR
// nginx has returned before the backend saw the request, so retrying
// is safe even for POST/PATCH — no risk of duplicate side-effect).
// Any other 4xx/5xx is deterministic and shouldn't be retried.
const RETRYABLE_STATUS = new Set([502, 503, 504]);

// Small exponential backoff schedule between retries. Two retries =
// three total attempts, covering an Amplify SSR cold start (~1-3s)
// without making a genuinely-down backend feel any slower for the user.
const RETRY_DELAYS_MS = [200, 500];

async function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    if (signal) {
      const onAbort = () => {
        clearTimeout(t);
        reject(new DOMException("Aborted", "AbortError"));
      };
      if (signal.aborted) return onAbort();
      signal.addEventListener("abort", onAbort, { once: true });
    }
  });
}

// fetchWithRetry wraps fetch() with a small retry loop for the RETRYABLE_STATUS
// set. Retries the underlying fetch AND network-error throws (fetch itself
// rejects on TCP resets, which the Amplify SSR nginx sometimes triggers
// alongside its 502 body). AbortSignal is honoured — an aborted request
// stops retrying immediately.
async function fetchWithRetry(
  url: string,
  init: RequestInit & { signal?: AbortSignal }
): Promise<Response> {
  let lastErr: unknown;
  for (let attempt = 0; attempt <= RETRY_DELAYS_MS.length; attempt++) {
    try {
      const res = await fetch(url, init);
      if (!RETRYABLE_STATUS.has(res.status) || attempt === RETRY_DELAYS_MS.length) {
        return res;
      }
      // Drain the body so the connection can be reused.
      try {
        await res.text();
      } catch {}
    } catch (err) {
      lastErr = err;
      // Don't retry on abort — the caller wants to stop.
      if (err instanceof DOMException && err.name === "AbortError") throw err;
      if (attempt === RETRY_DELAYS_MS.length) throw err;
    }
    await sleep(RETRY_DELAYS_MS[attempt], init.signal);
  }
  // Unreachable — the loop above always returns or throws.
  throw lastErr ?? new Error("fetchWithRetry: exhausted attempts");
}

async function request<T>(
  path: string,
  init?: RequestInit & { signal?: AbortSignal }
): Promise<T> {
  const res = await fetchWithRetry(`${API_BASE}${path}`, {
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
  const res = await fetchWithRetry(`${API_BASE}${path}`, {
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
  // Effective permission keys for the caller. For vendor accounts the
  // backend expands the "all permissions" bypass, so this list is always
  // authoritative for UI gating.
  permissions?: string[];
  building_scope_ids?: string[];
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
  slug?: string;
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
// its default look. Image fields (logo, hero) are "data:image/...;base64,..."
// URIs so they can be used directly as <img src=>.
//
// Sign-in surface fields (sign_in_message, support_contact, sso_button_label,
// sign_in_hero_data_url) are surfaced pre-auth by /public/branding?slug=…
// so they render on the sign-in page before the user has credentials.
export interface Branding {
  display_name?: string;
  accent_color?: string;
  logo_data_url?: string;
  sign_in_message?: string;
  support_contact?: string;
  sso_button_label?: string;
  sign_in_hero_data_url?: string;
}

export interface UpdateBrandingBody {
  display_name?: string;
  accent_color?: string;
  logo_data_url?: string;
  sign_in_message?: string;
  support_contact?: string;
  sso_button_label?: string;
  sign_in_hero_data_url?: string;
}

// Nightly Room Readiness schedule — the customer-level default row. See
// docs/nightly-lifecycle-spec.md for the full data model. Slice 1 exposes
// only this row; per-room overrides and routine CRUD land in later slices.
//
// GET /api/v1/nightly/schedule auto-provisions defaults on first read, so
// the portal never needs a "create schedule" flow.
export interface NightlySchedule {
  power_off_time: string;      // HH:MM (24h)
  power_on_time: string;       // HH:MM (24h)
  days_of_week: number[];      // ISO weekdays 1 (Mon) … 7 (Sun)
  timezone: string;            // IANA name
  test_routine_id?: string;     // routine not yet wired — slice 2 territory
  helpdesk_email?: string;
  retention_days: number;      // floor of 30
  enabled: boolean;
  updated_at?: string;
}

// UpdateNightlyScheduleBody — pointer-per-field so an omitted field leaves
// the stored value alone, and an empty string clears the two nullable text
// fields (helpdesk_email, test_routine_id).
export interface UpdateNightlyScheduleBody {
  power_off_time?: string;
  power_on_time?: string;
  days_of_week?: number[];
  timezone?: string;
  test_routine_id?: string;
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

// Nightly test routine — reusable step sequence executed after power-on.
// Slice 2B handles storage only; step execution is Phase B territory.
export interface NightlyRoutineRow {
  id: string;
  name: string;
  description?: string;
  step_count: number;
  updated_at: string;
}

export interface NightlyRoutineDetail {
  id: string;
  name: string;
  description?: string;
  // Steps are an opaque JSON array here; a structured builder can layer
  // types on later. `unknown[]` (not `any[]`) so consumers must narrow.
  steps: unknown[];
  created_at: string;
  updated_at: string;
}

export interface CreateNightlyRoutineBody {
  name: string;
  description?: string;
  steps: unknown[];
}

export interface UpdateNightlyRoutineBody {
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
  routine_id?: string;
  routine_name?: string;
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
// "no step results yet" hint until the routine runner lands).
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

  // Public — pre-login. Always resolves; the server returns 202 regardless
  // of whether the email matches a user so we can't accidentally surface
  // enumeration info to the caller. Only throws on transport errors.
  requestPasswordReset: async (
    email: string,
    signal?: AbortSignal
  ): Promise<void> => {
    await fetch(`${API_BASE}/public/password-reset/request`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email }),
      signal,
      cache: "no-store",
    });
  },

  // Public — completes a reset started via the email link. Bubbles the
  // server's error body up so the form can render "link expired or already
  // used" vs "password shorter than 12 characters" copy without probing.
  completePasswordReset: async (
    token: string,
    new_password: string,
    signal?: AbortSignal
  ): Promise<void> => {
    const res = await fetch(`${API_BASE}/public/password-reset/complete`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token, new_password }),
      signal,
      cache: "no-store",
    });
    if (!res.ok) {
      let body = "";
      try {
        body = await res.text();
      } catch {}
      let msg = `${res.status} ${res.statusText}`;
      if (body) {
        try {
          const parsed = JSON.parse(body) as { error?: string };
          if (parsed.error) msg = parsed.error;
        } catch {
          msg = body;
        }
      }
      throw new ApiError(msg, res.status);
    }
  },

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

  listNightlyRoutines: (signal?: AbortSignal) =>
    request<NightlyRoutineRow[]>("/api/v1/nightly/routines", { signal }),

  getNightlyRoutine: (id: string, signal?: AbortSignal) =>
    request<NightlyRoutineDetail>(
      `/api/v1/nightly/routines/${encodeURIComponent(id)}`,
      { signal }
    ),

  createNightlyRoutine: (body: CreateNightlyRoutineBody) =>
    request<{ id: string }>("/api/v1/nightly/routines", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  updateNightlyRoutine: (id: string, body: UpdateNightlyRoutineBody) =>
    request<void>(`/api/v1/nightly/routines/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),

  deleteNightlyRoutine: (id: string) =>
    request<void>(`/api/v1/nightly/routines/${encodeURIComponent(id)}`, {
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

  sendNightlyDigestNow: () =>
    request<{ status: string }>("/api/v1/nightly/digest/send-now", {
      method: "POST",
    }),

  runRoutineNow: (roomID: string, routineID?: string) =>
    request<{ run_id: string; routine_id: string }>(
      `/api/v1/nightly/rooms/${encodeURIComponent(roomID)}/run-now`,
      {
        method: "POST",
        body: routineID ? JSON.stringify({ routine_id: routineID }) : undefined,
      },
    ),

  helpdeskCustomers: (signal?: AbortSignal) =>
    request<HelpdeskCustomer[]>("/api/v1/helpdesk/customers", { signal }),

  helpdeskOverview: (signal?: AbortSignal) =>
    request<HelpdeskOverviewItem[]>("/api/v1/helpdesk/overview", { signal }),

  health: (signal?: AbortSignal) =>
    request<HealthResponse>("/healthz", { signal }),

  fleetStatus: (signal?: AbortSignal) =>
    request<FleetStatus>("/api/v1/status", { signal }),

  listDevices: (
    signal?: AbortSignal,
    filters?: { collectorID?: string }
  ) => {
    const qs = new URLSearchParams();
    if (filters?.collectorID) qs.set("collector_id", filters.collectorID);
    const q = qs.toString();
    return request<DeviceSummary[]>(
      "/api/v1/devices" + (q ? `?${q}` : ""),
      { signal }
    );
  },

  getDevice: (id: string, signal?: AbortSignal) =>
    request<DeviceDetail>(`/api/v1/devices/${encodeURIComponent(id)}`, {
      signal,
    }),

  getTelemetry: (id: string, signal?: AbortSignal) =>
    request<Telemetry>(
      `/api/v1/devices/${encodeURIComponent(id)}/telemetry`,
      { signal }
    ),

  getDeviceEvents: (id: string, limit = 50, signal?: AbortSignal) =>
    request<DeviceEvent[]>(
      `/api/v1/devices/${encodeURIComponent(id)}/events?limit=${limit}`,
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

  listAdapters: (signal?: AbortSignal) =>
    request<AdapterInfo[]>("/api/v1/adapters", { signal }),

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

  // Update an existing customer (helpdesk-only). Sends only the keys the
  // caller cares about — the server treats absent keys as "no change".
  updateCustomer: (
    id: string,
    body: UpdateCustomerBody,
    signal?: AbortSignal
  ) =>
    request<void>(`/api/v1/helpdesk/customers/${encodeURIComponent(id)}`, {
      method: "PATCH",
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
