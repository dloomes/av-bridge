import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatTimestamp(iso: string | undefined | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function formatRelative(iso: string | undefined | null): string {
  if (!iso) return "never";
  const d = new Date(iso).getTime();
  if (Number.isNaN(d)) return iso ?? "never";
  const diff = Math.max(0, Date.now() - d);
  if (diff < 1500) return "just now";
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

export function prettyMetricKey(key: string): string {
  return key
    .replace(/_/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

import type { DeviceSummary } from "./types";

export interface RoomGroup {
  room: string;
  devices: DeviceSummary[];
}

export interface BuildingGroup {
  building: string | null;
  // Region + location come off the first device in the group — every device
  // sharing a building shares the region and location above it in the
  // hierarchy, so they're safe to hoist onto the group header.
  region?: string;
  locationName?: string;
  rooms: RoomGroup[];
}

export function buildingFor(device: DeviceSummary): string | null {
  const tag = device.tags?.building?.trim();
  if (tag) return tag;
  // also accept "Building / Room" in the location field
  const loc = device.location ?? "";
  const parts = loc.split("/").map((s) => s.trim()).filter(Boolean);
  return parts.length > 1 ? parts[0] : null;
}

export function roomFor(device: DeviceSummary): string {
  const loc = device.location ?? "";
  const parts = loc.split("/").map((s) => s.trim()).filter(Boolean);
  if (parts.length > 1) return parts.slice(1).join(" / ");
  return loc.trim() || "Unassigned";
}

export function groupDevicesByLocation(
  devices: DeviceSummary[]
): BuildingGroup[] {
  const anyBuilding = devices.some((d) => buildingFor(d) !== null);

  if (!anyBuilding) {
    // Flat: just rooms
    const byRoom = new Map<string, DeviceSummary[]>();
    for (const d of devices) {
      const room = roomFor(d);
      if (!byRoom.has(room)) byRoom.set(room, []);
      byRoom.get(room)!.push(d);
    }
    return [
      {
        building: null,
        rooms: Array.from(byRoom.entries())
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([room, devs]) => ({ room, devices: sortByName(devs) })),
      },
    ];
  }

  const byBuilding = new Map<string, Map<string, DeviceSummary[]>>();
  const meta = new Map<string, { region?: string; locationName?: string }>();
  for (const d of devices) {
    const b = buildingFor(d) ?? "Other";
    const r = roomFor(d);
    if (!byBuilding.has(b)) byBuilding.set(b, new Map());
    const rooms = byBuilding.get(b)!;
    if (!rooms.has(r)) rooms.set(r, []);
    rooms.get(r)!.push(d);
    // First device wins — every device in the same building shares
    // the region/location above it. Skip empties so we don't overwrite
    // a real value with a blank from a device missing the fields.
    if (!meta.has(b) && (d.region || d.location_name)) {
      meta.set(b, { region: d.region, locationName: d.location_name });
    }
  }
  return Array.from(byBuilding.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([building, rooms]) => ({
      building,
      region: meta.get(building)?.region,
      locationName: meta.get(building)?.locationName,
      rooms: Array.from(rooms.entries())
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([room, devs]) => ({ room, devices: sortByName(devs) })),
    }));
}

function sortByName(d: DeviceSummary[]): DeviceSummary[] {
  return [...d].sort((a, b) => a.name.localeCompare(b.name));
}

export function formatMetricValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "number") return value.toString();
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
