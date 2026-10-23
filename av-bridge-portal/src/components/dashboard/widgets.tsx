"use client";

// Dashboard widgets. Each one fetches and refreshes its own data through
// the normal API calls, so it shows only what the viewer's access allows.
// WIDGETS is the catalogue the picker offers; the cloud keeps the same list
// of types (av-bridge-cloud/internal/portalapi/dashboards.go) — add a type
// in both places.

import dynamic from "next/dynamic";
import Link from "next/link";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Activity,
  AlertTriangle,
  BarChart3,
  Bell,
  Building2,
  CalendarClock,
  Clock,
  Hash,
  Map as MapIcon,
  Megaphone,
  Users,
  Cpu,
  DoorOpen,
  Gauge,
  Moon,
  Radio,
  Server,
  ShieldAlert,
  type LucideIcon,
} from "lucide-react";
import { usePolling } from "@/hooks/usePolling";
import { api, type DashboardWidgetSize } from "@/lib/api";
import { cn, formatMetricValue, formatRelative } from "@/lib/utils";
import type { BuildingsMapEntry } from "@/components/buildings-map";
import { alertSubject, type BuildingRow, type DeviceStatus, type DeviceSummary } from "@/lib/types";

export type WidgetConfig = Record<string, unknown>;

// A setting shown in the widget's settings panel.
export type ConfigField =
  | { key: string; label: string; kind: "select"; options: { value: string; label: string }[]; default: string }
  | { key: string; label: string; kind: "building" | "region" | "room" | "device"; optional?: boolean }
  | { key: string; label: string; kind: "text"; placeholder?: string; multiline?: boolean; maxLength: number }
  | { key: string; label: string; kind: "number"; default: number; min: number; max: number };

export interface WidgetDef {
  type: string;
  title: string;
  description: string;
  icon: LucideIcon;
  defaultSize: DashboardWidgetSize;
  fields: ConfigField[];
  // Title shown on the widget, when it depends on its settings.
  heading?: (config: WidgetConfig) => string | undefined;
  render: (config: WidgetConfig) => ReactNode;
}

const REFRESH_MS = 30_000;
// Reports scan days of readings; the cloud keeps each result for 15 minutes.
const REPORT_REFRESH_MS = 15 * 60_000;

const str = (c: WidgetConfig, k: string, d = "") => (typeof c[k] === "string" ? (c[k] as string) : d);
const num = (c: WidgetConfig, k: string, d: number) => {
  const v = Number(c[k]);
  return Number.isFinite(v) && v > 0 ? v : d;
};

const LIMIT_FIELD: ConfigField = {
  key: "limit", label: "Show", kind: "select", default: "5",
  options: [{ value: "5", label: "5" }, { value: "10", label: "10" }, { value: "20", label: "20" }],
};
const DAYS_FIELD: ConfigField = {
  key: "days", label: "Period", kind: "select", default: "7",
  options: [{ value: "1", label: "24 hours" }, { value: "7", label: "7 days" }, { value: "30", label: "30 days" }],
};
const BUILDING_FIELD: ConfigField = { key: "building", label: "Building", kind: "building", optional: true };

// ── Shared pieces ────────────────────────────────────────────────────────

// Loading / error / no-access states, so each widget handles only its data.
function Load<T>({ state, children }: { state: { data: T | null; error: Error | null }; children: (d: T) => ReactNode }) {
  if (state.error && state.data == null) {
    const denied = /^403\b/.test(state.error.message);
    return (
      <p className="text-xs text-muted-foreground">
        {denied ? "You don't have access to this widget's data." : "Couldn't load this widget. It will retry shortly."}
      </p>
    );
  }
  if (state.data == null) {
    return <div className="h-16 animate-pulse rounded-md bg-muted/60" />;
  }
  return <>{children(state.data)}</>;
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
}

function Row({ href, children, right }: { href?: string; children: ReactNode; right?: ReactNode }) {
  const inner = (
    <>
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {right != null && <span className="flex-shrink-0 text-xs text-muted-foreground tabular-nums">{right}</span>}
    </>
  );
  return (
    <li>
      {href ? (
        <Link href={href} className="flex items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent/40">
          {inner}
        </Link>
      ) : (
        <div className="flex items-center gap-2 px-1.5 py-1 text-sm">{inner}</div>
      )}
    </li>
  );
}

const statusDot: Record<DeviceStatus, string> = {
  online: "bg-success",
  offline: "bg-destructive",
  unknown: "bg-muted-foreground/40",
  switched_off: "bg-sky-500",
};

function Dot({ status }: { status: DeviceStatus }) {
  return <span className={cn("inline-block h-2 w-2 flex-shrink-0 rounded-full", statusDot[status])} aria-label={status} />;
}

