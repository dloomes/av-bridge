"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  Download,
  LineChart,
  RefreshCcw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type { DeviceUptimeRow, RoomActivityRow } from "@/lib/types";

type Tab = "uptime" | "activity";
type Window = 1 | 7 | 30 | 90;

const WINDOWS: Window[] = [1, 7, 30, 90];

const TAB_LABEL: Record<Tab, string> = {
  uptime: "Device uptime",
  activity: "Room activity",
};

export default function ReportsPage() {
  const [tab, setTab] = useState<Tab>("uptime");
  const [days, setDays] = useState<Window>(7);

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div>
          <h1 className="text-xl font-semibold">Reports</h1>
          <p className="text-sm text-muted-foreground">
            Device uptime and room activity over the selected window
          </p>
        </div>
        <UserMenu />
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex gap-1 border-b">
            {(Object.keys(TAB_LABEL) as Tab[]).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setTab(t)}
                className={`px-3 py-2 text-sm border-b-2 -mb-px ${
                  tab === t
                    ? "border-foreground font-semibold"
                    : "border-transparent text-muted-foreground hover:text-foreground"
                }`}
              >
                {TAB_LABEL[t]}
              </button>
            ))}
          </div>

          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">Window:</span>
            {WINDOWS.map((w) => (
              <button
                key={w}
                type="button"
                onClick={() => setDays(w)}
                className={`rounded-md border px-2 py-1 text-xs ${
                  days === w
                    ? "bg-foreground text-background border-foreground"
                    : "border-input hover:bg-accent/40"
                }`}
              >
                {w === 1 ? "24h" : `${w}d`}
              </button>
            ))}
            <Button asChild variant="outline" size="sm">
              <a
                href={api.reportCSVUrl(tab === "uptime" ? "device-uptime" : "room-activity", days)}
                download
              >
                <Download className="h-3.5 w-3.5" />
                CSV
              </a>
            </Button>
          </div>
        </div>

        {tab === "uptime" ? (
          <UptimeReport days={days} />
        ) : (
          <ActivityReport days={days} />
        )}
      </div>
    </div>
  );
}

// --- Device uptime ---------------------------------------------------------

