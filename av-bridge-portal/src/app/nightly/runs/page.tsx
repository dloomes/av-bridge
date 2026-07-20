"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  ArrowLeft,
  CheckCircle2,
  ChevronRight,
  Loader2,
  Moon,
  RefreshCcw,
  XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type {
  NightlyRoomRow,
  NightlyRunRow,
  NightlyStatus,
} from "@/lib/api";

// Room Readiness — run history list.
//
// Slice 4. Consumes GET /api/v1/nightly/runs; drill-down goes to the
// detail page. Table (not heatmap) for MVP — filters + status column are
// enough for the current fleet size and let the layout stay responsive
// on narrow viewports. A per-room-per-date heatmap can layer on later
// once fleets grow past the point where the table view scrolls forever.

type StatusFilter = "all" | "failed" | NightlyStatus;

const STATUS_OPTIONS: { key: StatusFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "in_progress", label: "In progress" },
  { key: "succeeded", label: "Succeeded" },
  { key: "failed", label: "Failed" },
];

function statusBadge(s: NightlyStatus) {
  switch (s) {
    case "succeeded":
      return (
        <Badge variant="success" className="uppercase text-[10px]">
          Ready
        </Badge>
      );
    case "failed":
      return (
        <Badge variant="destructive" className="uppercase text-[10px]">
          Failed
        </Badge>
      );
    case "in_progress":
      return (
        <Badge variant="secondary" className="uppercase text-[10px]">
          In progress
        </Badge>
      );
    case "pending":
      return (
        <Badge variant="secondary" className="uppercase text-[10px]">
          Pending
        </Badge>
      );
    case "skipped":
      return (
        <Badge variant="secondary" className="uppercase text-[10px]">
          Skipped
        </Badge>
      );
  }
}

// Compact "3m 24s" formatter — clearer than raw seconds once past a minute.
function fmtDuration(seconds: number | undefined): string {
  if (seconds === undefined) return "—";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s === 0 ? `${m}m` : `${m}m ${s}s`;
}

// Default from = 7 days ago, at midnight UTC so the picker resolves
// cleanly. Format as YYYY-MM-DD for the date input.
function isoDaysAgo(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() - days);
  return d.toISOString().slice(0, 10);
}

function toRFC3339Start(dateStr: string): string {
  return `${dateStr}T00:00:00Z`;
}

function toRFC3339End(dateStr: string): string {
  // Exclusive upper bound. The server treats `to` as exclusive; we pick
  // the START of the next day so an "including today" picker works.
  const d = new Date(dateStr + "T00:00:00Z");
  d.setDate(d.getDate() + 1);
  return d.toISOString().slice(0, 10) + "T00:00:00Z";
}

