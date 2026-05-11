import type { Device } from './types';

export interface RoomGroup {
  room: string;
  devices: Device[];
}

export interface BuildingGroup {
  building: string | null;
  rooms: RoomGroup[];
}

export function buildingFor(device: Device): string | null {
  const tag = device.tags?.building?.trim();
  if (tag) return tag;
  const loc = device.location ?? '';
  const parts = loc.split('/').map((s) => s.trim()).filter(Boolean);
  return parts.length > 1 ? parts[0] : null;
}

export function roomFor(device: Device): string {
  const loc = device.location ?? '';
  const parts = loc.split('/').map((s) => s.trim()).filter(Boolean);
  if (parts.length > 1) return parts.slice(1).join(' / ');
  return loc.trim() || 'Unassigned';
}

export function groupDevicesByLocation(devices: Device[]): BuildingGroup[] {
  const anyBuilding = devices.some((d) => buildingFor(d) !== null);

  if (!anyBuilding) {
    const byRoom = new Map<string, Device[]>();
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

  const byBuilding = new Map<string, Map<string, Device[]>>();
  for (const d of devices) {
    const b = buildingFor(d) ?? 'Other';
    const r = roomFor(d);
    if (!byBuilding.has(b)) byBuilding.set(b, new Map());
    const rooms = byBuilding.get(b)!;
    if (!rooms.has(r)) rooms.set(r, []);
    rooms.get(r)!.push(d);
  }
  return Array.from(byBuilding.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([building, rooms]) => ({
      building,
      rooms: Array.from(rooms.entries())
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([room, devs]) => ({ room, devices: sortByName(devs) })),
    }));
}

function sortByName(d: Device[]): Device[] {
  return [...d].sort((a, b) => a.name.localeCompare(b.name));
}