function Tile({ label, value, tone, href }: { label: string; value: ReactNode; tone?: "bad" | "warn" | "ok"; href?: string }) {
  const body = (
    <div className="rounded-md border bg-background/60 px-3 py-2">
      <div
        className={cn(
          "text-xl font-semibold tabular-nums",
          tone === "bad" && "[color:hsl(var(--destructive))]",
          tone === "warn" && "text-amber-600",
          tone === "ok" && "text-emerald-600"
        )}
      >
        {value}
      </div>
      <div className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</div>
    </div>
  );
  return href ? <Link href={href} className="block hover:opacity-80">{body}</Link> : body;
}

// Several widgets need the device list; they share one request, reused for
// a few seconds, rather than each fetching the whole estate.
let deviceCache: { at: number; list: Promise<DeviceSummary[]> } | null = null;

function sharedDevices(): Promise<DeviceSummary[]> {
  if (!deviceCache || Date.now() - deviceCache.at > 10_000) {
    const list = api.listDevices();
    deviceCache = { at: Date.now(), list };
    list.catch(() => {
      deviceCache = null;
    });
  }
  return deviceCache.list;
}

function useDevices(building: string) {
  const state = usePolling<DeviceSummary[]>(() => sharedDevices(), REFRESH_MS);
  const data = useMemo(
    () => (state.data ? state.data.filter((d) => !building || d.building === building) : null),
    [state.data, building]
  );
  return { data, error: state.error };
}

function devicesHref(params: Record<string, string | undefined>) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `/devices?${s}` : "/devices";
}

// ── Widgets ──────────────────────────────────────────────────────────────

function StatusWidget({ config }: { config: WidgetConfig }) {
  const building = str(config, "building");
  const state = useDevices(building);
  return (
    <Load state={state}>
      {(devs) => {
        const count = (s: DeviceStatus) => devs.filter((d) => d.status === s).length;
        const href = (status: string) => devicesHref({ status, building });
        return (
          <div className="grid grid-cols-2 gap-2">
            <Tile label="Online" value={count("online")} tone="ok" href={href("online")} />
            <Tile label="Offline" value={count("offline")} tone={count("offline") ? "bad" : undefined} href={href("offline")} />
            <Tile label="Unknown" value={count("unknown")} tone={count("unknown") ? "warn" : undefined} href={href("unknown")} />
            <Tile label="Switched off" value={count("switched_off")} href={href("switched_off")} />
          </div>
        );
      }}
    </Load>
  );
}

function AlertsWidget({ config }: { config: WidgetConfig }) {
  const severity = str(config, "severity", "any");
  const limit = num(config, "limit", 5);
  const state = usePolling((s) => api.listAlerts({ status: "open", limit: 200 }, s), REFRESH_MS);
  return (
    <Load state={state}>
      {(alerts) => {
        const rows = alerts.filter((a) =>
          severity === "critical" ? a.severity === "critical" : severity === "warning" ? a.severity !== "info" : true
        );
        if (rows.length === 0) return <Empty>No open alerts.</Empty>;
        return (
          <>
            <div className="mb-1 text-xs text-muted-foreground">{rows.length} open</div>
            <ul className="space-y-0.5">
              {rows.slice(0, limit).map((a) => (
                <Row key={a.id} href={alertSubject(a).href} right={formatRelative(a.opened_at)}>
                  <span
                    className={cn(
                      "mr-1.5 inline-block h-2 w-2 rounded-full align-middle",
                      a.severity === "critical" ? "bg-destructive" : a.severity === "warning" ? "bg-amber-500" : "bg-sky-500"
                    )}
                  />
                  <span className="font-medium">{alertSubject(a).name}</span>
                  <span className="text-muted-foreground"> · {a.message}</span>
                </Row>
              ))}
            </ul>
          </>
        );
      }}
    </Load>
  );
}

function AttentionWidget({ config }: { config: WidgetConfig }) {
  const building = str(config, "building");
  const limit = num(config, "limit", 5);
  const state = useDevices(building);
  return (
    <Load state={state}>
      {(devs) => {
        const rooms = new Map<string, { building?: string; room: string; offline: number; unknown: number }>();
        for (const d of devs) {
          if (d.status !== "offline" && d.status !== "unknown") continue;
          const room = (d.location ?? "").split(" / ").slice(-1)[0] || "Unassigned";
          const key = `${d.building ?? ""}|${room}`;
          const r = rooms.get(key) ?? { building: d.building, room, offline: 0, unknown: 0 };
          if (d.status === "offline") r.offline++;
          else r.unknown++;
          rooms.set(key, r);
        }
        const list = Array.from(rooms.values()).sort((a, b) => b.offline - a.offline || b.unknown - a.unknown);
        if (list.length === 0) return <Empty>Every room is healthy.</Empty>;
        return (
          <>
            <div className="mb-1 text-xs text-muted-foreground">{list.length} rooms</div>
            <ul className="space-y-0.5">
              {list.slice(0, limit).map((r) => (
                <Row
                  key={`${r.building}|${r.room}`}
                  href={devicesHref({ building: r.building, room: r.room })}
                  right={[r.offline && `${r.offline} offline`, r.unknown && `${r.unknown} unknown`].filter(Boolean).join(" · ")}
                >
                  <span className="font-medium">{r.room}</span>
                  {r.building && <span className="text-muted-foreground"> · {r.building}</span>}
                </Row>
              ))}
            </ul>
          </>
        );
      }}
    </Load>
  );
}

