"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  ChevronRight,
  CircleAlert,
  HelpCircle,
  Loader2,
  Plus,
  Radio,
  Search,
  Send,
  Signal,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { Modal } from "@/components/modal";
import { DeviceForm } from "@/components/device-form";
import { BulkCommandForm } from "@/components/bulk-command";
import { useSession } from "@/hooks/useSession";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import type {
  CollectorSummary,
  DeviceStatus,
  DeviceSummary,
  DeviceType,
} from "@/lib/types";

// /devices — flat listing of every device the caller can see, with
// query-string filters. Reached via:
//   * sidebar "Devices"
//   * "Devices" number on /collectors → /devices?collector_id=<uuid>
//
// Design choices:
//   * Server-side filter for collector_id (the SQL WHERE is cheap and it
//     matters for large fleets). Type/protocol/status/search filters are
//     client-side over the fetched list — page size is small enough that
//     round-tripping every keystroke would be wasteful.
//   * Deep-linking preserved: collector_id in the URL survives refresh
//     and back-button. The type/protocol/status filters intentionally
//     don't hit the URL — they're transient UI state.
//   * Row click opens the existing /devices/[id] detail page.

const STATUS_TONE: Record<
  DeviceStatus,
  { label: string; variant: "success" | "warning" | "destructive" | "secondary"; icon: React.ComponentType<{ className?: string }> }
> = {
  online:   { label: "Online",   variant: "success",     icon: Signal },
  degraded: { label: "Degraded", variant: "warning",     icon: CircleAlert },
  offline:  { label: "Offline", variant: "destructive", icon: CircleAlert },
  unknown:  { label: "Unknown",  variant: "secondary",   icon: HelpCircle },
};

const TYPE_LABEL: Record<DeviceType, string> = {
  display: "Display",
  conferencing: "Conferencing",
  audio: "Audio",
  camera: "Camera",
  control: "Control",
};

type StatusFilter = "all" | DeviceStatus;
type TypeFilter = "all" | DeviceType;

