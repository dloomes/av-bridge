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
import { useBranding } from "@/components/branding-provider";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type { FleetStatus } from "@/lib/types";

// Overview is a summary landing surface — four clickable stat tiles.
// Each drills through to /devices with the corresponding status filter
// pre-applied so the "how do I see the offline ones?" workflow is
// literally one click. Deeper browsing lives behind the Places tree on
// the left sidebar (building → room → device).
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
              Click a tile to drill into those devices · browse by location via{" "}
              <span className="font-medium">Places</span> on the left · refreshes every 15s
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-xs text-muted-foreground">
            Last update{" "}
            {formatRelative(
              fleet.lastUpdated
                ? new Date(fleet.lastUpdated).toISOString()
                : null
            )}
          </span>
          <Button variant="outline" size="sm" onClick={() => fleet.refresh()}>
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
                    {/* Total → all devices (no status filter). */}
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
          </div>
        </div>
      </div>
    </div>
  );
}