function ReadinessWidget({ config }: { config: WidgetConfig }) {
  const kind = str(config, "kind", "any");
  const limit = num(config, "limit", 5);
  const state = usePolling(
    (s) =>
      api.listNightlyRuns(
        {
          from: new Date(Date.now() - 24 * 3600_000).toISOString(),
          limit: 500,
          kind: kind === "power" || kind === "readiness" ? kind : undefined,
        },
        s
      ),
    REFRESH_MS,
    [kind]
  );
  return (
    <Load state={state}>
      {(runs) => {
        if (runs.length === 0) return <Empty>No runs in the last 24 hours.</Empty>;
        const ok = runs.filter((r) => r.status === "succeeded").length;
        const failed = runs.filter((r) => r.status === "failed");
        const running = runs.filter((r) => r.status === "in_progress" || r.status === "pending").length;
        return (
          <>
            <div className="mb-2 grid grid-cols-3 gap-2">
              <Tile label="Passed" value={ok} tone="ok" />
              <Tile label="Failed" value={failed.length} tone={failed.length ? "bad" : undefined} />
              <Tile label="Running" value={running} />
            </div>
            {failed.length > 0 && (
              <ul className="space-y-0.5">
                {failed.slice(0, limit).map((r) => (
                  <Row key={r.id} href="/nightly/runs" right={r.kind === "power" ? "Power" : "Checks"}>
                    <span className="font-medium">{r.room_name}</span>
                    {r.failure_reason && <span className="text-muted-foreground"> · {r.failure_reason}</span>}
                  </Row>
                ))}
              </ul>
            )}
          </>
        );
      }}
    </Load>
  );
}

function UsageWidget({ config }: { config: WidgetConfig }) {
  const days = num(config, "days", 7);
  const mode = str(config, "mode", "busiest");
  const limit = num(config, "limit", 5);
  const state = usePolling((s) => api.roomUsageReport(days, s), REPORT_REFRESH_MS, [days]);
  return (
    <Load state={state}>
      {(rows) => {
        const measured = rows.filter((r) => r.has_usage_signal);
        if (measured.length === 0) return <Empty>No rooms report calls or people yet.</Empty>;
        const sorted = [...measured].sort((a, b) =>
          mode === "least" ? a.avg_hours_per_day - b.avg_hours_per_day : b.avg_hours_per_day - a.avg_hours_per_day
        );
        const max = Math.max(1, ...measured.map((r) => r.avg_hours_per_day));
        return (
          <ul className="space-y-1.5">
            {sorted.slice(0, limit).map((r) => (
              <li key={r.room_id} className="text-sm">
                <div className="flex justify-between gap-2">
                  <span className="truncate font-medium">{r.room_name}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">{r.avg_hours_per_day.toFixed(1)} h/day</span>
                </div>
                <div className="mt-0.5 h-1.5 rounded bg-muted">
                  <div className="h-full rounded bg-foreground/70" style={{ width: `${(r.avg_hours_per_day / max) * 100}%` }} />
                </div>
              </li>
            ))}
          </ul>
        );
      }}
    </Load>
  );
}

function UptimeWidget({ config }: { config: WidgetConfig }) {
  const days = num(config, "days", 7);
  const limit = num(config, "limit", 5);
  const state = usePolling((s) => api.deviceUptimeReport(days, s), REPORT_REFRESH_MS, [days]);
  return (
    <Load state={state}>
      {(rows) => {
        const list = rows
          .filter((r) => r.uptime_pct != null)
          .sort((a, b) => (a.uptime_pct ?? 0) - (b.uptime_pct ?? 0))
          .slice(0, limit);
        if (list.length === 0) return <Empty>No uptime data yet.</Empty>;
        return (
          <ul className="space-y-0.5">
            {list.map((r) => (
              <Row key={r.device_id} href={`/devices/${encodeURIComponent(r.device_id)}`} right={`${(r.uptime_pct ?? 0).toFixed(1)}%`}>
                <span className="font-medium">{r.name}</span>
                {r.location && <span className="text-muted-foreground"> · {r.location}</span>}
              </Row>
            ))}
          </ul>
        );
      }}
    </Load>
  );
}

