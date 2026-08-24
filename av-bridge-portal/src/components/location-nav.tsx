"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useMemo, useState } from "react";
import { Building2, ChevronRight, DoorOpen } from "lucide-react";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import {
  cn,
  groupDevicesByLocation,
  type BuildingGroup,
} from "@/lib/utils";
import type { DeviceStatus, DeviceSummary } from "@/lib/types";
import { DeviceIcon } from "@/components/device-icon";

const dotColor: Record<DeviceStatus, string> = {
  online: "bg-success",
  offline: "bg-destructive",
  degraded: "bg-warning",
  unknown: "bg-muted-foreground/40",
};

export function LocationNav() {
  const { data, error, loading } = usePolling<DeviceSummary[]>(
    (signal) => api.listDevices(signal),
    30_000
  );

  const groups = useMemo(
    () => (data ? groupDevicesByLocation(data) : []),
    [data]
  );

  if (loading && !data) {
    return <SidebarHint text="Loading locations…" />;
  }
  if (error && !data) {
    return <SidebarHint text="No connection" tone="error" />;
  }
  if (!data || data.length === 0) {
    return <SidebarHint text="No devices configured" />;
  }

  return (
    <div className="space-y-3">
      {groups.map((g, i) => (
        <BuildingBlock key={g.building ?? `__flat__${i}`} group={g} />
      ))}
    </div>
  );
}

function BuildingBlock({ group }: { group: BuildingGroup }) {
  const [open, setOpen] = useState(true);
  if (group.building === null) {
    // Flat — render rooms directly without a building wrapper
    return (
      <div className="space-y-2">
        {group.rooms.map((r) => (
          <RoomBlock
            key={r.room}
            room={r.room}
            building={null}
            devices={r.devices}
          />
        ))}
      </div>
    );
  }
  const buildingName = group.building;
  return (
    <div>
      <div className="flex w-full items-center gap-1 px-2 py-1 text-xs font-semibold uppercase tracking-wide text-sidebar-foreground/60">
        {/* Chevron is its own button so the label link + expand toggle don't
            steal each other's clicks. Small hit target on purpose — the
            main affordance is the label, which navigates. */}
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-label={open ? `Collapse ${buildingName}` : `Expand ${buildingName}`}
          className="rounded p-0.5 hover:bg-white/10 hover:text-sidebar-foreground"
        >
          <ChevronRight
            className={cn(
              "h-3 w-3 transition-transform",
              open && "rotate-90"
            )}
          />
        </button>
        <Link
          href={`/devices?building=${encodeURIComponent(buildingName)}`}
          className="flex min-w-0 flex-1 items-center gap-2 hover:text-sidebar-foreground"
          title={`Show devices in ${buildingName}`}
        >
          <Building2 className="h-3.5 w-3.5" />
          <span className="truncate">{buildingName}</span>
        </Link>
      </div>
      {open && (
        <div className="mt-1 space-y-2 pl-3">
          {group.rooms.map((r) => (
            <RoomBlock
              key={r.room}
              room={r.room}
              building={buildingName}
              devices={r.devices}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function RoomBlock({
  room,
  building,
  devices,
}: {
  room: string;
  building: string | null;
  devices: DeviceSummary[];
}) {
  const [open, setOpen] = useState(true);
  const params = new URLSearchParams();
  if (building) params.set("building", building);
  params.set("room", room);
  const roomHref = `/devices?${params.toString()}`;
  return (
    <div>
      <div className="flex w-full items-center gap-1 px-2 py-1 text-[11px] font-medium uppercase tracking-wide text-sidebar-foreground/50">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-label={open ? `Collapse ${room}` : `Expand ${room}`}
          className="rounded p-0.5 hover:bg-white/10 hover:text-sidebar-foreground"
        >
          <ChevronRight
            className={cn(
              "h-3 w-3 transition-transform",
              open && "rotate-90"
            )}
          />
        </button>
        <Link
          href={roomHref}
          className="flex min-w-0 flex-1 items-center gap-2 hover:text-sidebar-foreground"
          title={`Show devices in ${room}`}
        >
          <DoorOpen className="h-3.5 w-3.5" />
          <span className="truncate">{room}</span>
          <span className="ml-auto text-[10px] text-sidebar-foreground/40">
            {devices.length}
          </span>
        </Link>
      </div>
      {open && (
        <ul className="mt-0.5 space-y-0.5 pl-3">
          {devices.map((d) => (
            <DeviceLink key={d.id} device={d} />
          ))}
        </ul>
      )}
    </div>
  );
}

function DeviceLink({ device }: { device: DeviceSummary }) {
  const params = useParams<{ id?: string }>();
  const activeId = params?.id ? decodeURIComponent(params.id) : null;
  const active = activeId === device.id;

  return (
    <li>
      <Link
        href={`/devices/${encodeURIComponent(device.id)}`}
        className={cn(
          "flex items-center gap-2 rounded-md px-2 py-1.5 text-xs transition-colors",
          active
            ? "bg-white/10 text-white"
            : "text-sidebar-foreground/75 hover:bg-white/5 hover:text-white"
        )}
        title={device.name}
      >
        <DeviceIcon
          type={device.type}
          className="h-3.5 w-3.5 flex-shrink-0 text-sidebar-foreground/60"
        />
        <span className="truncate">{device.name}</span>
        <span
          className={cn(
            "ml-auto h-1.5 w-1.5 flex-shrink-0 rounded-full",
            dotColor[device.status]
          )}
          aria-label={device.status}
        />
      </Link>
    </li>
  );
}

function SidebarHint({
  text,
  tone = "muted",
}: {
  text: string;
  tone?: "muted" | "error";
}) {
  return (
    <div
      className={cn(
        "px-2 py-1 text-xs",
        tone === "error"
          ? "text-destructive-foreground/80 [color:hsl(var(--destructive))]"
          : "text-sidebar-foreground/50"
      )}
    >
      {text}
    </div>
  );
}
