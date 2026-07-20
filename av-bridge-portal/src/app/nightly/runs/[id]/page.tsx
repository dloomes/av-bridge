"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  ArrowLeft,
  CheckCircle2,
  Loader2,
  Moon,
  XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type {
  NightlyPhase,
  NightlyRunDetail,
  NightlyStepRow,
} from "@/lib/api";

// Room Readiness — one-run detail.
//
// Slice 4. Shows:
//   - Room + hierarchy breadcrumb
//   - Timing: scheduled / started / completed / duration
//   - Phase timeline with the current phase highlighted (or "failed" red)
//   - Recipe reference (link back to the editor)
//   - Failure reason (if any)
//   - Step results table (empty until Phase B populates it)

// Every phase, in the order the runner walks them. The two terminal
// phases (ready / failed) are called out as their own final position;
// everything before them is a linear crumb.
const PHASE_TIMELINE: NightlyPhase[] = [
  "pending",
  "scheduled_off",
  "off",
  "scheduled_on",
  "waking",
  "warming",
  "testing",
  "ready",
];

const PHASE_LABELS: Record<NightlyPhase, string> = {
  pending: "Pending",
  scheduled_off: "Power off",
  off: "Off",
  scheduled_on: "Power on",
  waking: "Waking",
  warming: "Warming up",
  testing: "Testing",
  ready: "Ready",
  failed: "Failed",
};

function fmtDuration(seconds: number | undefined): string {
  if (seconds === undefined) return "—";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s === 0 ? `${m}m` : `${m}m ${s}s`;
}