function CollectorsWidget() {
  const state = usePolling((s) => api.listCollectors(s), REFRESH_MS);
  return (
    <Load state={state}>
      {(cols) => {
        const down = cols.filter((c) => c.status !== "online");
        return (
          <>
            <div className="mb-2 grid grid-cols-2 gap-2">
              <Tile label="Online" value={cols.length - down.length} tone="ok" href="/collectors" />
              <Tile label="Not online" value={down.length} tone={down.length ? "bad" : undefined} href="/collectors" />
            </div>
            <ul className="space-y-0.5">
              {down.slice(0, 5).map((c) => (
                <Row key={c.id} href="/collectors" right={c.last_seen_at ? `seen ${formatRelative(c.last_seen_at)}` : c.status}>
                  <span className="font-medium">{c.name}</span>
                  {c.building_name && <span className="text-muted-foreground"> · {c.building_name}</span>}
                </Row>
              ))}
            </ul>
          </>
        );
      }}
    </Load>
  );
}

function WarrantyWidget({ config }: { config: WidgetConfig }) {
  const limit = num(config, "limit", 5);
  const state = usePolling((s) => api.warrantyReport(s), 10 * 60_000);
  return (
    <Load state={state}>
      {(rows) => {
        const expired = rows.filter((r) => r.bucket === "expired").length;
        const soon = rows.filter((r) => r.bucket === "lt_30d" || r.bucket === "lt_90d");
        return (
          <>
            <div className="mb-2 grid grid-cols-2 gap-2">
              <Tile label="Expired" value={expired} tone={expired ? "bad" : undefined} href="/reports" />
              <Tile label="Within 90 days" value={soon.length} tone={soon.length ? "warn" : undefined} href="/reports" />
            </div>
            {soon.length === 0 ? (
              <Empty>Nothing expires in the next 90 days.</Empty>
            ) : (
              <ul className="space-y-0.5">
                {soon.slice(0, limit).map((r) => (
                  <Row key={r.asset_id} href="/assets" right={`${r.days_remaining} days`}>
                    <span className="font-medium">{r.name}</span>
                    {r.location && <span className="text-muted-foreground"> · {r.location}</span>}
                  </Row>
                ))}
              </ul>
            )}
          </>
        );
      }}
    </Load>
  );
}

function FirmwareWidget({ config }: { config: WidgetConfig }) {
  const limit = num(config, "limit", 5);
  const state = usePolling((s) => api.firmware(s), 10 * 60_000);
  return (
    <Load state={state}>
      {(rows) => {
        const targeted = rows.filter((r) => r.target_version);
        const drift = targeted.filter((r) => r.firmware_version !== r.target_version);
        if (targeted.length === 0) return <Empty>No firmware targets set. Set them on the Firmware page.</Empty>;
        return (
          <>
            <div className="mb-2 grid grid-cols-2 gap-2">
              <Tile label="On target" value={targeted.length - drift.length} tone="ok" href="/firmware" />
              <Tile label="Off target" value={drift.length} tone={drift.length ? "warn" : undefined} href="/firmware" />
            </div>
            <ul className="space-y-0.5">
              {drift.slice(0, limit).map((r) => (
                <Row key={r.device_id} href={`/devices/${encodeURIComponent(r.device_id)}`} right={`${r.firmware_version || "?"} → ${r.target_version}`}>
                  <span className="font-medium">{r.name}</span>
                </Row>
              ))}
            </ul>
          </>
        );
      }}
    </Load>
  );
}

function RoomWidget({ config }: { config: WidgetConfig }) {
  const roomId = str(config, "room");
  const state = usePolling<DeviceSummary[]>(() => sharedDevices(), REFRESH_MS);
  if (!roomId) return <Empty>Choose a room in this widget&apos;s settings.</Empty>;
  return (
    <Load state={state}>
      {(all) => {
        const devs = all.filter((d) => d.room_id === roomId).sort((a, b) => a.name.localeCompare(b.name));
        if (devs.length === 0) return <Empty>No devices in this room.</Empty>;
        return (
          <ul className="space-y-0.5">
            {devs.map((d) => (
              <Row key={d.id} href={`/devices/${encodeURIComponent(d.id)}`} right={d.power_status}>
                <Dot status={d.status} /> <span className="ml-1">{d.name}</span>
              </Row>
            ))}
          </ul>
        );
      }}
    </Load>
  );
}

function DeviceWidget({ config }: { config: WidgetConfig }) {
  const id = str(config, "device");
  const state = usePolling((s) => (id ? api.getTelemetry(id, s) : Promise.resolve(null)), REFRESH_MS, [id]);
  if (!id) return <Empty>Choose a device in this widget&apos;s settings.</Empty>;
  return (
    <Load state={state}>
      {(t) => {
        if (!t) return null;
        const readings = Object.entries(t.metrics ?? {})
          .filter(([, v]) => v == null || typeof v !== "object")
          .slice(0, 8);
        return (
          <>
            <Link href={`/devices/${encodeURIComponent(id)}`} className="mb-2 flex items-center gap-2 text-sm hover:underline">
              <Dot status={t.status} />
              <span className="font-medium capitalize">{t.status.replace("_", " ")}</span>
              <span className="text-xs text-muted-foreground">· {formatRelative(t.timestamp)}</span>
            </Link>
            {readings.length === 0 ? (
              <Empty>No readings yet.</Empty>
            ) : (
              <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
                {readings.map(([k, v]) => (
                  <div key={k} className="min-w-0">
                    <dt className="truncate text-muted-foreground">{k.replace(/_/g, " ")}</dt>
                    <dd className="truncate font-medium">{formatMetricValue(v)}</dd>
                  </div>
                ))}
              </dl>
            )}
          </>
        );
      }}
    </Load>
  );
}

