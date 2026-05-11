import type {
  CommandRequest,
  CommandResponse,
  Device,
  ServiceStatus,
} from './types';

export const BASE = process.env.NEXT_PUBLIC_AV_BRIDGE_URL ?? 'http://localhost:8080';

// In the browser, HTTP requests go through Next.js rewrites (see next.config.js)
// so they are same-origin and avoid CORS. On the server we hit av-bridge directly.
const HTTP_BASE = typeof window === 'undefined' ? BASE : '';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${HTTP_BASE}${path}`, {
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      ...(init?.headers ?? {}),
    },
    ...init,
  });
  if (!res.ok) {
    throw new Error(`Request failed (${res.status} ${res.statusText}) for ${path}`);
  }
  return (await res.json()) as T;
}

async function requestText(path: string): Promise<string> {
  const res = await fetch(`${HTTP_BASE}${path}`, { cache: 'no-store' });
  if (!res.ok) {
    throw new Error(`Request failed (${res.status} ${res.statusText}) for ${path}`);
  }
  return res.text();
}

export function listDevices(): Promise<Device[]> {
  return request<Device[]>('/api/v1/devices');
}

export function getDevice(id: string): Promise<Device> {
  return request<Device>(`/api/v1/devices/${encodeURIComponent(id)}`);
}

export function sendCommand(id: string, command: CommandRequest): Promise<CommandResponse> {
  return request<CommandResponse>(`/api/v1/devices/${encodeURIComponent(id)}/command`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(command),
  });
}

export function getStatus(): Promise<ServiceStatus> {
  return request<ServiceStatus>('/api/v1/status');
}

export function getMetrics(): Promise<string> {
  return requestText('/metrics');
}

export function eventsWebSocketUrl(): string {
  const wsBase = BASE.replace(/^http/, 'ws');
  return `${wsBase}/ws/events`;
}
