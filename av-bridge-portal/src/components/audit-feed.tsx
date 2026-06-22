"use client";

import { useMemo, useState } from "react";
import { ChevronRight, ShieldCheck } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type { AuditEntry } from "@/lib/types";

interface AuditFeedProps {
  // Filters narrow the feed; both omitted = customer-wide.
  targetKind?: string;
  targetId?: string;
  limit?: number;
  emptyHint?: string;
}

// Categorise the action verb so we can colour the row consistently regardless
// of which target type it touched. Cheap and works as long as actions follow
// the "<noun>.<verb>" naming the cloud uses.
function actionTone(action: string): "create" | "update" | "delete" | "submit" | "other" {
  const verb = action.split(".").pop() ?? "";
  if (verb === "create") return "create";
  if (verb === "update") return "update";
  if (verb === "delete") return "delete";
  if (verb === "submit") return "submit";
  return "other";
}

const TONE_CLASSES: Record<ReturnType<typeof actionTone>, string> = {
  create: "border-emerald-500/30 bg-emerald-500/5",
  update: "border-sky-500/30 bg-sky-500/5",
  delete: "border-red-500/30 bg-red-500/5",
  submit: "border-amber-500/30 bg-amber-500/5",
  other:  "border-border bg-muted/30",
};

const TONE_LABELS: Record<ReturnType<typeof actionTone>, string> = {
  create: "Created",
  update: "Updated",
  delete: "Deleted",
  submit: "Submitted",
  other:  "Action",
};

export function AuditFeed({ targetKind, targetId, limit = 100, emptyHint }: AuditFeedProps) {
  const fetcher = useMemo(
    () => (signal: AbortSignal) =>
      api.listAudit({ targetKind, targetId, limit }, signal),
    [targetKind, targetId, limit]
  );
  const { data, loading, error } = usePolling<AuditEntry[]>(fetcher, 30_000, [
    targetKind,
    targetId,
    limit,
  ]);

  if (loading && !data) {
    return (
      <div className="space-y-2">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-14 w-full" />
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <Card className="border-destructive/30 bg-destructive/5">
        <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
          Could not load audit log: {error.message}
        </CardContent>
      </Card>
    );
  }

  if (!data || data.length === 0) {
    return (
      <Card>
        <CardContent className="p-10 text-center text-sm text-muted-foreground">
          {emptyHint ?? "No audit entries yet."}
        </CardContent>
      </Card>
    );
  }

  return (
    <ul className="space-y-2">
      {data.map((e) => (
        <AuditRow key={e.id} entry={e} />
      ))}
    </ul>
  );
}

