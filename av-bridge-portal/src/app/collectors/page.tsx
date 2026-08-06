"use client";

import { useCallback } from "react";
import Link from "next/link";
import { CircleAlert, HelpCircle, Loader2, Server, Signal } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import type { CollectorSummary } from "@/lib/types";

// One-glance answer to "how are my collectors doing?" Table-first, no
// per-collector detail yet — clicking through takes you to the existing
// devices page filtered to that collector. Sorting is server-side (ops-first:
// offline before degraded before online) so the row that needs attention is
// always in the top pixels of the page.

type Tone = {
  label: string;
  variant: "success" | "warning" | "destructive" | "secondary";
  icon: React.ComponentType<{ className?: string }>;
};

const STATUS_TONE: Record<string, Tone> = {
  online:   { label: "Online",  variant: "success",     icon: Signal },
  degraded: { label: "Warning", variant: "warning",     icon: CircleAlert },
  offline:  { label: "Offline", variant: "destructive", icon: CircleAlert },
  unknown:  { label: "Unknown", variant: "secondary",   icon: HelpCircle },
};

const SYNC_TONE: Record<string, Tone> = {
  current: { label: "Current", variant: "success",   icon: Signal },
  stale:   { label: "Stale",   variant: "warning",   icon: CircleAlert },
  unknown: { label: "—",       variant: "secondary", icon: HelpCircle },
};

export default function CollectorsPage() {
  const fetcher = useCallback(
    (signal: AbortSignal) => api.listCollectors(signal),
    []
  );
  const { data, loading, error, refresh } = usePolling<CollectorSummary[]>(
    fetcher,
    15_000
  );

  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
              <Server aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div>
              <h1 className="text-xl font-semibold leading-tight">Collectors</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                One row per on-site bridge. Broken collectors sort to the top.
              </p>
            </div>
          </div>
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 px-6 py-6">
        <div className="mx-auto max-w-6xl space-y-4">
          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))] flex items-center justify-between">
              <span>Failed to load collectors: {error.message}</span>
              <button
                onClick={() => refresh()}
                className="text-xs underline underline-offset-2 hover:opacity-80"
              >
                Retry
              </button>
            </div>
          )}

          <Card>
            <CardContent className="p-0">
              <div className="overflow-x-auto">
                <table className="w-full min-w-[880px] text-sm">
                  <thead>
                    <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                      <th scope="col" className="px-4 py-3 font-medium">Name</th>
                      <th scope="col" className="px-4 py-3 font-medium">Building</th>
                      <th scope="col" className="px-4 py-3 font-medium">Version</th>
                      <th scope="col" className="px-4 py-3 font-medium text-right">Devices</th>
                      <th scope="col" className="px-4 py-3 font-medium">Status</th>
                      <th scope="col" className="px-4 py-3 font-medium">Config sync</th>
                      <th scope="col" className="px-4 py-3 font-medium">Last seen</th>
                    </tr>
                  </thead>
                  <tbody>
                    {loading && !data && (
                      <>
                        {[0, 1, 2].map((i) => (
                          <tr key={i} className="border-b last:border-0">
                            {[0, 1, 2, 3, 4, 5, 6].map((j) => (
                              <td key={j} className="px-4 py-3.5">
                                <Skeleton className="h-4 w-24" />
                              </td>
                            ))}
                          </tr>
                        ))}
                      </>
                    )}
                    {data && data.length === 0 && (
                      <tr>
                        <td colSpan={7} className="px-4 py-16 text-center">
                          <div className="mx-auto max-w-md space-y-2">
                            <div className="mx-auto h-10 w-10 rounded-md bg-muted flex items-center justify-center">
                              <Server aria-hidden="true" className="h-5 w-5 text-muted-foreground" />
                            </div>
                            <div className="font-medium">No collectors registered yet</div>
                            <div className="text-sm text-muted-foreground">
                              Provision one with Ansible from{" "}
                              <code className="rounded bg-muted px-1 py-0.5 text-xs">
                                av-bridge-ops/
                              </code>
                              .
                            </div>
                          </div>
                        </td>
                      </tr>
                    )}
                    {data?.map((c) => {
                      const tone = STATUS_TONE[c.status] ?? STATUS_TONE.unknown;
                      const StatusIcon = tone.icon;
                      const sync = SYNC_TONE[c.config_sync_status] ?? SYNC_TONE.unknown;
                      const SyncIcon = sync.icon;
                      return (
                        <tr
                          key={c.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3.5 align-top">
                            <div className="font-medium">{c.name}</div>
                            {c.bridge_collector_id && (
                              <div className="text-xs text-muted-foreground mt-0.5 font-mono">
                                {c.bridge_collector_id}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {c.building_name || (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {c.bridge_version ? (
                              <div>
                                <div className="font-mono text-xs">{c.bridge_version}</div>
                                {c.bridge_build_time && (
                                  <div
                                    className="text-[10px] text-muted-foreground mt-0.5"
                                    title={c.bridge_build_time}
                                  >
                                    built {formatRelative(c.bridge_build_time)}
                                  </div>
                                )}
                              </div>
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top text-right tabular-nums">
                            {c.device_count > 0 ? (
                              <Link
                                href={`/devices?collector_id=${c.id}`}
                                className="text-primary hover:underline"
                              >
                                {c.device_count}
                              </Link>
                            ) : (
                              <span className="text-muted-foreground">0</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            <Badge
                              variant={tone.variant}
                              className="gap-1 uppercase text-[10px]"
                            >
                              <StatusIcon aria-hidden="true" className="h-3 w-3" />
                              {tone.label}
                            </Badge>
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            <Badge
                              variant={sync.variant}
                              className="gap-1 uppercase text-[10px]"
                              title={
                                c.last_config_pull_at
                                  ? `Last pull ${formatRelative(c.last_config_pull_at)}`
                                  : "Bridge has not pulled config yet"
                              }
                            >
                              <SyncIcon aria-hidden="true" className="h-3 w-3" />
                              {sync.label}
                            </Badge>
                          </td>
                          <td className="px-4 py-3.5 align-top text-xs text-muted-foreground">
                            {c.last_seen_at ? (
                              formatRelative(c.last_seen_at)
                            ) : (
                              <span>never</span>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </CardContent>
          </Card>

          {loading && data && (
            <div className="flex items-center justify-center gap-2 text-xs text-muted-foreground">
              <Loader2 aria-hidden="true" className="h-3 w-3 animate-spin" />
              Refreshing…
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