function UptimeReport({ days }: { days: Window }) {
  const [rows, setRows] = useState<DeviceUptimeRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.deviceUptimeReport(days, signal);
      if (signal?.aborted) return;
      setRows(data);
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError((e as Error).message);
    }
  }, [days]);

  useEffect(() => {
    setRows(null);
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const summary = useMemo(() => {
    if (!rows) return { observed: 0, healthy: 0, degraded: 0, down: 0 };
    let observed = 0, healthy = 0, degraded = 0, down = 0;
    for (const r of rows) {
      if (r.uptime_pct == null) continue;
      observed++;
      if (r.uptime_pct >= 99) healthy++;
      else if (r.uptime_pct >= 90) degraded++;
      else down++;
    }
    return { observed, healthy, degraded, down };
  }, [rows]);

  if (error) {
    return (
      <Card className="border-destructive/30 bg-destructive/5">
        <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
          {error}
          <Button size="sm" variant="ghost" onClick={() => load()}>
            <RefreshCcw className="h-3 w-3" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  if (rows === null) {
    return <Skeleton className="h-96 w-full" />;
  }

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
        <SummaryCard label="Devices observed" value={summary.observed} />
        <SummaryCard label="≥ 99% uptime" value={summary.healthy} tone="ok" />
        <SummaryCard label="90–99%" value={summary.degraded} tone="warn" />
        <SummaryCard label="< 90%" value={summary.down} tone="bad" />
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/30 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Device</th>
                <th className="text-left px-3 py-2 font-medium">Location</th>
                <th className="text-right px-3 py-2 font-medium">Samples</th>
                <th className="text-right px-3 py-2 font-medium">Uptime</th>
                <th className="text-left px-3 py-2 font-medium">Current</th>
                <th className="text-left px-3 py-2 font-medium">Last seen</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.device_id} className="border-b last:border-b-0 hover:bg-accent/20">
                  <td className="px-3 py-2">
                    <Link
                      href={`/devices/${encodeURIComponent(r.device_id)}`}
                      className="hover:underline"
                    >
                      {r.name || r.device_id.slice(0, 8)}
                    </Link>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">{r.location || "—"}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.samples}</td>
                  <td className="px-3 py-2 text-right tabular-nums">
                    <UptimeBar pct={r.uptime_pct ?? null} />
                  </td>
                  <td className="px-3 py-2">
                    <StatusDot status={r.current_status} />
                  </td>
                  <td className="px-3 py-2 text-muted-foreground text-xs">
                    {r.last_seen_at ? formatRelative(r.last_seen_at) : "never"}
                  </td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-8 text-center text-muted-foreground text-sm">
                    No devices in this window.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      <p className="text-[11px] text-muted-foreground flex items-center gap-1">
        <LineChart className="h-3 w-3" />
        Uptime = share of telemetry polls in the window where the device reported "online".
        Devices with no samples (never polled) show "—".
      </p>
    </div>
  );
}

// --- Room activity ---------------------------------------------------------

function ActivityReport({ days }: { days: Window }) {
  const [rows, setRows] = useState<RoomActivityRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.roomActivityReport(days, signal);
      if (signal?.aborted) return;
      setRows(data);
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError((e as Error).message);
    }
  }, [days]);

  useEffect(() => {
    setRows(null);
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  // Top-events bar width scaled to the busiest room — gives an instant
  // visual ranking without pulling in a chart library.
  const maxEvents = useMemo(() => {
    if (!rows || rows.length === 0) return 1;
    return Math.max(1, ...rows.map((r) => r.event_count));
  }, [rows]);

  if (error) {
    return (
      <Card className="border-destructive/30 bg-destructive/5">
        <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
          {error}
          <Button size="sm" variant="ghost" onClick={() => load()}>
            <RefreshCcw className="h-3 w-3" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  if (rows === null) {
    return <Skeleton className="h-96 w-full" />;
  }

  const totalEvents = rows.reduce((n, r) => n + r.event_count, 0);
  const activeRooms = rows.filter((r) => r.event_count > 0).length;

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
        <SummaryCard label="Rooms with activity" value={`${activeRooms} / ${rows.length}`} />
        <SummaryCard label="Total events" value={totalEvents} />
        <SummaryCard label="Busiest room" value={rows[0]?.room_name ?? "—"} />
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/30 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Room</th>
                <th className="text-left px-3 py-2 font-medium">Building</th>
                <th className="text-right px-3 py-2 font-medium">Devices</th>
                <th className="text-right px-3 py-2 font-medium">Events</th>
                <th className="px-3 py-2 font-medium w-1/3">Activity</th>
                <th className="text-left px-3 py-2 font-medium">Last event</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.room_id} className="border-b last:border-b-0 hover:bg-accent/20">
                  <td className="px-3 py-2 font-medium">{r.room_name}</td>
                  <td className="px-3 py-2 text-muted-foreground">{r.building_name || "—"}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.device_count}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.event_count}</td>
                  <td className="px-3 py-2">
                    <div className="h-2 w-full rounded bg-muted overflow-hidden">
                      <div
                        className="h-full bg-foreground/80"
                        style={{ width: `${(r.event_count / maxEvents) * 100}%` }}
                      />
                    </div>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground text-xs">
                    {r.last_event_at ? formatRelative(r.last_event_at) : "—"}
                  </td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-8 text-center text-muted-foreground text-sm">
                    No rooms yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </div>
  );
}

// --- Shared bits -----------------------------------------------------------

function SummaryCard({
  label,
  value,
  tone,
}: {
  label: string;
  value: string | number;
  tone?: "ok" | "warn" | "bad";
}) {
  const toneClass =
    tone === "ok"
      ? "text-emerald-600"
      : tone === "warn"
      ? "text-amber-600"
      : tone === "bad"
      ? "text-red-600"
      : "";
  return (
    <Card>
      <CardContent className="p-3">
        <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
        <div className={`mt-0.5 text-xl font-semibold ${toneClass}`}>{value}</div>
      </CardContent>
    </Card>
  );
}

function UptimeBar({ pct }: { pct: number | null }) {
  if (pct == null) return <span className="text-muted-foreground">—</span>;
  const tone =
    pct >= 99 ? "bg-emerald-500" : pct >= 90 ? "bg-amber-500" : "bg-red-500";
  return (
    <div className="flex items-center gap-2 justify-end">
      <div className="h-1.5 w-20 rounded-full bg-muted overflow-hidden">
        <div className={`h-full ${tone}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-xs tabular-nums w-12 text-right">{pct.toFixed(1)}%</span>
    </div>
  );
}

function StatusDot({ status }: { status: string }) {
  const map: Record<string, string> = {
    online: "bg-emerald-500",
    offline: "bg-red-500",
    degraded: "bg-amber-500",
    unknown: "bg-muted-foreground/40",
  };
  return (
    <span className="inline-flex items-center gap-1.5 text-xs">
      <span className={`h-2 w-2 rounded-full ${map[status] ?? map.unknown}`} />
      {status}
    </span>
  );
}
