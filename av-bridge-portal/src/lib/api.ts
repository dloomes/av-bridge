import type {
  AlertItem,
  AlertsSummary,
  AuditEntry,
  BuildingRow,
  CollectorSummary,
  CommandRequest,
  CommandResponse,
  CreateDeviceBody,
  DeviceDetail,
  DeviceSummary,
  FleetStatus,
  HealthResponse,
  NamedRow,
  Telemetry,
  UpdateDeviceBody,
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
import { getScope, getToken } from "./session";
const FALLBACK_KEY = process.env.NEXT_PUBLIC_AV_BRIDGE_API_KEY ?? "";

export function currentToken(): string {
  return getToken() ?? FALLBACK_KEY;
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
    let body = "";
    try {
      body = await res.text();
    } catch {}
    throw new ApiError(
      `${res.status} ${res.statusText}${body ? `: ${body}` : ""}`,
      res.status
    );
  }
  return (await res.json()) as T;
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

export const api = {
  whoami: (signal?: AbortSignal) =>
    request<WhoamiResponse>("/api/v1/whoami", { signal }),

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
};

export { ApiError };