function AuditRow({ entry }: { entry: AuditEntry }) {
  const [open, setOpen] = useState(false);
  const tone = actionTone(entry.action);
  const hasDetails = entry.before != null || entry.after != null || entry.metadata != null;

  // "device.update" → "device · Updated"; falls back gracefully for actions
  // that don't follow the noun.verb shape.
  const [noun] = entry.action.split(".");

  return (
    <li className={`rounded-md border ${TONE_CLASSES[tone]} text-sm`}>
      <button
        type="button"
        className="flex w-full items-start gap-3 px-3 py-2 text-left hover:bg-foreground/[0.02]"
        onClick={() => hasDetails && setOpen((v) => !v)}
        disabled={!hasDetails}
        aria-expanded={open}
      >
        <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium capitalize">{noun}</span>
            <span className="text-xs text-muted-foreground">{TONE_LABELS[tone]}</span>
            <span className="text-xs text-muted-foreground">by {entry.actor}</span>
          </div>
          {entry.target_id && (
            <div className="mt-0.5 text-xs text-muted-foreground truncate">
              {entry.target_kind}:{entry.target_id}
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
          <span>{formatRelative(entry.ts)}</span>
          {hasDetails && (
            <ChevronRight
              className={`h-3.5 w-3.5 transition-transform ${open ? "rotate-90" : ""}`}
            />
          )}
        </div>
      </button>
      {open && <RowDetails entry={entry} />}
    </li>
  );
}

// RowDetails picks the right shape for the change:
//  - update (before + after objects): per-field diff (only changed fields shown)
//  - create (after only): flat list of what was created
//  - delete (before only): flat list of what was removed
//  - metadata only (e.g. command.submit): flat list of submitted args
//  - anything else: fall back to JSON
function RowDetails({ entry }: { entry: AuditEntry }) {
  const diff = computeDiff(entry.before, entry.after);
  return (
    <div className="space-y-3 border-t px-3 pb-3 pt-2 text-xs">
      {diff && diff.length > 0 && <DiffList rows={diff} />}
      {diff && diff.length === 0 && (
        <div className="italic text-muted-foreground">No field-level changes.</div>
      )}
      {!diff && entry.before != null && (
        <RawJSONBlock label="Before" value={entry.before} />
      )}
      {!diff && entry.after != null && (
        <RawJSONBlock label="After" value={entry.after} />
      )}
      {entry.metadata != null && Object.keys(entry.metadata).length > 0 && (
        <FlatList label="Metadata" obj={entry.metadata} />
      )}
    </div>
  );
}

type DiffRow = { field: string; before?: unknown; after?: unknown };

// computeDiff returns a per-field change list when before/after are both plain
// objects (or one is and the other is missing). Returns null when neither side
// is a usable object — caller falls back to raw JSON.
function computeDiff(before: unknown, after: unknown): DiffRow[] | null {
  const bObj = isPlainObject(before);
  const aObj = isPlainObject(after);
  if (!bObj && !aObj) return null;

  if (bObj && aObj) {
    const b = before as Record<string, unknown>;
    const a = after as Record<string, unknown>;
    const keys = new Set([...Object.keys(b), ...Object.keys(a)]);
    const rows: DiffRow[] = [];
    for (const k of keys) {
      if (!deepEqual(b[k], a[k])) rows.push({ field: k, before: b[k], after: a[k] });
    }
    return rows.sort((x, y) => x.field.localeCompare(y.field));
  }
  if (aObj) {
    const a = after as Record<string, unknown>;
    return Object.keys(a)
      .filter((k) => a[k] !== null && a[k] !== undefined)
      .sort()
      .map((field) => ({ field, after: a[field] }));
  }
  // bObj only — delete with snapshot
  const b = before as Record<string, unknown>;
  return Object.keys(b)
    .filter((k) => b[k] !== null && b[k] !== undefined)
    .sort()
    .map((field) => ({ field, before: b[field] }));
}

function DiffList({ rows }: { rows: DiffRow[] }) {
  return (
    <dl className="space-y-1.5">
      {rows.map((r) => (
        <div key={r.field} className="flex flex-wrap items-baseline gap-x-2">
          <dt className="font-mono text-[11px] font-semibold text-muted-foreground">
            {r.field}
          </dt>
          <dd className="flex min-w-0 flex-wrap items-baseline gap-x-2">
            {r.before !== undefined && <Value v={r.before} dim />}
            {r.before !== undefined && r.after !== undefined && (
              <span className="text-muted-foreground">→</span>
            )}
            {r.after !== undefined && <Value v={r.after} />}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function FlatList({ label, obj }: { label: string; obj: Record<string, unknown> }) {
  const keys = Object.keys(obj).sort();
  return (
    <div>
      <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <dl className="mt-1 space-y-1">
        {keys.map((k) => (
          <div key={k} className="flex flex-wrap items-baseline gap-x-2">
            <dt className="font-mono text-[11px] font-semibold text-muted-foreground">
              {k}
            </dt>
            <dd>
              <Value v={obj[k]} />
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function RawJSONBlock({ label, value }: { label: string; value: unknown }) {
  return (
    <div>
      <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <pre className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-background/70 p-2 font-mono">
        {JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}

// Value renders a JSON-ish value compactly. Nested objects/arrays are shown
// as compact JSON; primitives get appropriate quoting; null shows as an
// italic placeholder so it's visually distinct from a missing field.
function Value({ v, dim = false }: { v: unknown; dim?: boolean }) {
  const cls = `font-mono ${dim ? "text-muted-foreground line-through decoration-1" : ""}`;
  if (v === null) {
    return <span className={`italic ${cls}`}>null</span>;
  }
  if (typeof v === "string") {
    return <span className={cls}>&quot;{v}&quot;</span>;
  }
  if (typeof v === "number" || typeof v === "boolean") {
    return <span className={cls}>{String(v)}</span>;
  }
  return <span className={cls}>{JSON.stringify(v)}</span>;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a === null || b === null) return a === b;
  if (typeof a !== typeof b) return false;
  if (typeof a !== "object") return false;
  // Cheap structural equality via JSON. Audit snapshots are bounded and don't
  // contain functions/cycles, so this is fine; saves an explicit recurse.
  return JSON.stringify(a) === JSON.stringify(b);
}