function ActivityWidget({ config }: { config: WidgetConfig }) {
  const limit = num(config, "limit", 10);
  const state = usePolling((s) => api.listAudit({ limit }, s), REFRESH_MS, [limit]);
  return (
    <Load state={state}>
      {(rows) =>
        rows.length === 0 ? (
          <Empty>No activity yet.</Empty>
        ) : (
          <ul className="space-y-0.5">
            {rows.map((r) => (
              <Row key={r.id} href="/audit" right={formatRelative(r.ts)}>
                <span className="font-medium">{r.actor}</span>
                <span className="text-muted-foreground"> · {r.action.replace(/[._]/g, " ")}</span>
              </Row>
            ))}
          </ul>
        )
      }
    </Load>
  );
}

// ── Wallboard widgets ────────────────────────────────────────────────────

function ClockWidget({ config }: { config: WidgetConfig }) {
  const tz = str(config, "tz", "Europe/London");
  const hour12 = str(config, "format") === "12";
  const seconds = str(config, "seconds") !== "off";
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), seconds ? 1000 : 15_000);
    return () => clearInterval(t);
  }, [seconds]);
  let time = "";
  let date = "";
  try {
    time = now.toLocaleTimeString("en-GB", {
      timeZone: tz, hour: "2-digit", minute: "2-digit", second: seconds ? "2-digit" : undefined, hour12,
    });
    date = now.toLocaleDateString("en-GB", { timeZone: tz, weekday: "long", day: "numeric", month: "long", year: "numeric" });
  } catch {
    time = now.toLocaleTimeString("en-GB");
  }
  return (
    <div className="flex h-full flex-col items-center justify-center py-2 text-center">
      <div className="text-5xl font-semibold tabular-nums tracking-tight">{time}</div>
      <div className="mt-1 text-sm text-muted-foreground">{date}</div>
      {tz !== "Europe/London" && <div className="text-xs text-muted-foreground">{tz.replace(/_/g, " ")}</div>}
    </div>
  );
}

function NoticeWidget({ config }: { config: WidgetConfig }) {
  const text = str(config, "text").trim();
  const tone = str(config, "tone", "info");
  if (!text) return <Empty>Write the notice in this widget&apos;s settings.</Empty>;
  return (
    <div
      className={cn(
        "h-full whitespace-pre-wrap rounded-md border-l-4 px-3 py-2 text-lg leading-snug",
        tone === "critical" && "border-destructive bg-destructive/10",
        tone === "warning" && "border-amber-500 bg-amber-500/10",
        tone === "info" && "border-sky-500 bg-sky-500/10"
      )}
    >
      {text}
    </div>
  );
}

// Each Big number metric: how to fetch it, and whether a high value is bad.
const BIG_NUMBERS: Record<string, { label: string; fetch: () => Promise<number>; good?: boolean; href: string }> = {
  offline: { label: "Devices offline", href: "/devices?status=offline",
    fetch: async () => (await sharedDevices()).filter((d) => d.status === "offline").length },
  unknown: { label: "Devices not reporting", href: "/devices?status=unknown",
    fetch: async () => (await sharedDevices()).filter((d) => d.status === "unknown").length },
  critical: { label: "Critical alerts", href: "/alerts",
    fetch: async () => (await api.alertsSummary()).critical_open },
  alerts: { label: "Open alerts", href: "/alerts",
    fetch: async () => (await api.alertsSummary()).open },
  attention: { label: "Rooms needing attention", href: "/devices?status=offline",
    fetch: async () => {
      const rooms = new Set<string>();
      for (const d of await sharedDevices())
        if (d.status === "offline" || d.status === "unknown") rooms.add(`${d.building ?? ""}|${d.location}`);
      return rooms.size;
    } },
  readiness: { label: "Room Readiness failures, 24 h", href: "/nightly/runs",
    fetch: async () =>
      (await api.listNightlyRuns({ from: new Date(Date.now() - 24 * 3600_000).toISOString(), status: "failed", limit: 500 })).length },
  collectors: { label: "Collectors not online", href: "/collectors",
    fetch: async () => (await api.listCollectors()).filter((c) => c.status !== "online").length },
  in_use: { label: "Rooms in use now", href: "/dashboards", good: true,
    fetch: async () => (await api.roomsInUse()).length },
};

