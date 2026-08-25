"use client";

// Map view — the geographic sibling of the overview page. Same fleet
// polls, same tile row, but the middle of the page renders a Mapbox
// map with a coloured pin per building instead of the tile grid.
// Buildings without coordinates fall through to a list beneath the
// map so nothing is invisible.

import { useEffect, useMemo } from "react";
import Link from "next/link";
import dynamic from "next/dynamic";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  Bell,
  Building2,
  CircleSlash,
  DoorOpen,
  MapPin,
  RefreshCcw,
  Server,
  ServerCrash,
  Wifi,
} from "lucide-react";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { SetAsDefaultToggle } from "@/components/set-as-default-toggle";
import { StatCard } from "@/components/stat-card";
import { UserMenu } from "@/components/user-menu";
import { useBranding } from "@/components/branding-provider";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { formatRelative, groupDevicesByLocation } from "@/lib/utils";
import type {
  AlertsSummary,
  BuildingRow,
  CollectorSummary,
  DeviceStatus,
  DeviceSummary,
  FleetStatus,
} from "@/lib/types";
import type { BuildingsMapEntry } from "@/components/buildings-map";

// Mapbox GL touches window at import time, so the whole map component
// has to skip SSR. This dynamic import keeps the rest of the page
// server-rendered — the map only spins up in the browser.
const BuildingsMap = dynamic(
  () => import("@/components/buildings-map").then((m) => m.BuildingsMap),
  {
    ssr: false,
    loading: () => (
      <div className="flex h-[480px] items-center justify-center rounded-lg border border-border bg-muted/20">
        <Skeleton className="h-full w-full" />
      </div>
    ),
  }
);

const MAPBOX_TOKEN = process.env.NEXT_PUBLIC_MAPBOX_TOKEN ?? "";

// Worst-first precedence — matches the overview tile grid so the two
// pages never disagree on which status a building shows.
function worstOf(devices: DeviceSummary[]): DeviceStatus {
  if (devices.some((d) => d.status === "offline")) return "offline";
  if (devices.some((d) => d.status === "degraded")) return "degraded";
  if (devices.some((d) => d.status === "unknown")) return "unknown";
  return "online";
}

