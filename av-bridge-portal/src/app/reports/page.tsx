"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  Download,
  LineChart,
  RefreshCcw,
  ShieldAlert,
  Clock,
  Zap,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type {
  DeviceUptimeRow,
  PowerRow,
  RoomActivityRow,
  RoomUtilisationRow,
  WarrantyBucket,
  WarrantyRow,
} from "@/lib/types";

type Tab = "uptime" | "activity" | "utilisation" | "warranty" | "power";
type Window = 1 | 7 | 30 | 90;

const WINDOWS: Window[] = [1, 7, 30, 90];

const TAB_LABEL: Record<Tab, string> = {
  uptime: "Device uptime",
  activity: "Room activity",
  utilisation: "Room utilisation",
  warranty: "Warranty",
  power: "Power",
};

const TAB_CSV_KIND: Record<Tab, "device-uptime" | "room-activity" | "warranty" | "room-utilisation" | "power"> = {
  uptime: "device-uptime",
  activity: "room-activity",
  utilisation: "room-utilisation",
  warranty: "warranty",
  power: "power",
};

// Warranty is a lifetime state, not windowed — the window picker hides for it.
const TAB_HAS_WINDOW: Record<Tab, boolean> = {
  uptime: true,
  activity: true,
  utilisation: true,
  warranty: false,
  power: true,
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
            Uptime, activity, utilisation, warranty and power over the selected window
          </p>
        </div>
        <UserMenu />
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex gap-1 border-b flex-wrap">
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
            {TAB_HAS_WINDOW[tab] && (
              <>
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
              </>
            )}
            <Button asChild variant="outline" size="sm">
              <a
                href={api.reportCSVUrl(TAB_CSV_KIND[tab], days)}
                download
              >
                <Download className="h-3.5 w-3.5" />
                CSV
              </a>
            </Button>
          </div>
        </div>

        {tab === "uptime" && <UptimeReport days={days} />}
        {tab === "activity" && <ActivityReport days={days} />}
        {tab === "utilisation" && <UtilisationReport days={days} />}
        {tab === "warranty" && <WarrantyReport />}
        {tab === "power" && <PowerReport days={days} />}
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
    if (!rows) return { observed: 0, healthy: 0, down: 0 };
    let observed = 0, healthy = 0, down = 0;
    for (const r of rows) {
      if (r.uptime_pct == null) continue;
      observed++;
      if (r.uptime_pct >= 99) healthy++;
      else down++;
    }
    return { observed, healthy, down };
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
      <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
        <SummaryCard label="Devices observed" value={summary.observed} />
        <SummaryCard label="≥ 99% uptime" value={summary.healthy} tone="ok" />
        <SummaryCard label="< 99% uptime" value={summary.down} tone="bad" />
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

// --- Room utilisation ------------------------------------------------------

function UtilisationReport({ days }: { days: Window }) {
  const [rows, setRows] = useState<RoomUtilisationRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.roomUtilisationReport(days, signal);
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

  const maxHours = useMemo(() => {
    if (!rows || rows.length === 0) return 1;
    return Math.max(1, ...rows.map((r) => r.avg_hours_per_day));
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

  if (rows === null) return <Skeleton className="h-96 w-full" />;

  const active = rows.filter((r) => r.active_hours > 0);
  const totalActiveHours = active.reduce((n, r) => n + r.active_hours, 0);
  const busiest = rows[0];

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
        <SummaryCard label="Rooms with activity" value={`${active.length} / ${rows.length}`} />
        <SummaryCard label="Total active hours" value={totalActiveHours} />
        <SummaryCard
          label="Avg hours/day (busiest)"
          value={busiest ? busiest.avg_hours_per_day.toFixed(2) : "—"}
          tone="ok"
        />
        <SummaryCard
          label="Idle rooms"
          value={rows.length - active.length}
          tone={rows.length - active.length > 0 ? "warn" : undefined}
        />
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/30 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Room</th>
                <th className="text-left px-3 py-2 font-medium">Building</th>
                <th className="text-right px-3 py-2 font-medium">Devices</th>
                <th className="text-right px-3 py-2 font-medium">Active hours</th>
                <th className="text-right px-3 py-2 font-medium">Active days</th>
                <th className="text-right px-3 py-2 font-medium">Avg hrs/day</th>
                <th className="px-3 py-2 font-medium w-1/4">Utilisation</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.room_id} className="border-b last:border-b-0 hover:bg-accent/20">
                  <td className="px-3 py-2 font-medium">{r.room_name}</td>
                  <td className="px-3 py-2 text-muted-foreground">{r.building_name || "—"}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.device_count}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.active_hours}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.active_days}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.avg_hours_per_day.toFixed(2)}</td>
                  <td className="px-3 py-2">
                    <div className="h-2 w-full rounded bg-muted overflow-hidden">
                      <div
                        className="h-full bg-foreground/80"
                        style={{ width: `${(r.avg_hours_per_day / maxHours) * 100}%` }}
                      />
                    </div>
                  </td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-3 py-8 text-center text-muted-foreground text-sm">
                    No rooms yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      <p className="text-[11px] text-muted-foreground flex items-center gap-1">
        <Clock className="h-3 w-3" />
        Active hour = at least one device event in the room during that clock hour.
        Averaged over the window to give a comparable hours-per-day figure.
      </p>
    </div>
  );
}