function BigNumberWidget({ config }: { config: WidgetConfig }) {
  const metric = BIG_NUMBERS[str(config, "metric", "offline")] ?? BIG_NUMBERS.offline;
  const key = str(config, "metric", "offline");
  const threshold = num(config, "threshold", 1);
  const state = usePolling(() => metric.fetch(), REFRESH_MS, [key]);
  const label = str(config, "label").trim() || metric.label;
  return (
    <Load state={state}>
      {(n) => {
        const alarm = !metric.good && n >= threshold;
        return (
          <Link href={metric.href} className="flex h-full flex-col items-center justify-center py-2 text-center hover:opacity-80">
            <div
              className={cn(
                "text-6xl font-bold tabular-nums leading-none",
                alarm ? "[color:hsl(var(--destructive))]" : metric.good ? "text-emerald-600" : "text-foreground"
              )}
            >
              {n}
            </div>
            <div className="mt-2 text-sm uppercase tracking-wide text-muted-foreground">{label}</div>
          </Link>
        );
      }}
    </Load>
  );
}

function InUseWidget({ config }: { config: WidgetConfig }) {
  const limit = num(config, "limit", 10);
  const state = usePolling((s) => api.roomsInUse(s), REFRESH_MS);
  return (
    <Load state={state}>
      {(rooms) =>
        rooms.length === 0 ? (
          <Empty>No rooms are in a call or occupied right now.</Empty>
        ) : (
          <>
            <div className="mb-1 text-xs text-muted-foreground">{rooms.length} in use</div>
            <ul className="space-y-0.5">
              {rooms.slice(0, limit).map((r) => (
                <Row
                  key={r.room_id}
                  href={devicesHref({ building: r.building_name || undefined, room: r.room_name })}
                  right={[r.in_call && "In a call", r.people != null && `${r.people} people`].filter(Boolean).join(" · ")}
                >
                  <span className="mr-1.5 inline-block h-2 w-2 rounded-full bg-emerald-500 align-middle" />
                  <span className="font-medium">{r.room_name}</span>
                  {r.building_name && <span className="text-muted-foreground"> · {r.building_name}</span>}
                </Row>
              ))}
            </ul>
          </>
        )
      }
    </Load>
  );
}

// Worst-first, matching the Map and Overview pages.
function worstOf(devs: DeviceSummary[]): DeviceStatus {
  if (devs.some((d) => d.status === "offline")) return "offline";
  if (devs.some((d) => d.status === "unknown")) return "unknown";
  if (devs.some((d) => d.status === "switched_off")) return "switched_off";
  return "online";
}

function BuildingsWidget({ config }: { config: WidgetConfig }) {
  const region = str(config, "region");
  const state = usePolling(() => sharedDevices(), REFRESH_MS);
  return (
    <Load state={state}>
      {(all) => {
        const by = new Map<string, DeviceSummary[]>();
        for (const d of all) {
          if (!d.building || (region && d.region !== region)) continue;
          if (!by.has(d.building)) by.set(d.building, []);
          by.get(d.building)!.push(d);
        }
        const tiles = Array.from(by.entries()).sort(([a], [b]) => a.localeCompare(b));
        if (tiles.length === 0) return <Empty>No buildings with devices.</Empty>;
        return (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(9rem,1fr))] gap-2">
            {tiles.map(([name, devs]) => {
              const worst = worstOf(devs);
              const down = devs.filter((d) => d.status === "offline").length;
              const quiet = devs.filter((d) => d.status === "unknown").length;
              return (
                <Link
                  key={name}
                  href={devicesHref({ building: name })}
                  className={cn(
                    "rounded-md border-l-4 bg-background/60 px-2.5 py-2 hover:opacity-80",
                    worst === "offline" && "border-destructive bg-destructive/10",
                    worst === "unknown" && "border-amber-500 bg-amber-500/10",
                    worst === "switched_off" && "border-sky-500",
                    worst === "online" && "border-emerald-500"
                  )}
                >
                  <div className="truncate text-sm font-semibold">{name}</div>
                  <div className="text-xs text-muted-foreground">
                    {down || quiet
                      ? [down && `${down} offline`, quiet && `${quiet} not reporting`].filter(Boolean).join(" · ")
                      : `${devs.length} devices OK`}
                  </div>
                </Link>
              );
            })}
          </div>
        );
      }}
    </Load>
  );
}

const BuildingsMap = dynamic(() => import("@/components/buildings-map").then((m) => m.BuildingsMap), {
  ssr: false,
  loading: () => <div className="h-80 animate-pulse rounded-md bg-muted/60" />,
});

const MAPBOX_TOKEN = process.env.NEXT_PUBLIC_MAPBOX_TOKEN ?? "";