export default function NightlyRunDetailPage() {
  const params = useParams<{ id: string }>();
  const runID = params.id;

  const [loaded, setLoaded] = useState<NightlyRunDetail | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    api
      .getNightlyRun(runID, ctrl.signal)
      .then((r) => {
        if (!ctrl.signal.aborted) setLoaded(r);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setLoadError((e as Error).message);
      });
    return () => ctrl.abort();
  }, [runID]);

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
                href="/nightly/runs"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ArrowLeft aria-hidden="true" className="h-3 w-3" />
                All runs
              </Link>
            </div>
            <h1 className="text-xl font-semibold leading-tight">
              {loaded?.room_name ?? "Run"}
            </h1>
            {loaded && (
              <p className="text-xs text-muted-foreground leading-tight">
                {[loaded.region_name, loaded.location_name, loaded.building_name]
                  .filter(Boolean)
                  .join(" · ")}
              </p>
            )}
          </div>
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-4xl space-y-6">
          {loadError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </div>
          )}

          {loaded === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading run…
            </div>
          )}

          {loaded !== null && (
            <>
              {/* ── Header stats ─────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-4">
                  <div className="flex items-start justify-between gap-4 flex-wrap">
                    <div className="min-w-0">
                      <div className="text-xs uppercase tracking-wide text-muted-foreground">
                        Status
                      </div>
                      <div className="mt-1 flex items-center gap-2">
                        {loaded.status === "succeeded" ? (
                          <CheckCircle2
                            aria-hidden="true"
                            className="h-5 w-5 [color:hsl(var(--success))]"
                          />
                        ) : loaded.status === "failed" ? (
                          <XCircle
                            aria-hidden="true"
                            className="h-5 w-5 [color:hsl(var(--destructive))]"
                          />
                        ) : (
                          <Loader2
                            aria-hidden="true"
                            className={`h-5 w-5 ${loaded.status === "in_progress" ? "animate-spin" : ""}`}
                          />
                        )}
                        <span className="text-lg font-semibold">
                          {loaded.status === "succeeded"
                            ? "Ready"
                            : loaded.status === "failed"
                              ? "Failed"
                              : loaded.status === "in_progress"
                                ? "In progress"
                                : loaded.status}
                        </span>
                      </div>
                    </div>
                    {loaded.recipe_id && (
                      <div className="min-w-0">
                        <div className="text-xs uppercase tracking-wide text-muted-foreground">
                          Recipe
                        </div>
                        <Link
                          href={`/nightly/recipes/${loaded.recipe_id}`}
                          className="mt-1 inline-block text-sm text-primary hover:underline"
                        >
                          {loaded.recipe_name ?? "(deleted)"}
                        </Link>
                      </div>
                    )}
                  </div>

                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 pt-2 border-t">
                    <TimingBlock label="Scheduled" value={loaded.scheduled_at} />
                    <TimingBlock label="Started" value={loaded.started_at} />
                    <TimingBlock
                      label="Duration"
                      value={fmtDuration(loaded.duration_seconds)}
                    />
                  </div>

                  {loaded.failure_reason && (
                    <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm">
                      <div className="text-xs font-semibold [color:hsl(var(--destructive))] uppercase tracking-wide">
                        Failure reason
                      </div>
                      <div className="mt-1 [color:hsl(var(--destructive))]">
                        {loaded.failure_reason}
                      </div>
                    </div>
                  )}
                </CardContent>
              </Card>

              {/* ── Phase timeline ───────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-3">
                  <h2 className="text-sm font-semibold">Phase</h2>
                  <PhaseTimeline
                    current={loaded.phase}
                    failed={loaded.status === "failed"}
                  />
                </CardContent>
              </Card>

              {/* ── Step results ─────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-3">
                  <h2 className="text-sm font-semibold">Step results</h2>
                  {loaded.steps.length === 0 ? (
                    <div className="rounded-md border border-dashed p-6 text-center">
                      <div className="text-sm text-muted-foreground">
                        {loaded.recipe_id
                          ? "No step results yet — the recipe runner lands in Phase B."
                          : "No recipe was assigned when this run executed."}
                      </div>
                    </div>
                  ) : (
                    <div className="overflow-x-auto">
                      <table className="w-full text-sm">
                        <thead>
                          <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                            <th scope="col" className="px-3 py-2 font-medium">#</th>
                            <th scope="col" className="px-3 py-2 font-medium">Step</th>
                            <th scope="col" className="px-3 py-2 font-medium">Device</th>
                            <th scope="col" className="px-3 py-2 font-medium">Expected</th>
                            <th scope="col" className="px-3 py-2 font-medium">Actual</th>
                            <th scope="col" className="px-3 py-2 font-medium">Result</th>
                          </tr>
                        </thead>
                        <tbody>
                          {loaded.steps.map((s) => (
                            <StepRow key={s.step_index} step={s} />
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </CardContent>
              </Card>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function TimingBlock({
  label,
  value,
}: {
  label: string;
  value?: string;
}) {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className="mt-1 text-sm font-mono">
        {value
          ? label === "Duration"
            ? value
            : new Date(value).toLocaleString()
          : "—"}
      </div>
    </div>
  );
}

// PhaseTimeline — horizontal breadcrumb. Everything up to and including
// the current phase is filled with the accent colour; unwalked phases sit
// muted. A `failed` run replaces the trailing green with red on the last
// walked phase to signal where it went wrong.
function PhaseTimeline({
  current,
  failed,
}: {
  current: NightlyPhase;
  failed: boolean;
}) {
  // "failed" is its own terminal — treat the last non-failed phase as
  // "how far we got". decideTransition sets phase='failed' on transition
  // to the failed terminal, but the meaningful walked-phase for the
  // timeline is the one before it — we don't have it stored though, so
  // just mark all phases through the previous one as walked and light up
  // the failed terminal in red.
  if (current === "failed") {
    return (
      <div className="flex items-center flex-wrap gap-1">
        {PHASE_TIMELINE.slice(0, -1).map((p, i, arr) => (
          <PhaseChip key={p} phase={p} state={i < arr.length ? "walked" : "future"} />
        ))}
        <PhaseChip phase="failed" state="failed" />
      </div>
    );
  }

  const currentIdx = PHASE_TIMELINE.indexOf(current);
  return (
    <div className="flex items-center flex-wrap gap-1">
      {PHASE_TIMELINE.map((p, i) => (
        <PhaseChip
          key={p}
          phase={p}
          state={
            i < currentIdx
              ? "walked"
              : i === currentIdx
                ? current === "ready"
                  ? "ready"
                  : "current"
                : "future"
          }
        />
      ))}
    </div>
  );
}

function PhaseChip({
  phase,
  state,
}: {
  phase: NightlyPhase;
  state: "walked" | "current" | "future" | "ready" | "failed";
}) {
  const base = "rounded-md border px-2 py-1 text-xs font-medium";
  const style =
    state === "walked"
      ? "bg-primary/10 border-primary/30 [color:hsl(var(--primary))]"
      : state === "current"
        ? "bg-primary text-primary-foreground border-primary"
        : state === "ready"
          ? "bg-[color:hsl(var(--success))] text-white border-[color:hsl(var(--success))]"
          : state === "failed"
            ? "bg-destructive text-destructive-foreground border-destructive"
            : "bg-background border-border text-muted-foreground";
  return <span className={`${base} ${style}`}>{PHASE_LABELS[phase]}</span>;
}

function StepRow({ step }: { step: NightlyStepRow }) {
  return (
    <tr className="border-b last:border-0">
      <td className="px-3 py-2.5 align-top text-xs font-mono text-muted-foreground">
        {step.step_index + 1}
      </td>
      <td className="px-3 py-2.5 align-top">
        <div className="font-medium text-sm">{step.step_name}</div>
        <div className="text-[10px] text-muted-foreground uppercase tracking-wide">
          {step.step_type}
        </div>
      </td>
      <td className="px-3 py-2.5 align-top text-xs text-muted-foreground">
        {step.device_name ?? "—"}
      </td>
      <td className="px-3 py-2.5 align-top text-xs font-mono max-w-[180px] break-all">
        {step.expected !== undefined ? JSON.stringify(step.expected) : "—"}
      </td>
      <td className="px-3 py-2.5 align-top text-xs font-mono max-w-[180px] break-all">
        {step.actual !== undefined ? JSON.stringify(step.actual) : "—"}
      </td>
      <td className="px-3 py-2.5 align-top">
        {step.passed ? (
          <Badge variant="success" className="uppercase text-[10px]">
            Pass
          </Badge>
        ) : (
          <>
            <Badge variant="destructive" className="uppercase text-[10px]">
              Fail
            </Badge>
            {step.error && (
              <div className="text-xs [color:hsl(var(--destructive))] mt-1">
                {step.error}
              </div>
            )}
          </>
        )}
      </td>
    </tr>
  );
}