export default function DevicesPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const session = useSession();
  const collectorFilter = searchParams.get("collector_id") ?? "";

  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [typeFilter, setTypeFilter] = useState<TypeFilter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [bulkOpen, setBulkOpen] = useState(false);

  // Backend gates POST /devices on device.crud and the bulk command endpoint
  // on command.bulk — mirror those here so the buttons only appear when the
  // action would actually work (vendor bypass expands to the full set on
  // whoami so it Just Works for helpdesk callers).
  const canCreateDevices = hasPermission(session.user, "device.crud");
  const canSendBulk = hasPermission(session.user, "command.bulk");

  const fetcher = useCallback(
    (signal: AbortSignal) =>
      api.listDevices(signal, collectorFilter ? { collectorID: collectorFilter } : undefined),
    [collectorFilter]
  );
  const { data, loading, error, refresh } = usePolling<DeviceSummary[]>(
    fetcher,
    30_000,
    [collectorFilter]
  );

  // Only fetch collectors if we need to render the collector-filter chip.
  // Skips the extra round trip when the user landed on /devices directly.
  const [collectorName, setCollectorName] = useState<string>("");
  useEffect(() => {
    if (!collectorFilter) {
      setCollectorName("");
      return;
    }
    const ctrl = new AbortController();
    api
      .listCollectors(ctrl.signal)
      .then((cs: CollectorSummary[]) => {
        const match = cs.find((c) => c.id === collectorFilter);
        setCollectorName(match?.name ?? "");
      })
      .catch(() => {});
    return () => ctrl.abort();
  }, [collectorFilter]);

  const filtered = useMemo(() => {
    if (!data) return null;
    const needle = search.trim().toLowerCase();
    return data.filter((d) => {
      if (statusFilter !== "all" && d.status !== statusFilter) return false;
      if (typeFilter !== "all" && d.type !== typeFilter) return false;
      if (needle) {
        const hay =
          d.name.toLowerCase() +
          " " +
          (d.location ?? "").toLowerCase() +
          " " +
          (d.address ?? "").toLowerCase() +
          " " +
          (d.protocol ?? "").toLowerCase();
        if (!hay.includes(needle)) return false;
      }
      return true;
    });
  }, [data, search, statusFilter, typeFilter]);

  const counts = useMemo(() => {
    if (!data) return { total: 0, online: 0, offline: 0, degraded: 0 };
    return data.reduce(
      (acc, d) => {
        acc.total += 1;
        if (d.status === "online") acc.online += 1;
        else if (d.status === "offline") acc.offline += 1;
        else if (d.status === "degraded") acc.degraded += 1;
        return acc;
      },
      { total: 0, online: 0, offline: 0, degraded: 0 }
    );
  }, [data]);

  const clearCollectorFilter = () => router.push("/devices");

  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
              <Radio aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div>
              <h1 className="text-xl font-semibold leading-tight">Devices</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                {counts.total} total · {counts.online} online · {counts.offline} offline
                {counts.degraded > 0 && ` · ${counts.degraded} degraded`}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {canSendBulk && (
              <Button variant="outline" size="sm" onClick={() => setBulkOpen(true)}>
                <Send className="h-3.5 w-3.5" />
                Send to group
              </Button>
            )}
            {canCreateDevices && (
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus className="h-3.5 w-3.5" />
                New device
              </Button>
            )}
            <UserMenu />
          </div>
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
            refresh();
          }}
        />
      </Modal>

      <Modal
        open={bulkOpen}
        onClose={() => setBulkOpen(false)}
        title="Send command to devices"
      >
        <BulkCommandForm
          devices={data ?? []}
          onClose={() => setBulkOpen(false)}
        />
      </Modal>

      <div className="flex-1 px-6 py-6">
        <div className="mx-auto max-w-6xl space-y-4">
          {collectorFilter && (
            <div className="flex items-center gap-2 rounded-md border bg-muted/40 px-3 py-2 text-sm">
              <span className="text-muted-foreground">Filtering to collector:</span>
              <Badge variant="secondary" className="gap-1">
                {collectorName || "…"}
              </Badge>
              <button
                onClick={clearCollectorFilter}
                className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                aria-label="Clear collector filter"
              >
                <X aria-hidden="true" className="h-3 w-3" />
                Clear
              </button>
            </div>
          )}

          <div className="flex flex-wrap gap-2">
            <div className="relative flex-1 min-w-[240px]">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
              />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search name, room, address, protocol…"
                className="w-full rounded-md border bg-background pl-8 pr-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
              className="rounded-md border bg-background px-3 py-2 text-sm"
              aria-label="Filter by status"
            >
              <option value="all">All statuses</option>
              <option value="online">Online</option>
              <option value="degraded">Degraded</option>
              <option value="offline">Offline</option>
              <option value="unknown">Unknown</option>
            </select>
            <select
              value={typeFilter}
              onChange={(e) => setTypeFilter(e.target.value as TypeFilter)}
              className="rounded-md border bg-background px-3 py-2 text-sm"
              aria-label="Filter by type"
            >
              <option value="all">All types</option>
              <option value="display">Display</option>
              <option value="conferencing">Conferencing</option>
              <option value="audio">Audio</option>
              <option value="camera">Camera</option>
              <option value="control">Control</option>
            </select>
          </div>

          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))] flex items-center justify-between">
              <span>Failed to load devices: {error.message}</span>
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
                <table className="w-full min-w-[720px] text-sm">
                  <thead>
                    <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                      <th scope="col" className="px-4 py-3 font-medium">Name</th>
                      <th scope="col" className="px-4 py-3 font-medium">Location</th>
                      <th scope="col" className="px-4 py-3 font-medium">Type</th>
                      <th scope="col" className="px-4 py-3 font-medium">Protocol</th>
                      <th scope="col" className="px-4 py-3 font-medium">Status</th>
                      <th scope="col" className="px-4 py-3 font-medium">
                        <span className="sr-only">Details</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {loading && !data && (
                      <>
                        {[0, 1, 2, 3].map((i) => (
                          <tr key={i} className="border-b last:border-0">
                            {[0, 1, 2, 3, 4, 5].map((j) => (
                              <td key={j} className="px-4 py-3.5">
                                <Skeleton className="h-4 w-24" />
                              </td>
                            ))}
                          </tr>
                        ))}
                      </>
                    )}
                    {filtered && filtered.length === 0 && (
                      <tr>
                        <td colSpan={6} className="px-4 py-16 text-center">
                          <div className="mx-auto max-w-md space-y-2">
                            <div className="mx-auto h-10 w-10 rounded-md bg-muted flex items-center justify-center">
                              <Radio
                                aria-hidden="true"
                                className="h-5 w-5 text-muted-foreground"
                              />
                            </div>
                            <div className="font-medium">
                              {data && data.length === 0
                                ? "No devices yet"
                                : "Nothing matches your filters"}
                            </div>
                            {data && data.length === 0 ? (
                              <div className="text-sm text-muted-foreground">
                                Add one from{" "}
                                <Link href="/locations" className="text-primary hover:underline">
                                  Locations
                                </Link>{" "}
                                — devices attach to a room.
                              </div>
                            ) : (
                              <div className="text-sm text-muted-foreground">
                                Try clearing a filter or the search box.
                              </div>
                            )}
                          </div>
                        </td>
                      </tr>
                    )}
                    {filtered?.map((d) => {
                      const tone = STATUS_TONE[d.status] ?? STATUS_TONE.unknown;
                      const StatusIcon = tone.icon;
                      return (
                        <tr
                          key={d.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3.5 align-top">
                            <Link
                              href={`/devices/${d.id}`}
                              className="font-medium hover:underline"
                            >
                              {d.name}
                            </Link>
                            {d.address && (
                              <div className="text-xs text-muted-foreground mt-0.5 font-mono">
                                {d.address}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top text-muted-foreground">
                            {d.location || <span>—</span>}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {TYPE_LABEL[d.type] ?? d.type}
                          </td>
                          <td className="px-4 py-3.5 align-top text-xs font-mono text-muted-foreground">
                            {d.protocol}
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
                          <td className="px-4 py-3.5 align-top text-right">
                            <Link href={`/devices/${d.id}`}>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8"
                                aria-label={`View ${d.name}`}
                              >
                                View
                                <ChevronRight
                                  aria-hidden="true"
                                  className="h-3.5 w-3.5"
                                />
                              </Button>
                            </Link>
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