function MapWidget() {
  const devices = usePolling(() => sharedDevices(), REFRESH_MS);
  const buildings = usePolling<BuildingRow[]>((s) => api.listBuildings(s), 5 * 60_000);
  const entries = useMemo<BuildingsMapEntry[]>(() => {
    if (!devices.data || !buildings.data) return [];
    const byName = new Map<string, DeviceSummary[]>();
    for (const d of devices.data) {
      if (!d.building) continue;
      const k = d.building.toLowerCase();
      if (!byName.has(k)) byName.set(k, []);
      byName.get(k)!.push(d);
    }
    return buildings.data
      .map<BuildingsMapEntry>((b) => {
        const devs = byName.get(b.name.toLowerCase()) ?? [];
        return {
          building: b,
          worst: worstOf(devs),
          totals: {
            total: devs.length,
            online: devs.filter((d) => d.status === "online").length,
            offline: devs.filter((d) => d.status === "offline").length,
            unknown: devs.filter((d) => d.status === "unknown").length,
          },
        };
      })
      .filter((e) => e.totals.total > 0);
  }, [devices.data, buildings.data]);
  if (!MAPBOX_TOKEN) return <Empty>The map isn&apos;t set up on this portal.</Empty>;
  return (
    <Load state={{ data: devices.data && buildings.data ? entries : null, error: devices.error ?? buildings.error }}>
      {(e) => <BuildingsMap entries={e} mapboxToken={MAPBOX_TOKEN} className="h-80 min-h-[18rem] w-full rounded-md" />}
    </Load>
  );
}

// ── Catalogue ────────────────────────────────────────────────────────────

