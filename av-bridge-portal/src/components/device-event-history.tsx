"use client";

import { Fragment, useCallback } from "react";
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  History,
  RefreshCcw,
  RotateCw,
  XCircle,
} from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Skeleton } from "@/components/ui/skeleton";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { cn, formatRelative, formatTimestamp } from "@/lib/utils";
import type { DeviceEvent } from "@/lib/types";

// Historical events for a single device. Sourced from
// GET /api/v1/devices/{id}/events; polls every 30s. Rendered as a
// scannable per-severity list — colour + icon + human label + a
// filtered payload table, not the raw JSON dump.

interface Props {
  deviceId: string;
  limit?: number;
}

export function DeviceEventHistory({ deviceId, limit = 50 }: Props) {
  const fetcher = useCallback(
    (signal: AbortSignal) => api.getDeviceEvents(deviceId, limit, signal),
    [deviceId, limit]
  );
  const { data, loading, error, refresh } = usePolling<DeviceEvent[]>(
    fetcher,
    30_000
  );

  return (
    <Card className="flex flex-col">
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <div className="flex items-center gap-2">
          <History className="h-4 w-4 text-primary" />
          <CardTitle>Recent events</CardTitle>
        </div>
        <button
          type="button"
          onClick={() => refresh()}
          className="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Refresh event history"
        >
          <RefreshCcw
            className={cn("h-3 w-3", loading && data && "animate-spin")}
          />
          Refresh
        </button>
      </CardHeader>
      <CardContent className="p-0">
        <ScrollArea className="max-h-96 px-4 pb-4 scrollbar-thin">
          {error && (
            <div className="my-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              Failed to load events: {error.message}
            </div>
          )}

          {loading && !data && (
            <div className="space-y-2 py-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          )}

          {data && data.length === 0 && (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <div className="rounded-full bg-muted p-2">
                <Activity className="h-4 w-4 text-muted-foreground" />
              </div>
              <p className="text-sm text-muted-foreground">
                No events recorded yet
              </p>
              <p className="text-xs text-muted-foreground/70">
                Events appear here when the device changes state, reboots, or
                its status flips.
              </p>
            </div>
          )}

          {data && data.length > 0 && (
            <ul className="space-y-1.5" role="list">
              {data.map((evt, i) => (
                <EventRow key={`${evt.timestamp}-${i}`} event={evt} />
              ))}
            </ul>
          )}
        </ScrollArea>
      </CardContent>
    </Card>
  );
}

// ── Row ────────────────────────────────────────────────────────────────────

function EventRow({ event }: { event: DeviceEvent }) {
  const severity = eventSeverity(event.event_type);
  const meta = SEVERITY[severity];
  const Icon = meta.icon;
  const label = humaniseEventType(event.event_type);
  const payload = filterPayload(event.payload);

  return (
    <li
      className={cn(
        "group relative rounded-md border-l-2 border bg-card px-3 py-2 transition-colors hover:bg-muted/40",
        meta.border,
        meta.leftBar
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Icon className={cn("h-3.5 w-3.5 shrink-0", meta.icon_color)} />
          <span className="text-sm font-medium leading-tight truncate">
            {label}
          </span>
        </div>
        <time
          dateTime={event.timestamp}
          title={formatTimestamp(event.timestamp)}
          className="shrink-0 text-[11px] tabular-nums text-muted-foreground"
        >
          {formatRelative(event.timestamp)}
        </time>
      </div>

      {payload.length > 0 && (
        <dl className="mt-1.5 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5 pl-[22px] text-[11px] leading-relaxed">
          {payload.map(([k, v]) => (
            <Fragment key={k}>
              <dt className="text-muted-foreground">{k}</dt>
              <dd
                className="min-w-0 truncate font-mono text-foreground/80"
                title={String(v)}
              >
                {formatValue(v)}
              </dd>
            </Fragment>
          ))}
        </dl>
      )}
    </li>
  );
}

// ── Severity ───────────────────────────────────────────────────────────────

type Severity = "success" | "warning" | "destructive" | "info";

const SEVERITY: Record<
  Severity,
  {
    icon: React.ComponentType<{ className?: string }>;
    icon_color: string;
    border: string;
    leftBar: string;
  }
> = {
  success: {
    icon: CheckCircle2,
    icon_color: "[color:hsl(var(--success))]",
    border: "border-success/25",
    leftBar: "border-l-success",
  },
  warning: {
    icon: AlertTriangle,
    icon_color: "[color:hsl(var(--warning))]",
    border: "border-warning/25",
    leftBar: "border-l-warning",
  },
  destructive: {
    icon: XCircle,
    icon_color: "[color:hsl(var(--destructive))]",
    border: "border-destructive/25",
    leftBar: "border-l-destructive",
  },
  info: {
    icon: RotateCw,
    icon_color: "text-primary",
    border: "border-border",
    leftBar: "border-l-primary/50",
  },
};

// eventSeverity infers a semantic severity from the event type string.
// Alerts have well-known keys; device-emitted events fall back to "info".
// Keeping this a pure function of the string keeps the mapping easy to
// audit and extend as new adapters land.
function eventSeverity(eventType: string): Severity {
  const t = eventType.toLowerCase();
  if (t.includes("recovered") || t.includes("resolved")) return "success";
  if (
    t.includes("offline") ||
    t.includes("error") ||
    t.includes("failed") ||
    t.includes("critical")
  )
    return "destructive";
  if (t.includes("degraded") || t.includes("warning") || t.includes("stale"))
    return "warning";
  return "info";
}

// ── Labelling ──────────────────────────────────────────────────────────────

// Human-readable labels for the event types we know about. Unlisted event
// types fall through to a generic prettifier (strip `alert:` prefix,
// underscores → spaces, title-case). Extend this map as new adapter events
// land — the fallback is fine but a bespoke phrase is friendlier.
const EVENT_LABELS: Record<string, string> = {
  "alert:device_offline": "Device went offline",
  "alert:device_degraded": "Device degraded",
  "alert:device_recovered": "Device recovered",
  hotplug_status: "HDMI hotplug",
  call_state: "Call state changed",
  input_change: "Input source changed",
  mode_change: "Mode changed",
  subscription_update: "Subscription update",
};

function humaniseEventType(eventType: string): string {
  if (EVENT_LABELS[eventType]) return EVENT_LABELS[eventType];
  const stripped = eventType.replace(/^alert:/, "");
  return stripped
    .replace(/_/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

// ── Payload rendering ──────────────────────────────────────────────────────

// Fields to strip from every event's payload — they either duplicate
// context we already show (device_name is in the page header) or are
// wire-noise (alert_key repeats what event_type already says).
const NOISE_KEYS = new Set([
  "alert_key",
  "device_id",
  "device_name",
  "device_type",
  "timestamp",
  "ts",
]);

const MAX_PAYLOAD_FIELDS = 4;

function filterPayload(
  payload: Record<string, unknown> | undefined
): Array<[string, unknown]> {
  if (!payload) return [];
  const kept = Object.entries(payload).filter(([k, v]) => {
    if (NOISE_KEYS.has(k)) return false;
    if (v === null || v === undefined || v === "") return false;
    return true;
  });
  return kept.slice(0, MAX_PAYLOAD_FIELDS);
}

function formatValue(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "number" || typeof v === "string") return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}
