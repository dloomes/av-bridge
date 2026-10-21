import { SlidersHorizontal, Volume2, VolumeX } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { Subscription } from "@/lib/types";
import { cn, prettyMetricKey } from "@/lib/utils";

// LevelsCard shows the readings set up on a device (a Tesira's
// subscriptions), in the order they were added: levels and meters as dB
// with a bar, mutes as Muted / Unmuted, logic states as On / Off, VoIP
// call states as a call count. Everything else the device reports stays
// in Device Information.

interface Props {
  subscriptions: Subscription[];
  metrics: Record<string, unknown> | null | undefined;
}

// The labels this card shows, so Device Information can leave them out.
export function levelKeys(subs: Subscription[] | undefined): Set<string> {
  const keys = new Set<string>();
  for (const s of subs ?? []) {
    if (!s.label) continue;
    keys.add(s.label);
    if (s.attribute === "callState") keys.add(`${s.label}_active_calls`);
  }
  return keys;
}

// dB scale for the bars: a Tesira level control runs -100 to +12 dB.
const DB_MIN = -100;
const DB_MAX = 12;

function asNumber(v: unknown): number | undefined {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  if (typeof v === "string" && v.trim() !== "" && Number.isFinite(Number(v))) return Number(v);
  return undefined;
}

function asBool(v: unknown): boolean | undefined {
  if (typeof v === "boolean") return v;
  if (v === "true") return true;
  if (v === "false") return false;
  return undefined;
}

// Older Collectors send lists as text: "[-10.000000 0.000000]".
function asList(v: unknown): unknown[] | undefined {
  if (Array.isArray(v)) return v;
  if (typeof v === "string" && v.startsWith("[") && v.endsWith("]")) {
    return v.slice(1, -1).trim().split(/\s+/).filter(Boolean);
  }
  return undefined;
}

export function LevelsCard({ subscriptions, metrics }: Props) {
  const subs = subscriptions.filter((s) => s.label);
  if (subs.length === 0) return null;

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <SlidersHorizontal className="h-4 w-4 text-primary" />
          <CardTitle>Levels</CardTitle>
        </div>
      </CardHeader>
      <CardContent>
        <ul className="divide-y divide-border/50">
          {subs.map((s) => (
            <LevelRow key={s.label} sub={s} metrics={metrics ?? {}} />
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

function LevelRow({ sub, metrics }: { sub: Subscription; metrics: Record<string, unknown> }) {
  const value = metrics[sub.label];
  const list = sub.attribute === "levels" || sub.attribute === "mutes" ? asList(value) : undefined;
  const where = ["levels", "mutes", "callState"].includes(sub.attribute)
    ? `${sub.tag} · ${sub.attribute}`
    : `${sub.tag} · ${sub.attribute} ${sub.channel}`;

  return (
    <li className="py-2.5">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
        <div className="min-w-0">
          <div className="text-sm font-medium">{prettyMetricKey(sub.label)}</div>
          <div className="font-mono text-[11px] text-muted-foreground">{where}</div>
        </div>
        {!list && <Value sub={sub} value={value} metrics={metrics} />}
      </div>
      {list && (
        <ul className="mt-2 space-y-1.5">
          {list.map((v, i) => (
            <li key={i} className="flex items-center justify-between gap-4">
              <span className="text-xs text-muted-foreground">Channel {i + 1}</span>
              <Value sub={{ ...sub, attribute: sub.attribute === "levels" ? "level" : "mute" }} value={v} metrics={metrics} />
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function Value({ sub, value, metrics }: { sub: Subscription; value: unknown; metrics: Record<string, unknown> }) {
  if (value === undefined || value === null || value === "") {
    return <span className="text-sm text-muted-foreground">No reading yet</span>;
  }
  switch (sub.attribute) {
    case "level": {
      const db = asNumber(value);
      return db === undefined ? <Plain value={value} /> : <DbBar db={db} />;
    }
    case "mute": {
      const muted = asBool(value);
      if (muted === undefined) return <Plain value={value} />;
      const Icon = muted ? VolumeX : Volume2;
      return (
        <Badge variant={muted ? "offline" : "success"} className="gap-1">
          <Icon aria-hidden="true" className="h-3 w-3" />
          {muted ? "Muted" : "Unmuted"}
        </Badge>
      );
    }
    case "state": {
      const on = asBool(value);
      if (on === undefined) return <Plain value={value} />;
      return <Badge variant={on ? "success" : "secondary"}>{on ? "On" : "Off"}</Badge>;
    }
    case "callState": {
      const calls = asNumber(metrics[`${sub.label}_active_calls`]);
      if (calls === undefined) return <span className="text-sm text-muted-foreground">Reported</span>;
      return (
        <Badge variant={calls > 0 ? "success" : "secondary"}>
          {calls === 0 ? "Idle" : calls === 1 ? "In a call" : `In ${calls} calls`}
        </Badge>
      );
    }
    default:
      return <Plain value={value} />;
  }
}

function Plain({ value }: { value: unknown }) {
  const n = asNumber(value);
  const text = n !== undefined ? String(Math.round(n * 100) / 100) : typeof value === "string" ? value : JSON.stringify(value);
  return <span className="text-sm font-medium tabular-nums break-all text-right">{text}</span>;
}

function DbBar({ db }: { db: number }) {
  const pct = Math.max(0, Math.min(100, ((db - DB_MIN) / (DB_MAX - DB_MIN)) * 100));
  const zero = ((0 - DB_MIN) / (DB_MAX - DB_MIN)) * 100;
  return (
    <div className="flex items-center gap-3">
      <div
        className="relative h-2 w-32 sm:w-44 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-valuemin={DB_MIN}
        aria-valuemax={DB_MAX}
        aria-valuenow={db}
        aria-label={`${db} dB`}
      >
        <div
          className={cn("absolute inset-y-0 left-0 rounded-full", db > 0 ? "bg-warning" : "bg-primary")}
          style={{ width: `${pct}%` }}
        />
        {/* 0 dB mark */}
        <div className="absolute inset-y-0 w-px bg-foreground/40" style={{ left: `${zero}%` }} aria-hidden="true" />
      </div>
      <span className="w-16 text-right text-sm font-medium tabular-nums">
        {db > 0 ? "+" : ""}
        {Math.round(db * 10) / 10} dB
      </span>
    </div>
  );
}
