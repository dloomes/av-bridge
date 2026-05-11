"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useState } from "react";
import {
  ArrowLeft,
  MapPin,
  RefreshCcw,
  RotateCcw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { StatusBadge } from "@/components/status-badge";
import { DeviceIcon } from "@/components/device-icon";
import { CommandPanel } from "@/components/command-panel";
import { TelemetryGrid } from "@/components/telemetry-grid";
import { EventFeed } from "@/components/event-feed";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import type { DeviceDetail, Telemetry } from "@/lib/types";
import { formatRelative } from "@/lib/utils";

export default function DeviceDetailPage() {
  const params = useParams<{ id: string }>();
  const id = decodeURIComponent(params.id);

  const [device, setDevice] = useState<DeviceDetail | null>(null);
  const [deviceError, setDeviceError] = useState<Error | null>(null);
  const [reconnecting, setReconnecting] = useState(false);

  // Static-ish device meta — refresh once on load
  useEffect(() => {
    const ctrl = new AbortController();
    api
      .getDevice(id, ctrl.signal)
      .then((d) => {
        if (!ctrl.signal.aborted) setDevice(d);
      })
      .catch((e) => {
        if (ctrl.signal.aborted) return;
        setDeviceError(e instanceof Error ? e : new Error(String(e)));
      });
    return () => ctrl.abort();
  }, [id]);

  const telemetry = usePolling<Telemetry>(
    (signal) => api.getTelemetry(id, signal),
    10_000,
    [id]
  );

  const handleReconnect = async () => {
    setReconnecting(true);
    try {
      await api.reconnectDevice(id);
      telemetry.refresh();
    } catch {
      /* surfaced via telemetry error */
    } finally {
      setReconnecting(false);
    }
  };

  if (deviceError) {
    return (
      <div className="p-6 max-w-2xl mx-auto">
        <Button asChild variant="ghost" size="sm" className="mb-4">
          <Link href="/">
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </Link>
        </Button>
        <Card className="border-destructive/30 bg-destructive/5">
          <CardContent className="p-6 text-sm">
            <div className="font-medium [color:hsl(var(--destructive))]">
              Could not load device
            </div>
            <div className="text-muted-foreground mt-1">
              {deviceError.message}
            </div>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!device) {
    return (
      <div className="p-6 space-y-6 max-w-5xl">
        <Skeleton className="h-9 w-40" />
        <Skeleton className="h-32" />
        <Skeleton className="h-64" />
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3 min-w-0">
          <Button asChild variant="ghost" size="sm">
            <Link href="/">
              <ArrowLeft className="h-3.5 w-3.5" />
              Back
            </Link>
          </Button>
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center text-primary">
            <DeviceIcon type={device.type} />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="font-semibold truncate">{device.name}</h1>
              <StatusBadge status={device.status} />
            </div>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <MapPin className="h-3 w-3" />
              <span>{device.location || "—"}</span>
              <span>·</span>
              <span className="capitalize">{device.type}</span>
              <span>·</span>
              <span>{device.protocol}</span>
              {device.tags?.make && (
                <>
                  <span>·</span>
                  <span>
                    {device.tags.make}
                    {device.tags.model ? ` ${device.tags.model}` : ""}
                  </span>
                </>
              )}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">
            Updated{" "}
            {formatRelative(
              telemetry.lastUpdated
                ? new Date(telemetry.lastUpdated).toISOString()
                : null
            )}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => telemetry.refresh()}
          >
            <RefreshCcw className="h-3.5 w-3.5" />
            Refresh
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={handleReconnect}
            disabled={reconnecting}
          >
            <RotateCcw className="h-3.5 w-3.5" />
            {reconnecting ? "Reconnecting…" : "Reconnect"}
          </Button>
          <ConnectionIndicator />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="grid gap-6 p-6 lg:grid-cols-[minmax(0,1fr)_360px]">
          <div className="space-y-6 min-w-0">
            <TelemetryGrid
              metrics={telemetry.data?.metrics}
              lensMetrics={telemetry.data?.lens_metrics}
              error={
                telemetry.error?.message ?? telemetry.data?.error ?? undefined
              }
            />
            <CommandPanel device={device} telemetry={telemetry.data} />
          </div>
          <aside className="lg:sticky lg:top-6 lg:h-[calc(100vh-7rem)]">
            <EventFeed
              deviceId={device.id}
              emptyHint="No events for this device yet."
            />
          </aside>
        </div>
      </div>
    </div>
  );
}
