export type DeviceStatus = 'online' | 'offline' | 'degraded';

export type DeviceType = 'display' | 'audio' | 'video' | string;

export interface Device {
  id: string;
  name: string;
  location?: string;
  type: DeviceType;
  status: DeviceStatus;
  metrics?: Record<string, string | number | boolean | null>;
  lens_metrics?: Record<string, string | number | boolean | null>;
  last_seen?: string;
  tags?: Record<string, string>;
}

export interface DeviceEvent {
  id?: string;
  timestamp: string;
  device_id?: string;
  device_name?: string;
  level?: 'info' | 'warning' | 'error' | string;
  type?: string;
  message: string;
}

export interface CommandRequest {
  name: string;
  args?: Record<string, unknown>;
}

export interface CommandResponse {
  ok: boolean;
  message?: string;
  result?: unknown;
}

export interface ServiceStatus {
  status: string;
  version?: string;
  uptime_seconds?: number;
  [key: string]: unknown;
}
