"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ArrowRight, MapPin } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/status-badge";
import { DeviceIcon } from "@/components/device-icon";
import type { DeviceSummary } from "@/lib/types";
import { api } from "@/lib/api";
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

  const make = device.tags?.make;
  const model = device.tags?.model;
  const wantedKeys = KEY_METRICS_BY_TYPE[device.type] ?? [];
  const shown = metrics
    ? wantedKeys
        .filter((k) => metrics[k] !== undefined)
        .slice(0, 3)
        .map((k) => ({ key: k, value: metrics[k] }))
    : [];

  return (
    <Card className="group transition hover:shadow-md hover:border-primary/30">
      <CardContent className="p-5 space-y-4">
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-start gap-3 min-w-0">
            <div className="mt-0.5 h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center text-primary">
              <DeviceIcon type={device.type} className="h-4 w-4" />
            </div>
            <div className="min-w-0">
              <div className="font-semibold truncate">{device.name}</div>
              <div className="flex items-center gap-1 text-xs text-muted-foreground mt-0.5">
                <MapPin className="h-3 w-3" />
                <span className="truncate">{device.location || "—"}</span>
              </div>
            </div>
          </div>
          <StatusBadge status={device.status} />
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          {make && (
            <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground">
              {make}
              {model ? ` ${model}` : ""}
            </span>
          )}
          <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground">
            {device.protocol}
          </span>
          <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground capitalize">
            {device.type}
          </span>
        </div>

        {shown.length > 0 && (
          <div className="grid grid-cols-3 gap-2">
            {shown.map((m) => (
              <div
                key={m.key}
                className="rounded-md bg-muted/40 px-2.5 py-2 min-w-0"
              >
                <div className="text-[10px] uppercase tracking-wide text-muted-foreground truncate">
                  {m.key.replace(/_/g, " ")}
                </div>
                <div className="text-sm font-medium truncate">
                  {formatMetricValue(m.value)}
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center justify-between pt-1">
          <div className="text-xs text-muted-foreground">
            Updated {formatRelative(updatedAt)}
          </div>
          <Button asChild size="sm" variant="ghost" className="h-8">
            <Link href={`/devices/${encodeURIComponent(device.id)}`}>
              View details
              <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
