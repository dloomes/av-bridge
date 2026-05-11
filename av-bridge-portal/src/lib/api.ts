import type {
  CommandRequest,
  CommandResponse,
  DeviceDetail,
  DeviceSummary,
  FleetStatus,
  HealthResponse,
  Telemetry,
} from "./types";

// HTTP requests go to the Next.js dev server, which proxies to av-bridge via
// rewrites in next.config.mjs. This avoids CORS in the browser. To talk to a
// remote av-bridge, override the upstream when starting next: AV_BRIDGE_UPSTREAM=...
export const API_BASE = process.env.NEXT_PUBLIC_AV_BRIDGE_HTTP ?? "";

// WebSocket bypasses the browser's same-origin policy and connects directly,
// so when the bridge runs on another host point this at it explicitly.
export const WS_BASE =
  process.env.NEXT_PUBLIC_AV_BRIDGE_WS ?? "ws://localhost:8080";

// Optional bearer token for av-bridge's auth middleware. When set, every HTTP
// request carries `Authorization: Bearer <key>` and the WebSocket URL appends
// `?token=<key>` (browsers can't send custom headers on the WS handshake).
export const API_KEY = process.env.NEXT_PUBLIC_AV_BRIDGE_API_KEY ?? "";

class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

function authHeaders(): Record<string, string> {
  return API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {};
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

export const api = {
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

  metrics: (signal?: AbortSignal) => requestText("/metrics", { signal }),
};

export { ApiError };
