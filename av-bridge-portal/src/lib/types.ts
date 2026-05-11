export type DeviceStatus = "online" | "offline" | "degraded" | "unknown";

export type DeviceType = "display" | "conferencing" | "audio" | "camera";

export interface DeviceSummary {
  id: string;
  name: string;
  type: DeviceType;
  protocol: string;
  location: string;
  status: DeviceStatus;
  tags?: Record<string, string>;
}

export interface DeviceDetail extends DeviceSummary {
  commands?: Record<string, string>;
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
