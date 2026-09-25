"use client";

import { useMemo, useState } from "react";
import { ChevronRight, Search } from "lucide-react";
import type { NamedRow } from "@/lib/types";

// Physical scope, one id list per hierarchy level. A user sees everything
// under ANY ticked node (cloud migration 0047); all empty = full tenant.
export interface ScopeSelection {
  business_unit_ids: string[];
  region_ids: string[];
  location_ids: string[];
  building_ids: string[];
  room_ids: string[];
}

export const EMPTY_SCOPE: ScopeSelection = {
  business_unit_ids: [],
  region_ids: [],
  location_ids: [],
  building_ids: [],
  room_ids: [],
};

type Level = "business_unit" | "region" | "location" | "building" | "room";

const LEVEL_KEY: Record<Level, keyof ScopeSelection> = {
  business_unit: "business_unit_ids",
  region: "region_ids",
  location: "location_ids",
  building: "building_ids",
  room: "room_ids",
};

const LEVEL_LABEL: Record<Level, [string, string]> = {
  business_unit: ["business unit", "business units"],
  region: ["region", "regions"],
  location: ["location", "locations"],
  building: ["building", "buildings"],
  room: ["room", "rooms"],
};

interface Node {
  id: string;
  name: string;
  level: Level;
  children: Node[];
}

export function scopeIsEmpty(s: ScopeSelection): boolean {
  return Object.values(s).every((ids) => ids.length === 0);
}

// "2 regions · 1 room", or "Whole tenant" when nothing is ticked.
export function describeScope(s: ScopeSelection): string {
  const parts = (Object.keys(LEVEL_KEY) as Level[])
    .map((lvl) => {
      const n = s[LEVEL_KEY[lvl]].length;
      return n === 0 ? null : `${n} ${LEVEL_LABEL[lvl][n === 1 ? 0 : 1]}`;
    })
    .filter(Boolean);
  return parts.length === 0 ? "Whole tenant" : parts.join(" · ");
}

function buildTree(
  businessUnits: NamedRow[],
  regions: NamedRow[],
  locations: NamedRow[],
  buildings: NamedRow[],
  rooms: NamedRow[]
): Node[] {
  const byParent = (rows: NamedRow[], level: Level, childrenOf: (id: string) => Node[]) => {
    const m = new Map<string, Node[]>();
    for (const r of [...rows].sort((a, b) => a.name.localeCompare(b.name))) {
      const key = r.parent_id ?? "";
      const node: Node = { id: r.id, name: r.name, level, children: childrenOf(r.id) };
      m.set(key, [...(m.get(key) ?? []), node]);
    }
    return m;
  };
  const roomMap = byParent(rooms, "room", () => []);
  const buildingMap = byParent(buildings, "building", (id) => roomMap.get(id) ?? []);
  const locationMap = byParent(locations, "location", (id) => buildingMap.get(id) ?? []);
  const regionMap = byParent(regions, "region", (id) => locationMap.get(id) ?? []);

  if (businessUnits.length === 0) {
    return Array.from(regionMap.values()).flat().sort((a, b) => a.name.localeCompare(b.name));
  }
  const buNodes: Node[] = [...businessUnits]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((bu) => ({ id: bu.id, name: bu.name, level: "business_unit", children: regionMap.get(bu.id) ?? [] }));
  // Regions not assigned to any business unit sit alongside the BUs.
  const unassigned = regionMap.get("") ?? [];
  return [...buNodes, ...unassigned];
}

function descendants(n: Node): Node[] {
  return n.children.flatMap((c) => [c, ...descendants(c)]);
}

function roomCount(n: Node): number {
  return n.level === "room" ? 1 : n.children.reduce((sum, c) => sum + roomCount(c), 0);
}

interface Props {
  businessUnits: NamedRow[];
  regions: NamedRow[];
  locations: NamedRow[];
  buildings: NamedRow[];
  rooms: NamedRow[];
  value: ScopeSelection;
  onChange: (next: ScopeSelection) => void;
  disabled?: boolean;
}

