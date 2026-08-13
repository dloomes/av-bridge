"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import {
  ArrowLeft,
  ExternalLink,
  History,
  MapPin,
  Pencil,
  RefreshCcw,
  RotateCcw,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { UserMenu } from "@/components/user-menu";
import { StatusBadge } from "@/components/status-badge";
import { DeviceIcon } from "@/components/device-icon";
import { CommandPanel } from "@/components/command-panel";
import { TelemetryGrid } from "@/components/telemetry-grid";
import { EventFeed } from "@/components/event-feed";
import { DeviceEventHistory } from "@/components/device-event-history";
import { Modal } from "@/components/modal";
import { DeviceForm } from "@/components/device-form";
import { AuditFeed } from "@/components/audit-feed";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api, API_BASE, currentToken } from "@/lib/api";
import { canOperate, isAdmin } from "@/lib/session";
import type { DeviceDetail, Telemetry } from "@/lib/types";
import { formatRelative } from "@/lib/utils";

export default function DeviceDetailPage() {
  const params = useParams<{ id: string }>();
  const id = decodeURIComponent(params.id);
  const router = useRouter();
  const session = useSession();
  const admin = isAdmin(session.user?.role);
  const operator = canOperate(session.user?.role);

  const [device, setDevice] = useState<DeviceDetail | null>(null);
  const [deviceError, setDeviceError] = useState<Error | null>(null);
  const [reconnecting, setReconnecting] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [activityOpen, setActivityOpen] = useState(false);
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // Loader extracted so the Edit save handler can also refresh the device meta
  // after a successful PATCH without an artificial full page reload.
  const loadDevice = useCallback(
    (signal?: AbortSignal) =>
      api
        .getDevice(id, signal)
        .then((d) => {
          if (!signal?.aborted) {
            setDevice(d);
            setDeviceError(null);
          }
        })
        .catch((e) => {
          if (signal?.aborted) return;
          setDeviceError(e instanceof Error ? e : new Error(String(e)));
        }),
    [id]
  );

  useEffect(() => {
    const ctrl = new AbortController();
    void loadDevice(ctrl.signal);
    return () => ctrl.abort();
  }, [loadDevice]);

  const handleDelete = async () => {
    setDeleting(true);
    try {
      await api.deleteDevice(id);
      router.push("/");
    } catch (e) {
      setDeleteConfirmOpen(false);
      setDeviceError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setDeleting(false);
    }
  };

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
          {device.protocol === "aurora_rxt" && (
            <Button asChild variant="outline" size="sm">
              <a
                href={`${API_BASE}/api/v1/devices/${encodeURIComponent(device.id)}/touch-panel/user${currentToken() ? `?token=${encodeURIComponent(currentToken())}` : ""}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                Touch Panel
                <ExternalLink className="h-3.5 w-3.5" />
              </a>
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={() => telemetry.refresh()}
          >
            <RefreshCcw className="h-3.5 w-3.5" />
            Refresh
          </Button>
          {operator && (
            <Button
              variant="outline"
              size="sm"
              onClick={handleReconnect}
              disabled={reconnecting}
            >
              <RotateCcw className="h-3.5 w-3.5" />
              {reconnecting ? "Reconnecting…" : "Reconnect"}
            </Button>
          )}
          {admin && (
            <Button variant="outline" size="sm" onClick={() => setEditOpen(true)}>
              <Pencil className="h-3.5 w-3.5" />
              Edit
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => setActivityOpen(true)}>
            <History className="h-3.5 w-3.5" />
            Activity
          </Button>
          {admin && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDeleteConfirmOpen(true)}
            >
              <Trash2 className="h-3.5 w-3.5" />
              Delete
            </Button>
          )}
          <ConnectionIndicator />
          <UserMenu />
        </div>
      </header>

      <Modal
        open={editOpen}
        onClose={() => setEditOpen(false)}
        title={`Edit ${device.name}`}
      >
        <DeviceForm
          mode="edit"
          initial={device}
          onCancel={() => setEditOpen(false)}
          onSuccess={() => {
            setEditOpen(false);
            void loadDevice();
            telemetry.refresh();
          }}
        />
      </Modal>

      <Modal
        open={activityOpen}
        onClose={() => setActivityOpen(false)}
        title={`Activity — ${device.name}`}
      >
        <div className="max-h-[70vh] overflow-y-auto">
          <AuditFeed
            targetKind="device"
            targetId={device.id}
            emptyHint="Nothing has happened to this device yet."
          />
        </div>
      </Modal>

      <Modal
        open={deleteConfirmOpen}
        onClose={() => setDeleteConfirmOpen(false)}
        title="Delete device?"
        wide={false}
      >
        <p className="text-sm">
          Permanently delete <strong>{device.name}</strong>? Its telemetry,
          events and pending commands will also be removed. The bridge will stop
          managing it on its next config-pull tick.
        </p>
        <div className="mt-4 flex items-center justify-end gap-2">
          <Button
            variant="ghost"
            onClick={() => setDeleteConfirmOpen(false)}
            disabled={deleting}
          >
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={handleDelete}
            disabled={deleting}
          >
            {deleting ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </Modal>

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
            {operator && <CommandPanel device={device} telemetry={telemetry.data} />}
          </div>
          <aside className="space-y-6">
            <EventFeed
              deviceId={device.id}
              emptyHint="No events for this device yet."
            />
            <DeviceEventHistory deviceId={device.id} />
          </aside>
        </div>
      </div>
    </div>
  );
}
