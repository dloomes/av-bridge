"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import {
  AlertTriangle,
  Building2,
  ChevronRight,
  CircleSlash,
  DoorOpen,
  History,
  Plus,
  RefreshCcw,
  Server,
  Wifi,
} from "lucide-react";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { StatCard } from "@/components/stat-card";
import { StatusBadge } from "@/components/status-badge";
import { DeviceCard } from "@/components/device-card";
import { EventFeed } from "@/components/event-feed";
import { Modal } from "@/components/modal";
import { DeviceForm } from "@/components/device-form";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { formatRelative, groupDevicesByLocation } from "@/lib/utils";
import type {
  CollectorSummary,
  DeviceStatus,
  DeviceSummary,
  FleetStatus,
} from "@/lib/types";

export default function DashboardPage() {
  const fleet = usePolling<FleetStatus>(
    (signal) => api.fleetStatus(signal),
    15_000
  );
  const devices = usePolling<DeviceSummary[]>(
    (signal) => api.listDevices(signal),
    15_000
  );
  const collectors = usePolling<CollectorSummary[]>(
    (signal) => api.listCollectors(signal),
    15_000
  );

  const refreshTick = useMemo(
    () => devices.lastUpdated ?? 0,
    [devices.lastUpdated]
  );

  const groups = useMemo(
    () => (devices.data ? groupDevicesByLocation(devices.data) : []),
    [devices.data]
  );

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const toggle = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const [createOpen, setCreateOpen] = useState(false);

  const isLoading = fleet.loading && !fleet.data;
  const hasError = !!(fleet.error || devices.error);

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div>
          <h1 className="text-xl font-semibold">AV Bridge</h1>
          <p className="text-sm text-muted-foreground">
            Room overview · auto-refreshing every 15s
          </p>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-xs text-muted-foreground">
            Last update {formatRelative(
              devices.lastUpdated ? new Date(devices.lastUpdated).toISOString() : null
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
          <Button asChild variant="outline" size="sm">
            <Link href="/audit">
              <History className="h-3.5 w-3.5" />
              Activity
            </Link>
          </Button>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus className="h-3.5 w-3.5" />
            New device
          </Button>
          <ConnectionIndicator />
        </div>
      </header>

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title="New device"
      >
        <DeviceForm
          mode="create"
          onCancel={() => setCreateOpen(false)}
          onSuccess={() => {
            setCreateOpen(false);
            devices.refresh();
            fleet.refresh();
          }}
        />
      </Modal>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="grid gap-6 p-6 lg:grid-cols-[minmax(0,1fr)_360px]">
          <div className="space-y-6 min-w-0">
            {hasError && (
              <Card className="border-destructive/30 bg-destructive/5">
                <CardContent className="p-4 text-sm flex items-start gap-2">
                  <AlertTriangle className="h-4 w-4 mt-0.5 [color:hsl(var(--destructive))]" />
                  <div>
                    <div className="font-medium [color:hsl(var(--destructive))]">
                      Cannot reach av-bridge
                    </div>
                    <div className="text-muted-foreground mt-0.5">
                      Confirm the service is running on http://localhost:8080.{" "}
                      {fleet.error?.message ?? devices.error?.message}
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
                    />
                    <StatCard
                      label="Online"
                      value={fleet.data?.online ?? 0}
                      icon={Wifi}
                      tone="success"
                    />
                    <StatCard
                      label="Offline"
                      value={fleet.data?.offline ?? 0}
                      icon={CircleSlash}
                      tone="destructive"
                    />
                    <StatCard
                      label="Degraded"
                      value={fleet.data?.degraded ?? 0}
                      icon={AlertTriangle}
                      tone="warning"
                    />
                  </>
                )}
              </div>
            </section>

            <section className="space-y-3">
              <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wide">
                Collectors
              </h2>
              {collectors.loading && !collectors.data ? (
                <Skeleton className="h-14 w-full" />
              ) : collectors.data && collectors.data.length > 0 ? (
                <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {collectors.data.map((c) => (
                    <Card key={c.id} className="border">
                      <CardContent className="flex items-center justify-between gap-3 p-3">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium">{c.name}</div>
                          <div className="truncate text-xs text-muted-foreground">
                            {c.bridge_collector_id}
                          </div>
                          <div className="mt-1 text-[11px] text-muted-foreground">
                            {c.last_seen_at
                              ? `Last seen ${formatRelative(c.last_seen_at)}`
                              : "Never seen"}
                          </div>
                        </div>
                        <StatusBadge status={c.status as DeviceStatus} />
                      </CardContent>
                    </Card>
                  ))}
                </div>
              ) : (
                <Card>
                  <CardContent className="p-4 text-xs text-muted-foreground">
                    No collectors registered yet.
                  </CardContent>
                </Card>
              )}
            </section>

            <section className="space-y-3">
              <div className="flex items-center justify-between">
                <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wide">
                  Devices
                </h2>
                <span className="text-xs text-muted-foreground">
                  {devices.data?.length ?? 0} device
                  {devices.data?.length === 1 ? "" : "s"}
                </span>
              </div>

              {devices.loading && !devices.data ? (
                <div className="flex flex-col gap-2">
                  {[0, 1, 2].map((i) => (
                    <Skeleton key={i} className="h-12 w-full" />
                  ))}
                </div>
              ) : devices.data && devices.data.length > 0 ? (
                <div className="space-y-6">
                  {groups.map((g, gi) => {
                    const buildingKey = g.building ?? `__flat__${gi}`;
                    const buildingOpen = !collapsed.has(`b:${buildingKey}`);
                    const buildingDevices = g.rooms.reduce(
                      (n, r) => n + r.devices.length,
                      0
                    );
                    return (
                      <div key={buildingKey} className="space-y-3">
                        {g.building && (
                          <button
                            type="button"
                            onClick={() => toggle(`b:${buildingKey}`)}
                            className="flex w-full items-center gap-2 text-left hover:opacity-80"
                          >
                            <ChevronRight
                              className={`h-3.5 w-3.5 text-muted-foreground transition-transform ${
                                buildingOpen ? "rotate-90" : ""
                              }`}
                            />
                            <Building2 className="h-4 w-4 text-muted-foreground" />
                            <h3 className="text-sm font-semibold">
                              {g.building}
                            </h3>
                            <span className="text-[11px] text-muted-foreground/70">
                              · {buildingDevices} device
                              {buildingDevices === 1 ? "" : "s"}
                            </span>
                            <div className="h-px flex-1 bg-border" />
                          </button>
                        )}
                        {buildingOpen && (
                          <div
                            className={
                              g.building ? "space-y-4 pl-6" : "space-y-4"
                            }
                          >
                            {g.rooms.map((r) => {
                              const roomKey = `r:${buildingKey}::${r.room}`;
                              const roomOpen = !collapsed.has(roomKey);
                              return (
                                <div key={r.room} className="space-y-2">
                                  <button
                                    type="button"
                                    onClick={() => toggle(roomKey)}
                                    className="flex w-full items-center gap-2 text-left hover:opacity-80"
                                  >
                                    <ChevronRight
                                      className={`h-3 w-3 text-muted-foreground transition-transform ${
                                        roomOpen ? "rotate-90" : ""
                                      }`}
                                    />
                                    <DoorOpen className="h-3.5 w-3.5 text-muted-foreground" />
                                    <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                                      {r.room}
                                    </h4>
                                    <span className="text-[11px] text-muted-foreground/70">
                                      · {r.devices.length} device
                                      {r.devices.length === 1 ? "" : "s"}
                                    </span>
                                  </button>
                                  {roomOpen && (
                                    <div className="flex flex-col gap-2">
                                      {r.devices.map((d) => (
                                        <DeviceCard
                                          key={d.id}
                                          device={d}
                                          refreshTick={refreshTick}
                                        />
                                      ))}
                                    </div>
                                  )}
                                </div>
                              );
                            })}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              ) : (
                <Card>
                  <CardContent className="p-10 text-center text-sm text-muted-foreground">
                    No devices configured.
                  </CardContent>
                </Card>
              )}
            </section>
          </div>

          <aside className="lg:sticky lg:top-6 lg:h-[calc(100vh-7rem)]">
            <EventFeed />
          </aside>
        </div>
      </div>
    </div>
  );
}
