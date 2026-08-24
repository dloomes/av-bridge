"use client";

import { useEffect, useMemo } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  Building2,
  CircleSlash,
  MapPin,
  RefreshCcw,
  Server,
  Wifi,
} from "lucide-react";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { StatCard } from "@/components/stat-card";
import { UserMenu } from "@/components/user-menu";
import { useBranding } from "@/components/branding-provider";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { cn, formatRelative, groupDevicesByLocation } from "@/lib/utils";
import type { DeviceSummary, FleetStatus } from "@/lib/types";

// Building-level "map" tile aggregates the four fleet statuses for a
// single building. Composed on the client from the same /api/v1/devices
// list the Places sidebar consumes, so no new backend surface. If we
// grow real geographic maps later, this shape is what the pins would
// render into anyway.
interface BuildingStatus {
  name: string;
  href: string;
  // Region + location parents — shown as breadcrumb above the name so
  // an operator can eyeball where in the world the building sits.
  region?: string;
  locationName?: string;
  total: number;
  online: number;
  offline: number;
  degraded: number;
  unknown: number;
}

function tone(b: BuildingStatus): "destructive" | "warning" | "success" | "neutral" {
  if (b.offline > 0) return "destructive";
  if (b.degraded > 0) return "warning";
  if (b.online > 0) return "success";
  return "neutral";
}

const TONE_STYLES: Record<
  "destructive" | "warning" | "success" | "neutral",
  { ring: string; text: string }
> = {
  destructive: { ring: "ring-destructive/30 hover:ring-destructive/60", text: "[color:hsl(var(--destructive))]" },
  warning:     { ring: "ring-warning/30 hover:ring-warning/60",         text: "[color:hsl(var(--warning))]" },
  success:     { ring: "ring-success/25 hover:ring-success/60",         text: "[color:hsl(var(--success))]" },
  neutral:     { ring: "ring-border hover:ring-input",                  text: "text-muted-foreground" },
};

export default function DashboardPage() {
  const session = useSession();
  const router = useRouter();
  const { branding } = useBranding();

  // Vendor users without a customer scope have nothing to show here — every
  // tenant query would 500 with an empty UUID. Push them to the helpdesk
  // overview, which is the right landing page for unscoped support staff.
  useEffect(() => {
    if (!session.hydrated) return;
    if (session.user?.is_vendor && !session.scope) {
      router.replace("/helpdesk");
    }
  }, [session.hydrated, session.user, session.scope, router]);

  const fleet = usePolling<FleetStatus>(
    (signal) => api.fleetStatus(signal),
    15_000
  );
  // Devices re-added to the overview — powers the building tiles below
  // the stat row. Same 30s cadence LocationNav uses so we don't
  // double-poll when both are on-screen (SWR-style dedup would be
  // stricter but this is fine at current scale).
  const devices = usePolling<DeviceSummary[]>(
    (signal) => api.listDevices(signal),
    30_000
  );

  const buildings = useMemo<BuildingStatus[]>(() => {
    if (!devices.data) return [];
    const groups = groupDevicesByLocation(devices.data);
    return groups.map((g) => {
      const label = g.building ?? "Unassigned";
      const all = g.rooms.flatMap((r) => r.devices);
      const counts = all.reduce(
        (acc, d) => {
          if (d.status === "online") acc.online += 1;
          else if (d.status === "offline") acc.offline += 1;
          else if (d.status === "degraded") acc.degraded += 1;
          else acc.unknown += 1;
          return acc;
        },
        { online: 0, offline: 0, degraded: 0, unknown: 0 }
      );
      // Unassigned devices don't get a building filter link — the URL
      // param would be a literal "Unassigned" that matches nothing.
      // Skip the href for that bucket so the tile is informational.
      const href = g.building
        ? `/devices?building=${encodeURIComponent(g.building)}`
        : undefined;
      return {
        name: label,
        href: href ?? "",
        region: g.region,
        locationName: g.locationName,
        total: all.length,
        online: counts.online,
        offline: counts.offline,
        degraded: counts.degraded,
        unknown: counts.unknown,
      };
    });
  }, [devices.data]);

  const isLoading = fleet.loading && !fleet.data;

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          {branding.logo_data_url && (
            <img
              src={branding.logo_data_url}
              alt=""
              className="h-8 w-8 rounded object-contain"
            />
          )}
          <div>
            <h1 className="text-xl font-semibold">
              {branding.display_name || "AV Bridge"}
            </h1>
            <p className="text-sm text-muted-foreground">
              Click a tile or building to drill in · browse via{" "}
              <span className="font-medium">Places</span> on the left · refreshes every 15s
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-xs text-muted-foreground">
            Last update{" "}
            {formatRelative(
              devices.lastUpdated
                ? new Date(devices.lastUpdated).toISOString()
                : null
            )}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              fleet.refresh();
              devices.refresh();
            }}
          >
            <RefreshCcw className="h-3.5 w-3.5" />
            Refresh
          </Button>
          <ConnectionIndicator />
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="p-6">
          <div className="mx-auto max-w-5xl space-y-6">
            {fleet.error && (
              <Card className="border-destructive/30 bg-destructive/5">
                <CardContent className="p-4 text-sm flex items-start gap-2">
                  <AlertTriangle className="h-4 w-4 mt-0.5 [color:hsl(var(--destructive))]" />
                  <div>
                    <div className="font-medium [color:hsl(var(--destructive))]">
                      Cannot reach av-bridge
                    </div>
                    <div className="text-muted-foreground mt-0.5">
                      {fleet.error.message}
                    </div>
                  </div>
                </CardContent>
              </Card>
            )}

            <section>
              <h2 className="sr-only">Fleet summary</h2>
              <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
                {isLoading ? (
                  <>
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                  </>
                ) : (
                  <>
                    <StatCard
                      label="Total devices"
                      value={fleet.data?.total ?? 0}
                      icon={Server}
                      href="/devices"
                    />
                    <StatCard
                      label="Online"
                      value={fleet.data?.online ?? 0}
                      icon={Wifi}
                      tone="success"
                      href="/devices?status=online"
                    />
                    <StatCard
                      label="Offline"
                      value={fleet.data?.offline ?? 0}
                      icon={CircleSlash}
                      tone="destructive"
                      href="/devices?status=offline"
                    />
                    <StatCard
                      label="Degraded"
                      value={fleet.data?.degraded ?? 0}
                      icon={AlertTriangle}
                      tone="warning"
                      href="/devices?status=degraded"
                    />
                  </>
                )}
              </div>
            </section>

            <section className="space-y-3">
              <div className="flex items-baseline justify-between">
                <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wide">
                  Buildings
                </h2>
                {buildings.length > 0 && (
                  <span className="text-xs text-muted-foreground">
                    {buildings.length} building{buildings.length === 1 ? "" : "s"}
                  </span>
                )}
              </div>
              {devices.loading && !devices.data ? (
                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
                  {[0, 1, 2].map((i) => (
                    <Skeleton key={i} className="h-32" />
                  ))}
                </div>
              ) : buildings.length === 0 ? (
                <Card>
                  <CardContent className="p-10 text-center text-sm text-muted-foreground">
                    No devices configured yet. Add a collector via{" "}
                    <Link href="/collectors" className="underline underline-offset-2">
                      Collectors
                    </Link>{" "}
                    to get started.
                  </CardContent>
                </Card>
              ) : (
                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
                  {buildings.map((b) => (
                    <BuildingTile key={b.name} b={b} />
                  ))}
                </div>
              )}
            </section>
          </div>
        </div>
      </div>
    </div>
  );
}

