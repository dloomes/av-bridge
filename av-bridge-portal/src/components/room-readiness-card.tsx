"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { CheckCircle2, ChevronRight, Loader2, Moon, XCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { NightlyRunRow, NightlyStatus, NightlyStepRow } from "@/lib/api";
import { formatRelative } from "@/lib/utils";

// Room Readiness for one room, shown on the Devices page when it's
// filtered to that room: the latest readiness check (and, when it failed,
// the steps that failed), the latest overnight power cycle, and a
// fortnight of checks at a glance.

const HISTORY_DAYS = 14;

const STATUS: Record<NightlyStatus, { label: string; variant: "success" | "offline" | "secondary" }> = {
  succeeded: { label: "Ready", variant: "success" },
  failed: { label: "Failed", variant: "offline" },
  in_progress: { label: "In progress", variant: "secondary" },
  pending: { label: "Pending", variant: "secondary" },
  skipped: { label: "Skipped", variant: "secondary" },
};

function when(r: NightlyRunRow): string {
  return r.completed_at ?? r.started_at ?? r.scheduled_at;
}

function formatWhen(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function RoomReadinessCard({ roomId, title }: { roomId: string; title?: string }) {
  const [runs, setRuns] = useState<NightlyRunRow[] | null>(null);
  const [failedSteps, setFailedSteps] = useState<NightlyStepRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    const from = new Date(Date.now() - HISTORY_DAYS * 86400_000).toISOString();
    setRuns(null);
    setFailedSteps(null);
    api
      .listNightlyRuns({ room_id: roomId, from, limit: 100 }, ctrl.signal)
      .then((rs) => {
        setRuns(rs);
        setError(null);
        const latest = rs.find((r) => r.kind === "readiness" && r.status !== "pending");
        if (latest?.status === "failed") {
          return api.getNightlyRun(latest.id, ctrl.signal).then((d) => {
            setFailedSteps(d.steps.filter((s) => !s.passed && s.step_index >= 0));
          });
        }
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setError((e as Error).message);
      });
    return () => ctrl.abort();
  }, [roomId]);

  const { readiness, power, history } = useMemo(() => {
    const rs = [...(runs ?? [])].sort((a, b) => when(b).localeCompare(when(a)));
    const checks = rs.filter((r) => r.kind === "readiness");
    return {
      readiness: checks.find((r) => r.status !== "pending"),
      power: rs.find((r) => r.kind === "power" && r.status !== "pending"),
      // Oldest first, so the strip reads left to right like a calendar.
      history: checks.filter((r) => r.status === "succeeded" || r.status === "failed").reverse(),
    };
  }, [runs]);

  return (
    <Card>
      <CardContent className="p-4 sm:p-5 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <Moon aria-hidden="true" className="h-4 w-4 text-primary" />
            <h2 className="text-sm font-semibold">
              Room Readiness{title ? ` · ${title}` : ""}
            </h2>
          </div>
          <Link
            href={`/nightly/runs?room_id=${encodeURIComponent(roomId)}`}
            className="inline-flex items-center gap-0.5 text-xs font-medium text-primary hover:underline"
          >
            All runs for this room
            <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
          </Link>
        </div>

        {error && <p className="text-sm [color:hsl(var(--destructive))]">{error}</p>}
        {runs === null && !error && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading results…
          </div>
        )}
        {runs !== null && runs.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No checks or power cycles in the last {HISTORY_DAYS} days.
          </p>
        )}

        {runs !== null && runs.length > 0 && (
          <div className="grid gap-4 sm:grid-cols-2">
            <RunSummary label="Latest readiness check" run={readiness} empty="No checks yet" />
            <RunSummary label="Latest overnight power cycle" run={power} empty="No power cycles yet" />
          </div>
        )}

        {readiness?.status === "failed" && failedSteps && failedSteps.length > 0 && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3">
            <p className="text-xs font-medium [color:hsl(var(--destructive))]">What failed</p>
            <ul className="mt-1.5 space-y-1 text-sm">
              {failedSteps.map((s) => (
                <li key={s.step_index} className="flex gap-2">
                  <XCircle aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0 [color:hsl(var(--destructive))]" />
                  <span className="min-w-0 break-words">
                    <span className="font-medium">{s.step_name}</span>
                    {s.device_name && <span className="text-muted-foreground"> · {s.device_name}</span>}
                    {s.error && <span className="text-muted-foreground"> — {s.error}</span>}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {history.length > 0 && (
          <div>
            <p className="mb-1.5 text-xs text-muted-foreground">
              Readiness checks, last {HISTORY_DAYS} days ·{" "}
              {history.filter((r) => r.status === "succeeded").length} of {history.length} passed
            </p>
            <ol className="flex flex-wrap gap-1" aria-label="Readiness check history">
              {history.map((r) => {
                const ok = r.status === "succeeded";
                const label = `${formatWhen(when(r))}: ${ok ? "passed" : "failed"}`;
                return (
                  <li key={r.id}>
                    <Link
                      href={`/nightly/runs/${r.id}`}
                      title={label}
                      aria-label={label}
                      className={`flex h-6 w-6 items-center justify-center rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                        ok
                          ? "bg-[color:hsl(var(--success))]/15 [color:hsl(var(--success))]"
                          : "bg-destructive/15 [color:hsl(var(--destructive))]"
                      }`}
                    >
                      {ok ? (
                        <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5" />
                      ) : (
                        <XCircle aria-hidden="true" className="h-3.5 w-3.5" />
                      )}
                    </Link>
                  </li>
                );
              })}
            </ol>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function RunSummary({ label, run, empty }: { label: string; run?: NightlyRunRow; empty: string }) {
  if (!run) {
    return (
      <div className="rounded-md border p-3">
        <p className="text-xs text-muted-foreground">{label}</p>
        <p className="mt-1 text-sm text-muted-foreground">{empty}</p>
      </div>
    );
  }
  const s = STATUS[run.status];
  return (
    <Link
      href={`/nightly/runs/${run.id}`}
      className="block rounded-md border p-3 transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="mt-1 flex flex-wrap items-center gap-2">
        <Badge variant={s.variant} className="uppercase text-[10px]">
          {s.label}
        </Badge>
        <span className="text-sm tabular-nums" title={formatWhen(when(run))}>
          {formatRelative(when(run))}
        </span>
        {run.routine_name && (
          <span className="truncate text-xs text-muted-foreground">{run.routine_name}</span>
        )}
      </div>
      {run.status === "failed" && run.failure_reason && (
        <p className="mt-1 text-xs text-muted-foreground break-words">{run.failure_reason}</p>
      )}
    </Link>
  );
}