export const WIDGETS: WidgetDef[] = [
  {
    type: "status", title: "Estate status", icon: Radio, defaultSize: "m",
    description: "Devices online, offline, unknown and switched off.",
    fields: [BUILDING_FIELD],
    heading: (c) => (str(c, "building") ? `Estate status · ${str(c, "building")}` : undefined),
    render: (c) => <StatusWidget config={c} />,
  },
  {
    type: "alerts", title: "Open alerts", icon: Bell, defaultSize: "l",
    description: "The newest open alerts, by severity.",
    fields: [
      { key: "severity", label: "Severity", kind: "select", default: "any",
        options: [{ value: "any", label: "All" }, { value: "warning", label: "Warning and critical" }, { value: "critical", label: "Critical only" }] },
      LIMIT_FIELD,
    ],
    render: (c) => <AlertsWidget config={c} />,
  },
  {
    type: "attention", title: "Rooms needing attention", icon: AlertTriangle, defaultSize: "m",
    description: "Rooms with devices offline or not reporting.",
    fields: [BUILDING_FIELD, LIMIT_FIELD],
    render: (c) => <AttentionWidget config={c} />,
  },
  {
    type: "readiness", title: "Room Readiness, last 24 hours", icon: Moon, defaultSize: "m",
    description: "Power schedule and readiness check results, with failures.",
    fields: [
      { key: "kind", label: "Runs", kind: "select", default: "any",
        options: [{ value: "any", label: "All" }, { value: "power", label: "Power schedule" }, { value: "readiness", label: "Readiness checks" }] },
      LIMIT_FIELD,
    ],
    render: (c) => <ReadinessWidget config={c} />,
  },
  {
    type: "usage", title: "Room usage", icon: BarChart3, defaultSize: "m",
    description: "Busiest or least-used rooms, from calls and people counts.",
    fields: [
      { key: "mode", label: "Show", kind: "select", default: "busiest",
        options: [{ value: "busiest", label: "Busiest rooms" }, { value: "least", label: "Least-used rooms" }] },
      DAYS_FIELD, LIMIT_FIELD,
    ],
    heading: (c) => (str(c, "mode") === "least" ? "Least-used rooms" : "Busiest rooms"),
    render: (c) => <UsageWidget config={c} />,
  },
  {
    type: "uptime", title: "Lowest uptime", icon: Gauge, defaultSize: "m",
    description: "Devices with the lowest uptime over a period.",
    fields: [DAYS_FIELD, LIMIT_FIELD],
    render: (c) => <UptimeWidget config={c} />,
  },
  {
    type: "collectors", title: "Collectors", icon: Server, defaultSize: "s",
    description: "Collectors online, and any that aren't.",
    fields: [],
    render: () => <CollectorsWidget />,
  },
  {
    type: "warranty", title: "Warranty", icon: CalendarClock, defaultSize: "m",
    description: "Assets out of warranty or expiring within 90 days.",
    fields: [LIMIT_FIELD],
    render: (c) => <WarrantyWidget config={c} />,
  },
  {
    type: "firmware", title: "Firmware", icon: Cpu, defaultSize: "m",
    description: "Devices off their target firmware version.",
    fields: [LIMIT_FIELD],
    render: (c) => <FirmwareWidget config={c} />,
  },
  {
    type: "room", title: "Room", icon: DoorOpen, defaultSize: "s",
    description: "Every device in one room, with its status.",
    fields: [{ key: "room", label: "Room", kind: "room" }],
    heading: (c) => str(c, "room_name") || undefined,
    render: (c) => <RoomWidget config={c} />,
  },
  {
    type: "device", title: "Device", icon: ShieldAlert, defaultSize: "s",
    description: "One device's status and latest readings.",
    fields: [{ key: "device", label: "Device", kind: "device" }],
    heading: (c) => str(c, "device_name") || undefined,
    render: (c) => <DeviceWidget config={c} />,
  },
  {
    type: "activity", title: "Recent activity", icon: Activity, defaultSize: "m",
    description: "The latest changes and commands from the audit log.",
    fields: [
      { key: "limit", label: "Show", kind: "select", default: "10",
        options: [{ value: "5", label: "5" }, { value: "10", label: "10" }, { value: "20", label: "20" }] },
    ],
    render: (c) => <ActivityWidget config={c} />,
  },
  {
    type: "clock", title: "Clock", icon: Clock, defaultSize: "s",
    description: "The time and date, in any time zone.",
    fields: [
      { key: "tz", label: "Time zone", kind: "select", default: "Europe/London",
        options: [
          { value: "Europe/London", label: "UK (London)" },
          { value: "UTC", label: "UTC" },
          { value: "Europe/Dublin", label: "Ireland (Dublin)" },
          { value: "Europe/Paris", label: "Central Europe (Paris)" },
          { value: "America/New_York", label: "US Eastern (New York)" },
          { value: "America/Los_Angeles", label: "US Pacific (Los Angeles)" },
          { value: "Asia/Dubai", label: "Gulf (Dubai)" },
          { value: "Asia/Singapore", label: "Singapore" },
          { value: "Australia/Sydney", label: "Australia (Sydney)" },
        ] },
      { key: "format", label: "Format", kind: "select", default: "24",
        options: [{ value: "24", label: "24-hour" }, { value: "12", label: "12-hour" }] },
      { key: "seconds", label: "Seconds", kind: "select", default: "on",
        options: [{ value: "on", label: "Show" }, { value: "off", label: "Hide" }] },
    ],
    render: (c) => <ClockWidget config={c} />,
  },
  {
    type: "notice", title: "Notice", icon: Megaphone, defaultSize: "m",
    description: "A message for everyone watching, such as a change freeze or a room out of use.",
    fields: [
      { key: "text", label: "Message", kind: "text", multiline: true, maxLength: 500,
        placeholder: "Court 3 out of use until Monday" },
      { key: "tone", label: "Style", kind: "select", default: "info",
        options: [{ value: "info", label: "Information" }, { value: "warning", label: "Warning" }, { value: "critical", label: "Critical" }] },
    ],
    heading: () => "Notice",
    render: (c) => <NoticeWidget config={c} />,
  },
  {
    type: "bignumber", title: "Big number", icon: Hash, defaultSize: "s",
    description: "One figure in large type, red once it reaches a threshold.",
    fields: [
      { key: "metric", label: "Figure", kind: "select", default: "offline",
        options: Object.entries(BIG_NUMBERS).map(([value, m]) => ({ value, label: m.label })) },
      { key: "threshold", label: "Red from", kind: "number", default: 1, min: 1, max: 100000 },
      { key: "label", label: "Label (optional)", kind: "text", maxLength: 60 },
    ],
    heading: (c) => (BIG_NUMBERS[str(c, "metric", "offline")] ?? BIG_NUMBERS.offline).label,
    render: (c) => <BigNumberWidget config={c} />,
  },
  {
    type: "inuse", title: "Rooms in use now", icon: Users, defaultSize: "m",
    description: "Rooms in a call or with people in them right now.",
    fields: [LIMIT_FIELD],
    render: (c) => <InUseWidget config={c} />,
  },
  {
    type: "buildings", title: "Building status", icon: Building2, defaultSize: "l",
    description: "Every building as a tile, coloured by its worst device.",
    fields: [{ key: "region", label: "Region", kind: "region", optional: true }],
    heading: (c) => (str(c, "region") ? `Building status · ${str(c, "region")}` : undefined),
    render: (c) => <BuildingsWidget config={c} />,
  },
  {
    type: "map", title: "Map", icon: MapIcon, defaultSize: "l",
    description: "Buildings on a map, coloured by status.",
    fields: [],
    render: () => <MapWidget />,
  },
];

export const WIDGET_BY_TYPE: Record<string, WidgetDef> = Object.fromEntries(WIDGETS.map((w) => [w.type, w]));

export const SIZE_LABEL: Record<DashboardWidgetSize, string> = { s: "Small", m: "Medium", l: "Large", xl: "Full width" };

// Grid spans: one column on phones, two on tablets, 12 on desktops.
export const SIZE_CLASS: Record<DashboardWidgetSize, string> = {
  s: "md:col-span-1 lg:col-span-3",
  m: "md:col-span-1 lg:col-span-4",
  l: "md:col-span-2 lg:col-span-6",
  xl: "md:col-span-2 lg:col-span-12",
};
