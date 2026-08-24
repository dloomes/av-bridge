"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  CircleSlash,
  RefreshCcw,
  Server,
  Wifi,
} from "lucide-react";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { StatCard } from "@/components/stat-card";
import { UserMenu } from "@/components/user-menu";
import { StatusBadge } from "@/components/status-badge";
import { useBranding } from "@/components/branding-provider";
import { FleetHealth } from "@/components/fleet-health";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type {
  CollectorSummary,
  DeviceStatus,
  DeviceSummary,
  FleetStatus,
} from "@/lib/types";

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
  // Device list still polled — it drives the FleetHealth aside (which
  // pins offline / degraded rooms to the right column) and gives the
  // sidebar's Places tree its data. The overview no longer renders a
  // per-device list of its own; that lives behind the Places nav on
  // the left and the /devices page.
  const devices = usePolling<DeviceSummary[]>(
    (signal) => api.listDevices(signal),
    15_000
  );
  const collectors = usePolling<CollectorSummary[]>(
    (signal) => api.listCollectors(signal),
    15_000
  );

  const isLoading = fleet.loading && !fleet.data;
  const hasError = !!(fleet.error || devices.error);

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
              Fleet snapshot · browse devices via <span className="font-medium">Places</span> on the left · refreshes every 15s
            </p>
          </div>
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
          <ConnectionIndicator />
          <UserMenu />
        </div>
      </header>

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

          </div>

          <aside className="lg:sticky lg:top-6 lg:h-[calc(100vh-7rem)]">
            <FleetHealth devices={devices.data} loading={devices.loading} />
          </aside>
        </div>
      </div>
    </div>
  );
}
