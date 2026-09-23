"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ArrowRight, ExternalLink, MapPin } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/status-badge";
import { DeviceIcon } from "@/components/device-icon";
import type { DeviceSummary } from "@/lib/types";
import { api, currentToken } from "@/lib/api";
import { formatRelative, formatMetricValue } from "@/lib/utils";

interface Props {
  device: DeviceSummary;
  refreshTick: number;
}

const KEY_METRICS_BY_TYPE: Record<string, string[]> = {
  display: ["power_status", "input", "volume"],
  conferencing: ["call_state", "active_calls", "mic_mute", "mute"],
  audio: ["mute", "level", "gain"],
  camera: ["position", "preset"],
};

export function DeviceCard({ device, refreshTick }: Props) {
  const [metrics, setMetrics] = useState<Record<string, unknown> | null>(null);
  const [updatedAt, setUpdatedAt] = useState<string | null>(null);
  const [, setNow] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    api
      .getTelemetry(device.id, ctrl.signal)
      .then((tel) => {
        setMetrics(tel.metrics ?? {});
        setUpdatedAt(tel.timestamp);
      })
      .catch(() => {
        /* device may be offline; status badge already conveys this */
      });
    return () => ctrl.abort();
  }, [device.id, refreshTick]);

  // tick once a second so "X s ago" stays fresh
  useEffect(() => {
    const t = setInterval(() => setNow((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);

  // Aurora RXT touch panels expose a built-in web UI at https://<ip>/user.
  // Two link paths:
  //
  //   1. Collector has a local_url set → route through the bridge's on-prem
  //      proxy so panels on subnets the browser can't reach directly still
  //      open. Bearer token rides as a query param for the initial GET.
  //   2. No local_url → fall back to the direct-to-panel link. Works when
  //      browser + panel are on the same LAN.
  const touchPanelHref = (() => {
    if (device.protocol !== "aurora_rxt") return null;
    const localBase = device.collector_local_url?.replace(/\/+$/, "");
    if (localBase) {
      const tok = currentToken();
      return `${localBase}/api/v1/devices/${encodeURIComponent(device.id)}/touch-panel/user${
        tok ? `?token=${encodeURIComponent(tok)}` : ""
      }`;
    }
    const host = device.tags?.ip_address ?? device.address?.split(":")[0];
    return host ? `https://${host}/user` : null;
  })();

  const wantedKeys = KEY_METRICS_BY_TYPE[device.type] ?? [];
  const shown = metrics
    ? wantedKeys
        .filter((k) => metrics[k] !== undefined)
        .slice(0, 3)
        .map((k) => ({ key: k, value: metrics[k] }))
    : [];

  return (
    <Card className="group transition hover:shadow-md hover:border-primary/30">
      <CardContent className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
        <div className="flex items-center gap-2.5 min-w-0 flex-1">
          <div className="h-7 w-7 shrink-0 rounded-md bg-primary/10 flex items-center justify-center text-primary">
            <DeviceIcon type={device.type} className="h-3.5 w-3.5" />
          </div>
          <div className="min-w-0">
            <div className="font-medium text-sm truncate leading-tight">
              {device.name}
            </div>
            <div className="flex items-center gap-1 text-[11px] text-muted-foreground leading-tight">
              <MapPin className="h-3 w-3 shrink-0" />
              <span className="truncate">{device.location || "—"}</span>
            </div>
          </div>
        </div>

        {shown.length > 0 && (
          <div className="flex items-center gap-3">
            {shown.map((m) => (
              <div key={m.key} className="text-right leading-tight">
                <div className="text-[9px] uppercase tracking-wide text-muted-foreground">
                  {m.key.replace(/_/g, " ")}
                </div>
                <div className="text-xs font-medium">
                  {formatMetricValue(m.value)}
                </div>
              </div>
            ))}
          </div>
        )}

        <StatusBadge
          status={device.status}
          collectorStatus={device.collector_status}
        />

        <div className="flex items-center gap-2 ml-auto">
          <div className="text-[11px] text-muted-foreground hidden sm:block">
            {formatRelative(updatedAt)}
          </div>
          {touchPanelHref && (
            <Button asChild size="sm" variant="outline" className="h-7 px-2 gap-1 text-xs">
              <a href={touchPanelHref} target="_blank" rel="noopener noreferrer">
                Touch Panel
                <ExternalLink className="h-3 w-3" />
              </a>
            </Button>
          )}
          <Button asChild size="sm" variant="ghost" className="h-7 px-2">
            <Link href={`/devices/${encodeURIComponent(device.id)}`}>
              <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
