// Rooms grouped by building for room pickers, so a dropdown reads as
// "Leeds · HQ → Boardroom" rather than a flat list of room names (which
// collide as soon as two buildings both have a "Boardroom"). The site
// (location) is in the group label because building names can collide
// too. A room whose building isn't known — legacy data or a mid-move
// state — falls under "Other rooms" so it's still selectable.

import type { NamedRow } from "@/lib/types";

export interface RoomGroup {
  label: string; // "Site · Building", or just the building
  building: string;
  rooms: NamedRow[];
}

export const OTHER_ROOMS = "Other rooms";

export function groupRoomsByBuilding(
  rooms: NamedRow[],
  buildings: NamedRow[],
  locations: NamedRow[] = []
): RoomGroup[] {
  const site = new Map(locations.map((l) => [l.id, l.name]));
  const byID = new Map(
    buildings.map((b) => {
      const s = b.parent_id ? site.get(b.parent_id) : undefined;
      return [b.id, { building: b.name, label: s ? `${s} · ${b.name}` : b.name }];
    })
  );

  const groups = new Map<string, RoomGroup>();
  for (const r of rooms) {
    const b = (r.parent_id && byID.get(r.parent_id)) || { building: OTHER_ROOMS, label: OTHER_ROOMS };
    const key = r.parent_id && byID.has(r.parent_id) ? r.parent_id : OTHER_ROOMS;
    const g = groups.get(key) ?? { ...b, rooms: [] };
    g.rooms.push(r);
    groups.set(key, g);
  }
  return Array.from(groups.values())
    .sort((a, b) => {
      if (a.label === OTHER_ROOMS) return 1;
      if (b.label === OTHER_ROOMS) return -1;
      return a.label.localeCompare(b.label);
    })
    .map((g) => ({ ...g, rooms: [...g.rooms].sort((a, b) => a.name.localeCompare(b.name)) }));
}
