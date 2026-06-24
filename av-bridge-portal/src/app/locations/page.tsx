"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  ArrowLeft,
  Building2,
  ChevronRight,
  DoorOpen,
  Globe,
  MapPin,
  Pencil,
  Plus,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import {
  HierarchyForm,
  type HierarchyEditInitial,
  type HierarchyKind,
} from "@/components/hierarchy-form";
import { ConfirmDelete, type ImpactCounts } from "@/components/confirm-delete";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import type { BuildingRow, DeviceSummary, NamedRow } from "@/lib/types";

interface ModalState {
  kind: HierarchyKind;
  mode: "create" | "edit";
  parentId?: string;
  parentLabel?: string;
  initial?: HierarchyEditInitial;
}

interface DeleteState {
  kind: HierarchyKind;
  id: string;
  name: string;
  impact: ImpactCounts;
}

export default function LocationsPage() {
  const session = useSession();
  const admin = isAdmin(session.user?.role);
  const [regions, setRegions] = useState<NamedRow[] | null>(null);
  const [locations, setLocations] = useState<NamedRow[] | null>(null);
  const [buildings, setBuildings] = useState<BuildingRow[] | null>(null);
  const [rooms, setRooms] = useState<NamedRow[] | null>(null);
  const [devices, setDevices] = useState<DeviceSummary[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [modal, setModal] = useState<ModalState | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteState | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [rg, lc, bd, rm, dv] = await Promise.all([
        api.listRegions(signal),
        api.listLocations(signal),
        api.listBuildings(signal),
        api.listRooms(signal),
        api.listDevices(signal),
      ]);
      if (signal?.aborted) return;
      setRegions(rg);
      setLocations(lc);
      setBuildings(bd);
      setRooms(rm);
      setDevices(dv);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const toggle = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const locationsByRegion = useMemo(() => {
    const m = new Map<string, NamedRow[]>();
    (locations ?? []).forEach((l) => {
      if (!l.parent_id) return;
      const arr = m.get(l.parent_id) ?? [];
      arr.push(l);
      m.set(l.parent_id, arr);
    });
    return m;
  }, [locations]);

  const buildingsByLocation = useMemo(() => {
    const m = new Map<string, BuildingRow[]>();
    (buildings ?? []).forEach((b) => {
      if (!b.parent_id) return;
      const arr = m.get(b.parent_id) ?? [];
      arr.push(b);
      m.set(b.parent_id, arr);
    });
    return m;
  }, [buildings]);

  const roomsByBuilding = useMemo(() => {
    const m = new Map<string, NamedRow[]>();
    (rooms ?? []).forEach((r) => {
      if (!r.parent_id) return;
      const arr = m.get(r.parent_id) ?? [];
      arr.push(r);
      m.set(r.parent_id, arr);
    });
    return m;
  }, [rooms]);

  const devicesByRoom = useMemo(() => {
    const m = new Map<string, number>();
    (devices ?? []).forEach((d) => {
      if (!d.room_id) return;
      m.set(d.room_id, (m.get(d.room_id) ?? 0) + 1);
    });
    return m;
  }, [devices]);

  const isLoading = regions === null;

  const handleSuccess = () => {
    setModal(null);
    void load();
  };

  // Cascade impact, computed from already-fetched lists. The cloud recounts
  // server-side for the audit metadata, so the UI being a few seconds stale
  // is harmless — worst case the count shown is off by one.
  const buildImpact = useCallback(
    (kind: HierarchyKind, id: string): ImpactCounts => {
      switch (kind) {
        case "region": {
          const locs = locationsByRegion.get(id) ?? [];
          const blds = locs.flatMap((l) => buildingsByLocation.get(l.id) ?? []);
          const rms = blds.flatMap((b) => roomsByBuilding.get(b.id) ?? []);
          const dev = rms.reduce((n, r) => n + (devicesByRoom.get(r.id) ?? 0), 0);
          return {
            locations: locs.length,
            buildings: blds.length,
            rooms: rms.length,
            devicesOrphaned: dev,
          };
        }
        case "location": {
          const blds = buildingsByLocation.get(id) ?? [];
          const rms = blds.flatMap((b) => roomsByBuilding.get(b.id) ?? []);
          const dev = rms.reduce((n, r) => n + (devicesByRoom.get(r.id) ?? 0), 0);
          return {
            buildings: blds.length,
            rooms: rms.length,
            devicesOrphaned: dev,
          };
        }
        case "building": {
          const rms = roomsByBuilding.get(id) ?? [];
          const dev = rms.reduce((n, r) => n + (devicesByRoom.get(r.id) ?? 0), 0);
          return { rooms: rms.length, devicesOrphaned: dev };
        }
        case "room":
          return { devicesOrphaned: devicesByRoom.get(id) ?? 0 };
      }
    },
    [locationsByRegion, buildingsByLocation, roomsByBuilding, devicesByRoom]
  );

  const handleDelete = async () => {
    if (!deleteTarget) return;
    const table = ({
      region: "regions",
      location: "locations",
      building: "buildings",
      room: "rooms",
    } as const)[deleteTarget.kind];
    await api.deleteHierarchy(table, deleteTarget.id);
    setDeleteTarget(null);
    void load();
  };

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          <Button asChild variant="ghost" size="sm">
            <Link href="/">
              <ArrowLeft className="h-3.5 w-3.5" />
              Dashboard
            </Link>
          </Button>
          <div>
            <h1 className="text-xl font-semibold">Locations</h1>
            <p className="text-sm text-muted-foreground">
              Regions, sites, buildings, rooms
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          {admin && (
            <Button
              size="sm"
              onClick={() => setModal({ kind: "region", mode: "create" })}
            >
              <Plus className="h-3.5 w-3.5" />
              New region
            </Button>
          )}
          <UserMenu />
        </div>
      </header>

      {modal && (
        <Modal
          open
          onClose={() => setModal(null)}
          title={`${modal.mode === "edit" ? "Edit" : "New"} ${modal.kind}`}
          wide={false}
        >
          <HierarchyForm
            kind={modal.kind}
            mode={modal.mode}
            initial={modal.initial}
            parentId={modal.parentId}
            parentLabel={modal.parentLabel}
            onCancel={() => setModal(null)}
            onSuccess={handleSuccess}
          />
        </Modal>
      )}

      {deleteTarget && (
        <Modal
          open
          onClose={() => setDeleteTarget(null)}
          title={`Delete ${deleteTarget.kind}`}
          wide={false}
        >
          <ConfirmDelete
            kind={deleteTarget.kind}
            name={deleteTarget.name}
            impact={deleteTarget.impact}
            onCancel={() => setDeleteTarget(null)}
            onConfirm={handleDelete}
          />
        </Modal>
      )}

      <div className="flex-1 min-h-0 overflow-y-auto p-6">
        {loadError && (
          <Card className="mb-4 border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
              Failed to load locations: {loadError}
            </CardContent>
          </Card>
        )}

        {isLoading ? (
          <div className="flex flex-col gap-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : regions && regions.length > 0 ? (
          <div className="space-y-3">
            {regions.map((region) => {
              const regionKey = `region:${region.id}`;
              const regionOpen = !collapsed.has(regionKey);
              const regionLocations = locationsByRegion.get(region.id) ?? [];
              return (
                <Card key={region.id}>
                  <CardContent className="p-3 space-y-2">
                    <div className="flex items-center gap-2">
                      <button
                        type="button"
                        onClick={() => toggle(regionKey)}
                        className="flex flex-1 items-center gap-2 text-left hover:opacity-80"
                      >
                        <ChevronRight
                          className={`h-3.5 w-3.5 text-muted-foreground transition-transform ${
                            regionOpen ? "rotate-90" : ""
                          }`}
                        />
                        <Globe className="h-4 w-4 text-muted-foreground" />
                        <span className="font-semibold">{region.name}</span>
                        <span className="text-[11px] text-muted-foreground/70">
                          · {regionLocations.length} location
                          {regionLocations.length === 1 ? "" : "s"}
                        </span>
                      </button>
                      {admin && (
                        <>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label="Edit region"
                            onClick={() =>
                              setModal({
                                kind: "region",
                                mode: "edit",
                                initial: { id: region.id, name: region.name },
                              })
                            }
                          >
                            <Pencil className="h-3.5 w-3.5" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label="Delete region"
                            onClick={() =>
                              setDeleteTarget({
                                kind: "region",
                                id: region.id,
                                name: region.name,
                                impact: buildImpact("region", region.id),
                              })
                            }
                          >
                            <Trash2 className="h-3.5 w-3.5 text-destructive" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() =>
                              setModal({
                                kind: "location",
                                mode: "create",
                                parentId: region.id,
                                parentLabel: region.name,
                              })
                            }
                          >
                            <Plus className="h-3.5 w-3.5" />
                            Location
                          </Button>
                        </>
                      )}
                    </div>

                    {regionOpen && (
                      <div className="space-y-2 pl-6">
                        {regionLocations.length === 0 && (
                          <div className="text-xs italic text-muted-foreground">
                            No locations yet.
                          </div>
                        )}
                        {regionLocations.map((loc) => {
                          const locKey = `location:${loc.id}`;
                          const locOpen = !collapsed.has(locKey);
                          const locBuildings =
                            buildingsByLocation.get(loc.id) ?? [];
                          return (
                            <div key={loc.id} className="space-y-2">
                              <div className="flex items-center gap-2">
                                <button
                                  type="button"
                                  onClick={() => toggle(locKey)}
                                  className="flex flex-1 items-center gap-2 text-left hover:opacity-80"
                                >
                                  <ChevronRight
                                    className={`h-3 w-3 text-muted-foreground transition-transform ${
                                      locOpen ? "rotate-90" : ""
                                    }`}
                                  />
                                  <MapPin className="h-3.5 w-3.5 text-muted-foreground" />
                                  <span className="text-sm font-medium">
                                    {loc.name}
                                  </span>
                                  <span className="text-[11px] text-muted-foreground/70">
                                    · {locBuildings.length} building
                                    {locBuildings.length === 1 ? "" : "s"}
                                  </span>
                                </button>
                                {admin && (
                                  <>
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      aria-label="Edit location"
                                      onClick={() =>
                                        setModal({
                                          kind: "location",
                                          mode: "edit",
                                          initial: { id: loc.id, name: loc.name },
                                        })
                                      }
                                    >
                                      <Pencil className="h-3.5 w-3.5" />
                                    </Button>
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      aria-label="Delete location"
                                      onClick={() =>
                                        setDeleteTarget({
                                          kind: "location",
                                          id: loc.id,
                                          name: loc.name,
                                          impact: buildImpact("location", loc.id),
                                        })
                                      }
                                    >
                                      <Trash2 className="h-3.5 w-3.5 text-destructive" />
                                    </Button>
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      onClick={() =>
                                        setModal({
                                          kind: "building",
                                          mode: "create",
                                          parentId: loc.id,
                                          parentLabel: `${region.name} / ${loc.name}`,
                                        })
                                      }
                                    >
                                      <Plus className="h-3.5 w-3.5" />
                                      Building
                                    </Button>
                                  </>
                                )}
                              </div>

                              {locOpen && (
                                <div className="space-y-2 pl-6">
                                  {locBuildings.length === 0 && (
                                    <div className="text-xs italic text-muted-foreground">
                                      No buildings yet.
                                    </div>
                                  )}
                                  {locBuildings.map((bld) => {
                                    const bKey = `building:${bld.id}`;
                                    const bOpen = !collapsed.has(bKey);
                                    const bRooms =
                                      roomsByBuilding.get(bld.id) ?? [];
                                    return (
                                      <div key={bld.id} className="space-y-1">
                                        <div className="flex items-center gap-2">
                                          <button
                                            type="button"
                                            onClick={() => toggle(bKey)}
                                            className="flex flex-1 items-center gap-2 text-left hover:opacity-80"
                                          >
                                            <ChevronRight
                                              className={`h-3 w-3 text-muted-foreground transition-transform ${
                                                bOpen ? "rotate-90" : ""
                                              }`}
                                            />
                                            <Building2 className="h-3.5 w-3.5 text-muted-foreground" />
                                            <span className="text-sm">
                                              {bld.name}
                                            </span>
                                            <span className="text-[11px] text-muted-foreground/70">
                                              · {bRooms.length} room
                                              {bRooms.length === 1 ? "" : "s"}
                                            </span>
                                          </button>
                                          {admin && (
                                            <>
                                              <Button
                                                size="sm"
                                                variant="ghost"
                                                aria-label="Edit building"
                                                onClick={() =>
                                                  setModal({
                                                    kind: "building",
                                                    mode: "edit",
                                                    initial: {
                                                      id: bld.id,
                                                      name: bld.name,
                                                      address: bld.address,
                                                      timezone: bld.timezone,
                                                    },
                                                  })
                                                }
                                              >
                                                <Pencil className="h-3.5 w-3.5" />
                                              </Button>
                                              <Button
                                                size="sm"
                                                variant="ghost"
                                                aria-label="Delete building"
                                                onClick={() =>
                                                  setDeleteTarget({
                                                    kind: "building",
                                                    id: bld.id,
                                                    name: bld.name,
                                                    impact: buildImpact("building", bld.id),
                                                  })
                                                }
                                              >
                                                <Trash2 className="h-3.5 w-3.5 text-destructive" />
                                              </Button>
                                              <Button
                                                size="sm"
                                                variant="ghost"
                                                onClick={() =>
                                                  setModal({
                                                    kind: "room",
                                                    mode: "create",
                                                    parentId: bld.id,
                                                    parentLabel: `${region.name} / ${loc.name} / ${bld.name}`,
                                                  })
                                                }
                                              >
                                                <Plus className="h-3.5 w-3.5" />
                                                Room
                                              </Button>
                                            </>
                                          )}
                                        </div>

                                        {bOpen && (
                                          <ul className="space-y-0.5 pl-9">
                                            {bRooms.length === 0 ? (
                                              <li className="text-xs italic text-muted-foreground">
                                                No rooms yet.
                                              </li>
                                            ) : (
                                              bRooms.map((room) => {
                                                const deviceCount =
                                                  devicesByRoom.get(room.id) ?? 0;
                                                return (
                                                  <li
                                                    key={room.id}
                                                    className="group flex items-center gap-2 text-sm text-muted-foreground"
                                                  >
                                                    <DoorOpen className="h-3 w-3" />
                                                    <span className="flex-1">
                                                      {room.name}
                                                      {deviceCount > 0 && (
                                                        <span className="ml-2 text-[11px] text-muted-foreground/70">
                                                          · {deviceCount} device
                                                          {deviceCount === 1 ? "" : "s"}
                                                        </span>
                                                      )}
                                                    </span>
                                                    {admin && (
                                                      <>
                                                    <Button
                                                      size="sm"
                                                      variant="ghost"
                                                      aria-label="Edit room"
                                                      className="opacity-0 group-hover:opacity-100 transition-opacity"
                                                      onClick={() =>
                                                        setModal({
                                                          kind: "room",
                                                          mode: "edit",
                                                          initial: {
                                                            id: room.id,
                                                            name: room.name,
                                                          },
                                                        })
                                                      }
                                                    >
                                                      <Pencil className="h-3 w-3" />
                                                    </Button>
                                                    <Button
                                                      size="sm"
                                                      variant="ghost"
                                                      aria-label="Delete room"
                                                      className="opacity-0 group-hover:opacity-100 transition-opacity"
                                                      onClick={() =>
                                                        setDeleteTarget({
                                                          kind: "room",
                                                          id: room.id,
                                                          name: room.name,
                                                          impact: buildImpact("room", room.id),
                                                        })
                                                      }
                                                    >
                                                      <Trash2 className="h-3 w-3 text-destructive" />
                                                    </Button>
                                                      </>
                                                    )}
                                                  </li>
                                                );
                                              })
                                            )}
                                          </ul>
                                        )}
                                      </div>
                                    );
                                  })}
                                </div>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </CardContent>
                </Card>
              );
            })}
          </div>
        ) : (
          <Card>
            <CardContent className="p-10 text-center space-y-3">
              <div className="text-sm text-muted-foreground">
                {admin
                  ? "No regions yet. Create one to start organising your devices."
                  : "No regions yet — ask an admin to create one."}
              </div>
              {admin && (
                <Button onClick={() => setModal({ kind: "region", mode: "create" })}>
                  <Plus className="h-3.5 w-3.5" />
                  Create your first region
                </Button>
              )}
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