export default function MapPage() {
  const session = useSession();
  const router = useRouter();
  const { branding } = useBranding();

  useEffect(() => {
    if (!session.hydrated) return;
    if (session.user?.is_vendor && !session.scope) {
      router.replace("/helpdesk");
    }
  }, [session.hydrated, session.user, session.scope, router]);

  const fleet = usePolling<FleetStatus>((s) => api.fleetStatus(s), 15_000);
  const devices = usePolling<DeviceSummary[]>((s) => api.listDevices(s), 30_000);
  const alerts = usePolling<AlertsSummary>((s) => api.alertsSummary(s), 15_000);
  const collectors = usePolling<CollectorSummary[]>(
    (s) => api.listCollectors(s),
    15_000
  );
  const buildings = usePolling<BuildingRow[]>(
    (s) => api.listBuildings(s),
    60_000
  );

  // Merge the buildings list with the device rollup so each map entry
  // carries a worst-status and per-status counts. We key the device
  // rollup by lower-case name to survive light casing drift between
  // the buildings table and the tag/location field on devices.
  const entries = useMemo<BuildingsMapEntry[]>(() => {
    if (!buildings.data || !devices.data) return [];
    const groups = groupDevicesByLocation(devices.data);
    const byName = new Map<string, DeviceSummary[]>();
    for (const g of groups) {
      if (!g.building) continue;
      byName.set(
        g.building.toLowerCase(),
        g.rooms.flatMap((r) => r.devices)
      );
    }
    return buildings.data.map<BuildingsMapEntry>((b) => {
      const devs = byName.get(b.name.toLowerCase()) ?? [];
      const totals = devs.reduce(
        (acc, d) => {
          acc.total += 1;
          if (d.status === "online") acc.online += 1;
          else if (d.status === "offline") acc.offline += 1;
          else if (d.status === "degraded") acc.degraded += 1;
          else acc.unknown += 1;
          return acc;
        },
        { total: 0, online: 0, offline: 0, degraded: 0, unknown: 0 }
      );
      return { building: b, worst: worstOf(devs), totals };
    });
  }, [buildings.data, devices.data]);

  const missingCoords = useMemo(
    () =>
      entries.filter(
        (e) =>
          typeof e.building.latitude !== "number" ||
          typeof e.building.longitude !== "number"
      ),
    [entries]
  );

  const roomStats = useMemo(() => {
    if (!devices.data) return null;
    const groups = groupDevicesByLocation(devices.data);
    let total = 0;
    let withIssue = 0;
    for (const g of groups) {
      for (const r of g.rooms) {
        total += 1;
        if (r.devices.some((d) => d.status === "offline" || d.status === "degraded")) {
          withIssue += 1;
        }
      }
    }
    return { total, withIssue };
  }, [devices.data]);

  const collectorStats = useMemo(() => {
    if (!collectors.data) return null;
    let offline = 0;
    for (const c of collectors.data) {
      if (c.status === "offline") offline += 1;
    }
    return { offline };
  }, [collectors.data]);

  const isLoading = fleet.loading && !fleet.data;
  const isRow2Loading =
    (devices.loading && !devices.data) ||
    (alerts.loading && !alerts.data) ||
    (collectors.loading && !collectors.data);

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
              {branding.display_name || "AV Bridge"} · Map
            </h1>
            <p className="text-sm text-muted-foreground">
              Geographic view of every building on the fleet · refreshes every 15s
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <SetAsDefaultToggle page="map" />
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
              alerts.refresh();
              collectors.refresh();
              buildings.refresh();
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
          <div className="mx-auto max-w-6xl space-y-6">
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

            <section className="space-y-4">
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
                    <StatCard label="Total devices" value={fleet.data?.total ?? 0} icon={Server} href="/devices" />
                    <StatCard label="Online" value={fleet.data?.online ?? 0} icon={Wifi} tone="success" href="/devices?status=online" />
                    <StatCard label="Offline" value={fleet.data?.offline ?? 0} icon={CircleSlash} tone="destructive" href="/devices?status=offline" />
                    <StatCard label="Degraded" value={fleet.data?.degraded ?? 0} icon={AlertTriangle} tone="warning" href="/devices?status=degraded" />
                  </>
                )}
              </div>
              <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
                {isRow2Loading ? (
                  <>
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                    <Skeleton className="h-[88px]" />
                  </>
                ) : (
                  <>
                    <StatCard label="Rooms" value={roomStats?.total ?? 0} icon={DoorOpen} />
                    <StatCard
                      label="Rooms with issues"
                      value={roomStats?.withIssue ?? 0}
                      icon={AlertTriangle}
                      tone={(roomStats?.withIssue ?? 0) > 0 ? "destructive" : "neutral"}
                      href={(roomStats?.withIssue ?? 0) > 0 ? "/devices?status=offline" : undefined}
                    />
                    <StatCard
                      label={(alerts.data?.critical_open ?? 0) > 0 ? "Open alerts (critical)" : "Open alerts"}
                      value={alerts.data?.open ?? 0}
                      icon={Bell}
                      tone={
                        (alerts.data?.critical_open ?? 0) > 0
                          ? "destructive"
                          : (alerts.data?.open ?? 0) > 0
                          ? "warning"
                          : "success"
                      }
                      href="/alerts"
                    />
                    <StatCard
                      label="Offline collectors"
                      value={collectorStats?.offline ?? 0}
                      icon={ServerCrash}
                      tone={(collectorStats?.offline ?? 0) > 0 ? "destructive" : "success"}
                      href="/collectors"
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
                {buildings.data && (
                  <span className="text-xs text-muted-foreground">
                    {entries.length - missingCoords.length} pinned ·{" "}
                    {missingCoords.length} without coords
                  </span>
                )}
              </div>
              {buildings.loading && !buildings.data ? (
                <Skeleton className="h-[480px]" />
              ) : (
                <BuildingsMap entries={entries} mapboxToken={MAPBOX_TOKEN} />
              )}
            </section>

            {missingCoords.length > 0 && (
              <section className="space-y-3">
                <div className="flex items-baseline justify-between">
                  <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wide">
                    Missing coordinates
                  </h2>
                  <Link
                    href="/locations"
                    className="text-xs text-primary underline-offset-4 hover:underline"
                  >
                    Add coordinates on Locations →
                  </Link>
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
                  {missingCoords.map((e) => (
                    <Card key={e.building.id}>
                      <CardContent className="flex items-start gap-3 p-4">
                        <div className="grid h-9 w-9 shrink-0 place-items-center rounded-md bg-muted">
                          <Building2 className="h-4 w-4 text-muted-foreground" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">
                            {e.building.name}
                          </div>
                          {e.building.address && (
                            <div className="mt-0.5 flex items-center gap-1 truncate text-[10px] uppercase tracking-wide text-muted-foreground">
                              <MapPin className="h-2.5 w-2.5" />
                              <span className="truncate">{e.building.address}</span>
                            </div>
                          )}
                          <div className="mt-1 text-[11px] text-muted-foreground">
                            {e.totals.total} device{e.totals.total === 1 ? "" : "s"}
                          </div>
                        </div>
                      </CardContent>
                    </Card>
                  ))}
                </div>
              </section>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