export function ScopeTreePicker({
  businessUnits,
  regions,
  locations,
  buildings,
  rooms,
  value,
  onChange,
  disabled,
}: Props) {
  const tree = useMemo(
    () => buildTree(businessUnits, regions, locations, buildings, rooms),
    [businessUnits, regions, locations, buildings, rooms]
  );
  const ticked = useMemo(
    () => new Set(Object.values(value).flat()),
    [value]
  );
  const [filter, setFilter] = useState("");

  // Open the branches that lead to something ticked, so an existing scope
  // is visible without hunting for it.
  const [expanded, setExpanded] = useState<Set<string>>(() => {
    const open = new Set<string>();
    const walk = (n: Node, path: string[]): void => {
      if (ticked.has(n.id)) path.forEach((id) => open.add(id));
      n.children.forEach((c) => walk(c, [...path, n.id]));
    };
    tree.forEach((n) => walk(n, []));
    return open;
  });

  const toggleExpand = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // Ticking a node covers everything beneath it, so drop any descendant
  // that was ticked on its own — it would be redundant.
  const toggle = (n: Node) => {
    const key = LEVEL_KEY[n.level];
    if (ticked.has(n.id)) {
      onChange({ ...value, [key]: value[key].filter((id) => id !== n.id) });
      return;
    }
    const below = new Set(descendants(n).map((d) => d.id));
    const next = Object.fromEntries(
      Object.entries(value).map(([k, ids]) => [k, (ids as string[]).filter((id) => !below.has(id))])
    ) as unknown as ScopeSelection;
    next[key] = [...next[key], n.id];
    onChange(next);
  };

  const q = filter.trim().toLowerCase();
  const matches = (n: Node): boolean =>
    n.name.toLowerCase().includes(q) || n.children.some(matches);

  const renderNode = (n: Node, depth: number, covered: boolean) => {
    if (q && !matches(n)) return null;
    const isTicked = ticked.has(n.id);
    const open = q ? true : expanded.has(n.id);
    const hasChildren = n.children.length > 0;
    const rooms = roomCount(n);
    return (
      <li key={n.id}>
        <div
          className="flex items-center gap-1.5 py-1 pr-2 hover:bg-accent/30"
          style={{ paddingLeft: `${0.5 + depth * 1.1}rem` }}
        >
          {hasChildren ? (
            <button
              type="button"
              onClick={() => toggleExpand(n.id)}
              className="rounded p-0.5 text-muted-foreground hover:text-foreground"
              aria-label={open ? `Collapse ${n.name}` : `Expand ${n.name}`}
              aria-expanded={open}
            >
              <ChevronRight className={`h-3.5 w-3.5 transition-transform ${open ? "rotate-90" : ""}`} />
            </button>
          ) : (
            <span className="w-[1.125rem]" />
          )}
          <label className="flex flex-1 min-w-0 cursor-pointer items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={isTicked || covered}
              disabled={disabled || covered}
              onChange={() => toggle(n)}
            />
            <span className={`truncate ${covered ? "text-muted-foreground" : ""}`}>{n.name}</span>
            <span className="shrink-0 text-[11px] text-muted-foreground">
              {LEVEL_LABEL[n.level][0]}
              {n.level !== "room" && ` · ${rooms} ${rooms === 1 ? "room" : "rooms"}`}
            </span>
            {covered && (
              <span className="shrink-0 text-[11px] text-muted-foreground italic">included</span>
            )}
          </label>
        </div>
        {hasChildren && open && (
          <ul>{n.children.map((c) => renderNode(c, depth + 1, covered || isTicked))}</ul>
        )}
      </li>
    );
  };

  return (
    <div className="space-y-2">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter by name"
          className="h-8 w-full rounded-md border border-input bg-background pl-8 pr-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Filter locations"
        />
      </div>
      <div className="rounded-md border max-h-64 overflow-y-auto">
        {tree.length === 0 ? (
          <div className="p-3 text-xs text-muted-foreground">
            No locations defined yet — add them via Locations first.
          </div>
        ) : (
          <ul className="py-1">{tree.map((n) => renderNode(n, 0, false))}</ul>
        )}
      </div>
    </div>
  );
}