// --- Warranty --------------------------------------------------------------

const BUCKET_LABEL: Record<WarrantyBucket, string> = {
  expired: "Expired",
  lt_30d: "≤ 30 days",
  lt_90d: "≤ 90 days",
  lt_365d: "≤ 365 days",
  later: "Later",
  no_date: "No date",
};

function bucketToneClass(b: WarrantyBucket): string {
  switch (b) {
    case "expired": return "bg-red-500/15 text-red-600 border-red-500/30";
    case "lt_30d":  return "bg-red-500/10 text-red-600 border-red-500/25";
    case "lt_90d":  return "bg-amber-500/15 text-amber-700 border-amber-500/30";
    case "lt_365d": return "bg-amber-500/5 text-amber-700/80 border-amber-500/20";
    case "no_date": return "bg-muted text-muted-foreground border-muted";
    default:        return "bg-emerald-500/10 text-emerald-700 border-emerald-500/25";
  }
}

function WarrantyReport() {
  const [rows, setRows] = useState<WarrantyRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.warrantyReport(signal);
      if (signal?.aborted) return;
      setRows(data);
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    setRows(null);
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const buckets = useMemo(() => {
    const b: Record<WarrantyBucket, number> = {
      expired: 0, lt_30d: 0, lt_90d: 0, lt_365d: 0, later: 0, no_date: 0,
    };
    if (!rows) return b;
    for (const r of rows) b[r.bucket]++;
    return b;
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

  if (rows === null) return <Skeleton className="h-96 w-full" />;

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-6 gap-2">
        <SummaryCard label="Expired" value={buckets.expired} tone={buckets.expired > 0 ? "bad" : undefined} />
        <SummaryCard label="≤ 30 days" value={buckets.lt_30d} tone={buckets.lt_30d > 0 ? "bad" : undefined} />
        <SummaryCard label="≤ 90 days" value={buckets.lt_90d} tone={buckets.lt_90d > 0 ? "warn" : undefined} />
        <SummaryCard label="≤ 365 days" value={buckets.lt_365d} />
        <SummaryCard label="Later" value={buckets.later} tone="ok" />
        <SummaryCard label="No date" value={buckets.no_date} />
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/30 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Asset</th>
                <th className="text-left px-3 py-2 font-medium">Category</th>
                <th className="text-left px-3 py-2 font-medium">Make / model</th>
                <th className="text-left px-3 py-2 font-medium">Location</th>
                <th className="text-left px-3 py-2 font-medium">Warranty end</th>
                <th className="text-right px-3 py-2 font-medium">Days left</th>
                <th className="text-left px-3 py-2 font-medium">Bucket</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const makeModel = [r.manufacturer, r.model].filter(Boolean).join(" / ") || "—";
                return (
                  <tr key={r.asset_id} className="border-b last:border-b-0 hover:bg-accent/20">
                    <td className="px-3 py-2">
                      <Link
                        href={`/assets/${encodeURIComponent(r.asset_id)}`}
                        className="font-medium hover:underline"
                      >
                        {r.name}
                      </Link>
                      {r.asset_tag && (
                        <span className="ml-2 text-xs text-muted-foreground">{r.asset_tag}</span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-muted-foreground">{r.category}</td>
                    <td className="px-3 py-2 text-muted-foreground">{makeModel}</td>
                    <td className="px-3 py-2 text-muted-foreground">{r.location || "—"}</td>
                    <td className="px-3 py-2 text-muted-foreground">{r.warranty_end || "—"}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                      {r.days_remaining ?? "—"}
                    </td>
                    <td className="px-3 py-2">
                      <span className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[11px] ${bucketToneClass(r.bucket)}`}>
                        {BUCKET_LABEL[r.bucket]}
                      </span>
                    </td>
                  </tr>
                );
              })}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-3 py-8 text-center text-muted-foreground text-sm">
                    No assets in the catalogue.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      <p className="text-[11px] text-muted-foreground flex items-center gap-1">
        <ShieldAlert className="h-3 w-3" />
        Retired assets are excluded. Populate warranty dates via the Assets page or the CSV import.
      </p>
    </div>
  );
}

// --- Power -----------------------------------------------------------------

function PowerReport({ days }: { days: Window }) {
  const [rows, setRows] = useState<PowerRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.powerReport(days, signal);
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

  if (rows === null) return <Skeleton className="h-96 w-full" />;

  const totalConsumed = rows.reduce((n, r) => n + r.consumed_kwh, 0);
  const totalSaved = rows.reduce((n, r) => n + r.saved_kwh, 0);
  const totalRated = rows.reduce((n, r) => n + r.rated_devices, 0);
  const totalUnrated = rows.reduce((n, r) => n + r.unrated_devices, 0);

  const maxConsumed = Math.max(1, ...rows.map((r) => r.consumed_kwh));

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
        <SummaryCard label="Consumed (kWh)" value={totalConsumed.toFixed(1)} />
        <SummaryCard label="Saved via nightly (kWh)" value={totalSaved.toFixed(1)} tone="ok" />
        <SummaryCard label="Rated devices" value={totalRated} />
        <SummaryCard
          label="Unrated devices"
          value={totalUnrated}
          tone={totalUnrated > 0 ? "warn" : undefined}
        />
      </div>

      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/30 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Room</th>
                <th className="text-left px-3 py-2 font-medium">Building</th>
                <th className="text-right px-3 py-2 font-medium">Rated</th>
                <th className="text-right px-3 py-2 font-medium">Unrated</th>
                <th className="text-right px-3 py-2 font-medium">Consumed (kWh)</th>
                <th className="text-right px-3 py-2 font-medium">Saved (kWh)</th>
                <th className="text-right px-3 py-2 font-medium">Nightly runs</th>
                <th className="px-3 py-2 font-medium w-1/4">Consumed</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.room_id} className="border-b last:border-b-0 hover:bg-accent/20">
                  <td className="px-3 py-2 font-medium">{r.room_name}</td>
                  <td className="px-3 py-2 text-muted-foreground">{r.building_name || "—"}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.rated_devices}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                    {r.unrated_devices}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums">{r.consumed_kwh.toFixed(2)}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-emerald-600">
                    {r.saved_kwh > 0 ? r.saved_kwh.toFixed(2) : "—"}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                    {r.nightly_runs}
                  </td>
                  <td className="px-3 py-2">
                    <div className="h-2 w-full rounded bg-muted overflow-hidden">
                      <div
                        className="h-full bg-foreground/80"
                        style={{ width: `${(r.consumed_kwh / maxConsumed) * 100}%` }}
                      />
                    </div>
                  </td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={8} className="px-3 py-8 text-center text-muted-foreground text-sm">
                    No rooms yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      <p className="text-[11px] text-muted-foreground flex items-center gap-1">
        <Zap className="h-3 w-3" />
        Consumed = uptime × on-watts + downtime × standby-watts (kWh). Saved = successful nightly runs × avg off-hours × (on − standby). Only devices with a nameplate power rating contribute; unrated devices are shown so you can populate them via the device edit form.
      </p>
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
  const tone = pct >= 99 ? "bg-emerald-500" : "bg-red-500";
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
    unknown: "bg-muted-foreground/40",
  };
  return (
    <span className="inline-flex items-center gap-1.5 text-xs">
      <span className={`h-2 w-2 rounded-full ${map[status] ?? map.unknown}`} />
      {status}
    </span>
  );
}