export default function NightlyRunsPage() {
  const [from, setFrom] = useState(isoDaysAgo(7));
  const [to, setTo] = useState(isoDaysAgo(0));
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [roomFilter, setRoomFilter] = useState<string>("");

  const [runs, setRuns] = useState<NightlyRunRow[] | null>(null);
  const [rooms, setRooms] = useState<NightlyRoomRow[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const loadRuns = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listNightlyRuns(
        {
          from: toRFC3339Start(from),
          to: toRFC3339End(to),
          room_id: roomFilter || undefined,
          status: statusFilter === "all" ? undefined : statusFilter,
          limit: 500,
        },
        signal
      );
      if (signal?.aborted) return;
      setRuns(list);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, [from, to, statusFilter, roomFilter]);

  useEffect(() => {
    const ctrl = new AbortController();
    void loadRuns(ctrl.signal);
    return () => ctrl.abort();
  }, [loadRuns]);

  // Load room list once — populates the room filter dropdown. Uses the
  // existing nightly-rooms endpoint (slice 2A) so the picker only shows
  // rooms actually visible under the caller's scope.
  useEffect(() => {
    const ctrl = new AbortController();
    api
      .listNightlyRooms(ctrl.signal)
      .then((rs) => {
        if (!ctrl.signal.aborted) setRooms(rs);
      })
      .catch(() => {
        // non-fatal — filter dropdown just shows "All rooms" only
      });
    return () => ctrl.abort();
  }, []);

  const refresh = async () => {
    setRefreshing(true);
    try {
      await loadRuns();
    } finally {
      setRefreshing(false);
    }
  };

  // Stats over the current filter — computed client-side to avoid a
  // second round-trip.
  const stats = useMemo(() => {
    if (!runs) return null;
    const succeeded = runs.filter((r) => r.status === "succeeded").length;
    const failed = runs.filter((r) => r.status === "failed").length;
    const inProgress = runs.filter((r) => r.status === "in_progress").length;
    return { total: runs.length, succeeded, failed, inProgress };
  }, [runs]);

  return (
    <div className="flex flex-col h-screen">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-2.5">
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
            <Moon aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <Link
                href="/nightly/schedule"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ArrowLeft aria-hidden="true" className="h-3 w-3" />
                Room Readiness
              </Link>
            </div>
            <h1 className="text-xl font-semibold leading-tight">Run history</h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Every scheduled nightly cycle, most recent first.
            </p>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={refresh}
            disabled={refreshing}
            aria-label="Refresh runs"
          >
            <RefreshCcw
              aria-hidden="true"
              className={`h-4 w-4 ${refreshing ? "animate-spin" : ""}`}
            />
            Refresh
          </Button>
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-5xl space-y-4">
          {/* ── Filters ────────────────────────────────────────────────── */}
          <Card>
            <CardContent className="p-4 flex flex-wrap items-end gap-3">
              <div className="space-y-1">
                <label htmlFor="from" className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  From
                </label>
                <input
                  id="from"
                  type="date"
                  value={from}
                  onChange={(e) => setFrom(e.target.value)}
                  max={to}
                  className="rounded-md border bg-background px-3 py-2 text-sm"
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="to" className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  To
                </label>
                <input
                  id="to"
                  type="date"
                  value={to}
                  onChange={(e) => setTo(e.target.value)}
                  min={from}
                  className="rounded-md border bg-background px-3 py-2 text-sm"
                />
              </div>
              <div className="space-y-1 flex-1 min-w-[180px]">
                <label htmlFor="room" className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  Room
                </label>
                <select
                  id="room"
                  value={roomFilter}
                  onChange={(e) => setRoomFilter(e.target.value)}
                  className="w-full rounded-md border bg-background px-3 py-2 text-sm"
                >
                  <option value="">All rooms</option>
                  {rooms.map((r) => (
                    <option key={r.room_id} value={r.room_id}>
                      {r.room_name} ({r.building_name})
                    </option>
                  ))}
                </select>
              </div>
              <div className="space-y-1">
                <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground block">
                  Status
                </span>
                <div className="flex flex-wrap gap-1">
                  {STATUS_OPTIONS.map((s) => (
                    <button
                      key={s.key}
                      type="button"
                      aria-pressed={statusFilter === s.key}
                      onClick={() => setStatusFilter(s.key)}
                      className={`rounded-md border px-3 py-2 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                        statusFilter === s.key
                          ? "bg-primary text-primary-foreground border-primary"
                          : "bg-background hover:bg-accent"
                      }`}
                    >
                      {s.label}
                    </button>
                  ))}
                </div>
              </div>
            </CardContent>
          </Card>

          {/* ── Stats ─────────────────────────────────────────────────── */}
          {stats && (
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
              <StatCard label="Total runs" value={stats.total} tone="default" />
              <StatCard
                label="Ready"
                value={stats.succeeded}
                tone="success"
              />
              <StatCard label="Failed" value={stats.failed} tone="destructive" />
              <StatCard
                label="In progress"
                value={stats.inProgress}
                tone="secondary"
              />
            </div>
          )}

          {loadError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </div>
          )}

          {runs === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading runs…
            </div>
          )}

          {runs !== null && runs.length === 0 && (
            <Card>
              <CardContent className="p-10 text-center space-y-3">
                <div
                  aria-hidden="true"
                  className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/10"
                >
                  <Moon className="h-6 w-6 [color:hsl(var(--primary))]" />
                </div>
                <div className="text-base font-semibold">
                  No runs in this window
                </div>
                <p className="mx-auto max-w-md text-sm text-muted-foreground">
                  Widen the date range, or enable the nightly lifecycle on
                  the{" "}
                  <Link
                    href="/nightly/schedule"
                    className="text-primary hover:underline"
                  >
                    schedule page
                  </Link>{" "}
                  if it isn't running yet.
                </p>
              </CardContent>
            </Card>
          )}

          {runs !== null && runs.length > 0 && (
            <Card>
              <CardContent className="p-0">
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[640px] text-sm">
                    <thead>
                      <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                        <th scope="col" className="px-4 py-3 font-medium">Room</th>
                        <th scope="col" className="px-4 py-3 font-medium">Scheduled</th>
                        <th scope="col" className="px-4 py-3 font-medium">Status</th>
                        <th scope="col" className="px-4 py-3 font-medium">Duration</th>
                        <th scope="col" className="px-4 py-3 font-medium">
                          <span className="sr-only">Details</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {runs.map((r) => (
                        <tr
                          key={r.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3.5 align-top">
                            <Link
                              href={`/nightly/runs/${r.id}`}
                              className="font-medium hover:underline"
                            >
                              {r.room_name}
                            </Link>
                            <div className="text-xs text-muted-foreground mt-0.5">
                              {[r.region_name, r.location_name, r.building_name]
                                .filter(Boolean)
                                .join(" · ")}
                            </div>
                          </td>
                          <td className="px-4 py-3.5 align-top text-xs text-muted-foreground">
                            {new Date(r.scheduled_at).toLocaleString()}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {statusBadge(r.status)}
                            <div className="text-[10px] text-muted-foreground mt-1 uppercase tracking-wide">
                              phase: {r.phase.replace(/_/g, " ")}
                            </div>
                            {r.failure_reason && (
                              <div className="text-xs [color:hsl(var(--destructive))] mt-1 max-w-xs">
                                {r.failure_reason}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top text-xs text-muted-foreground font-mono">
                            {fmtDuration(r.duration_seconds)}
                          </td>
                          <td className="px-4 py-3.5 align-top text-right">
                            <Link href={`/nightly/runs/${r.id}`}>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8"
                                aria-label={`View details for ${r.room_name}`}
                              >
                                View
                                <ChevronRight
                                  aria-hidden="true"
                                  className="h-3.5 w-3.5"
                                />
                              </Button>
                            </Link>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}

// StatCard — the summary tiles at the top of the runs list. Colour is a
// tone rather than raw tailwind so brand accent (--primary) picks it up.
function StatCard({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: "default" | "success" | "destructive" | "secondary";
}) {
  const border =
    tone === "success"
      ? "border-[color:hsl(var(--success))]/30 bg-[color:hsl(var(--success))]/5"
      : tone === "destructive"
        ? "border-destructive/30 bg-destructive/5"
        : tone === "secondary"
          ? "border-border bg-muted/20"
          : "border-border";
  const colour =
    tone === "success"
      ? "[color:hsl(var(--success))]"
      : tone === "destructive"
        ? "[color:hsl(var(--destructive))]"
        : "";
  const Icon =
    tone === "success" ? CheckCircle2 : tone === "destructive" ? XCircle : null;
  return (
    <div className={`rounded-md border p-3 ${border}`}>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        {Icon && <Icon aria-hidden="true" className={`h-3.5 w-3.5 ${colour}`} />}
        {label}
      </div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${colour}`}>
        {value}
      </div>
    </div>
  );
}