function BuildingTile({ b }: { b: BuildingStatus }) {
  const t = tone(b);
  const styles = TONE_STYLES[t];
  // Segment bar visualises the health mix at a glance. Widths are
  // percentages; a building with 20 total and 2 offline shows a slim
  // red band that reads as "small problem" rather than the full-red
  // scare a solid-colour ring alone would suggest.
  const seg = (n: number) => (b.total > 0 ? (n / b.total) * 100 : 0);

  const inner = (
    <Card
      className={cn(
        "h-full transition-shadow ring-1",
        styles.ring,
        b.href && "cursor-pointer"
      )}
    >
      <CardContent className="p-4 space-y-3">
        <div className="flex items-start gap-2">
          <div
            className={cn(
              "h-8 w-8 rounded-md bg-muted flex items-center justify-center shrink-0",
              t !== "neutral" && "bg-transparent"
            )}
          >
            <Building2 className={cn("h-4 w-4", styles.text)} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium text-sm">{b.name}</div>
            {(b.region || b.locationName) && (
              <div className="mt-0.5 flex items-center gap-1 text-[10px] uppercase tracking-wide text-muted-foreground/80">
                <MapPin className="h-2.5 w-2.5" />
                <span className="truncate">
                  {[b.region, b.locationName].filter(Boolean).join(" · ")}
                </span>
              </div>
            )}
          </div>
        </div>

        <div className="flex items-baseline gap-2">
          <div className="text-2xl font-semibold leading-none">{b.total}</div>
          <div className="text-xs text-muted-foreground">
            device{b.total === 1 ? "" : "s"}
          </div>
        </div>

        {/* Stacked segment bar — proportional widths, in worst-first
            order so the eye lands on trouble first. Flex row so the
            segments actually sit side-by-side without margin gymnastics. */}
        <div
          className="flex h-1.5 w-full overflow-hidden rounded-full bg-muted"
          role="img"
          aria-label={`${b.offline} offline, ${b.degraded} degraded, ${b.unknown} unknown, ${b.online} online`}
        >
          {b.offline > 0 && (
            <div className="h-full bg-destructive" style={{ width: `${seg(b.offline)}%` }} />
          )}
          {b.degraded > 0 && (
            <div className="h-full bg-warning" style={{ width: `${seg(b.degraded)}%` }} />
          )}
          {b.unknown > 0 && (
            <div className="h-full bg-muted-foreground/40" style={{ width: `${seg(b.unknown)}%` }} />
          )}
          {b.online > 0 && (
            <div className="h-full bg-success" style={{ width: `${seg(b.online)}%` }} />
          )}
        </div>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          {b.offline > 0 && (
            <StatusDot color="bg-destructive" label={`${b.offline} offline`} />
          )}
          {b.degraded > 0 && (
            <StatusDot color="bg-warning" label={`${b.degraded} degraded`} />
          )}
          {b.unknown > 0 && (
            <StatusDot color="bg-muted-foreground/40" label={`${b.unknown} unknown`} />
          )}
          {b.online > 0 && (
            <StatusDot color="bg-success" label={`${b.online} online`} />
          )}
        </div>
      </CardContent>
    </Card>
  );

  if (b.href) {
    return (
      <Link
        href={b.href}
        className="block outline-none rounded-lg focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
      >
        {inner}
      </Link>
    );
  }
  return inner;
}

function StatusDot({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", color)} />
      {label}
    </span>
  );
}
