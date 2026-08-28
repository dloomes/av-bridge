"use client";

import Link from "next/link";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronRight,
  CircleHelp,
  CircleSlash,
  ShieldAlert,
} from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Skeleton } from "@/components/ui/skeleton";
import { DeviceIcon } from "@/components/device-icon";
import { cn, formatRelative } from "@/lib/utils";
import type { DeviceStatus, DeviceSummary } from "@/lib/types";

// Fleet Health at a glance — replaces the old dashboard Live Events feed.
// Signal density > liveness: rather than a real-time ticker that's empty
// 95% of the time, this lists the specific devices an operator would
// actually act on (offline first, then degraded) with a direct link
// through to each device's detail page. All-healthy state is a positive
// success message so a green panel confirms "nothing to do".

interface Props {
  devices?: DeviceSummary[] | null;
  loading?: boolean;
}

interface RowMeta {
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  color: string;
  border: string;
  bar: string;
}

const STATUS_META: Record<"offline" | "degraded" | "unknown", RowMeta> = {
  offline: {
    label: "Offline",
    icon: CircleSlash,
    color: "[color:hsl(var(--destructive))]",
    border: "border-destructive/25",
    bar: "border-l-destructive",
  },
  degraded: {
    label: "Degraded",
    icon: AlertTriangle,
    color: "[color:hsl(var(--warning))]",
    border: "border-warning/25",
    bar: "border-l-warning",
  },
  // Unknown ≡ collector offline — real device state is not visible to us
  // right now. Muted styling: attention-worthy but not the same alarm
  // level as a device the bridge actively reported as down.
  unknown: {
    label: "Unknown",
    icon: CircleHelp,
    color: "text-muted-foreground",
    border: "border-muted-foreground/25",
    bar: "border-l-muted-foreground/60",
  },
};

export function FleetHealth({ devices, loading }: Props) {
  // Attention order: real reported failures first (offline, degraded),
  // then can't-see-them (unknown) so operators triage what they can act
  // on directly before chasing collector issues.
  const offline = (devices ?? []).filter((d) => d.status === "offline");
  const degraded = (devices ?? []).filter((d) => d.status === "degraded");
  const unknown = (devices ?? []).filter((d) => d.status === "unknown");
  const problems = [...offline, ...degraded, ...unknown];
  const attentionCount = problems.length;

  return (
    <Card className="flex h-full flex-col">
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <div className="flex items-center gap-2">
          <ShieldAlert
            className={cn(
              "h-4 w-4",
              attentionCount > 0
                ? "[color:hsl(var(--destructive))]"
                : "text-primary"
            )}
          />
          <CardTitle>Attention needed</CardTitle>
        </div>
        {attentionCount > 0 && (
          <span
            className="inline-flex items-center gap-1 rounded-full border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-semibold [color:hsl(var(--destructive))]"
            aria-label={`${attentionCount} device${attentionCount === 1 ? "" : "s"} need attention`}
          >
            {attentionCount}
          </span>
        )}
      </CardHeader>
      <CardContent className="min-h-0 flex-1 p-0">
        <ScrollArea className="h-full px-4 pb-4 scrollbar-thin">
          {loading && !devices && (
            <div className="space-y-2 py-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          )}

          {devices && problems.length === 0 && <AllHealthy />}

          {problems.length > 0 && (
            <ul className="space-y-1.5" role="list">
              {problems.map((d) => (
                <FleetHealthRow key={d.id} device={d} />
              ))}
            </ul>
          )}
        </ScrollArea>
      </CardContent>
    </Card>
  );
}

// ── All-healthy state ──────────────────────────────────────────────────────

function AllHealthy() {
  return (
    <div className="flex flex-col items-center gap-2 py-10 text-center">
      <div className="rounded-full bg-success/10 p-2">
        <CheckCircle2 className="h-5 w-5 [color:hsl(var(--success))]" />
      </div>
      <p className="text-sm font-medium">All devices healthy</p>
      <p className="text-xs text-muted-foreground">
        Nothing offline, nothing degraded. Anything that needs attention
        will show here.
      </p>
    </div>
  );
}

// ── Row ────────────────────────────────────────────────────────────────────

function FleetHealthRow({ device }: { device: DeviceSummary }) {
  const status =
    device.status === "offline" || device.status === "degraded" || device.status === "unknown"
      ? device.status
      : "degraded";
  const meta = STATUS_META[status];
  const Icon = meta.icon;
  const locationParts = [device.building, device.location_name].filter(Boolean);
  const location = locationParts.join(" · ");

  return (
    <li>
      <Link
        href={`/devices/${encodeURIComponent(device.id)}`}
        className={cn(
          "group flex items-center gap-3 rounded-md border border-l-2 bg-card px-3 py-2 transition-colors hover:bg-muted/40",
          meta.border,
          meta.bar
        )}
      >
        <DeviceIcon
          type={device.type}
          className="h-4 w-4 shrink-0 text-muted-foreground group-hover:text-foreground transition-colors"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium">{device.name}</span>
            <span
              className={cn(
                "shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide",
                meta.color,
                meta.border
              )}
            >
              <span className="sr-only">Status: </span>
              <Icon className="mr-0.5 -mt-0.5 inline h-2.5 w-2.5" />
              {meta.label}
            </span>
          </div>
          {location && (
            <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
              {location}
            </p>
          )}
        </div>
        <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
      </Link>
    </li>
  );
}
