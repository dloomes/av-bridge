"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Briefcase,
  Building2,
  ChevronRight,
  DoorOpen,
  Globe,
  Loader2,
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
import type { Branding, BusinessUnit } from "@/lib/api";
import { hasPermission } from "@/lib/session";
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
  const admin = hasPermission(session.user, "hierarchy.crud");
  // Vendor admins bypass tenant RBAC on the backend; mirror that on the
  // portal so a vendor scoped into a customer sees the manage controls
  // even when their cached whoami hasn't picked up a newly-added
  // permission key yet.
  const canManageBU =
    !!session.user?.is_vendor ||
    hasPermission(session.user, "business_unit.crud");
  const [regions, setRegions] = useState<NamedRow[] | null>(null);
  const [locations, setLocations] = useState<NamedRow[] | null>(null);
  const [buildings, setBuildings] = useState<BuildingRow[] | null>(null);
  const [rooms, setRooms] = useState<NamedRow[] | null>(null);
  const [devices, setDevices] = useState<DeviceSummary[] | null>(null);
  const [businessUnits, setBusinessUnits] = useState<BusinessUnit[] | null>(null);
  const [buFlagOn, setBUFlagOn] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [modal, setModal] = useState<ModalState | null>(null);
  const [buModal, setBUModal] = useState<{ mode: "create" | "edit"; unit?: BusinessUnit } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteState | null>(null);
  const [deleteBU, setDeleteBU] = useState<BusinessUnit | null>(null);
  const [deletingBU, setDeletingBU] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      // BU tier + flag come from branding (business_units_enabled) and
      // /business-units — the endpoint returns [] when the tenant has
      // none, so a failure to load is treated as "no BUs" rather than
      // a hard error (keeps the page working for the 99% of tenants that
      // never enable the feature).
      const [rg, lc, bd, rm, dv, br, bus] = await Promise.all([
        api.listRegions(signal),
        api.listLocations(signal),
        api.listBuildings(signal),
        api.listRooms(signal),
        api.listDevices(signal),
        api.getBranding(signal).catch(() => ({} as Branding)),
        api.listBusinessUnits(signal).catch(() => [] as BusinessUnit[]),
      ]);
      if (signal?.aborted) return;
      setRegions(rg);
      setLocations(lc);
      setBuildings(bd);
      setRooms(rm);
      setDevices(dv);
      setBUFlagOn(Boolean(br.business_units_enabled));
      setBusinessUnits(bus);
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
        <div>
          <h1 className="text-xl font-semibold">Locations</h1>
          <p className="text-sm text-muted-foreground">
            Regions, sites, buildings, rooms
          </p>
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
            businessUnits={buFlagOn ? businessUnits : null}
            onCancel={() => setModal(null)}
            onSuccess={handleSuccess}
          />
        </Modal>
      )}

      {buModal && (
        <Modal
          open
          onClose={() => setBUModal(null)}
          title={`${buModal.mode === "edit" ? "Edit" : "New"} business unit`}
          wide={false}
        >
          <BusinessUnitForm
            mode={buModal.mode}
            initial={buModal.unit}
            onCancel={() => setBUModal(null)}
            onSaved={async () => {
              setBUModal(null);
              await load();
            }}
          />
        </Modal>
      )}

      {deleteBU && (
        <Modal
          open
          onClose={() => (deletingBU ? undefined : setDeleteBU(null))}
          title={`Delete business unit`}
          wide={false}
        >
          <div className="space-y-3">
            <p className="text-sm">
              Delete <span className="font-medium">{deleteBU.name}</span>?
            </p>
            <p className="text-xs text-muted-foreground">
              Regions assigned to this business unit will be reverted to
              unassigned. No regions, locations, buildings, rooms, or
              devices are deleted.
            </p>
            <div className="flex items-center justify-end gap-2 pt-2 border-t">
              <Button
                variant="ghost"
                onClick={() => setDeleteBU(null)}
                disabled={deletingBU}
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                disabled={deletingBU}
                onClick={async () => {
                  setDeletingBU(true);
                  try {
                    await api.deleteBusinessUnit(deleteBU.id);
                    setDeleteBU(null);
                    await load();
                  } catch (e) {
                    alert((e as Error).message);
                  } finally {
                    setDeletingBU(false);
                  }
                }}
              >
                {deletingBU && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                Delete
              </Button>
            </div>
          </div>
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

        {/*
          Business Units section — only rendered when the tenant has the
          business_units_enabled flag on. Sits above the regions list because
          BUs are the parent tier. Even when the flag is on, the section can
          be empty (no BUs created yet) and shows an inline hint.
        */}
        {buFlagOn && (
          <Card className="mb-4">
            <CardContent className="p-3 space-y-2">
              <div className="flex items-center gap-2">
                <Briefcase className="h-4 w-4 text-muted-foreground" />
                <span className="font-semibold">Business units</span>
                <span className="text-[11px] text-muted-foreground/70">
                  · {businessUnits?.length ?? 0}
                </span>
                <div className="ml-auto">
                  {canManageBU && (
                    <Button
                      size="sm"
                      onClick={() => setBUModal({ mode: "create" })}
                    >
                      <Plus className="h-3.5 w-3.5" />
                      New business unit
                    </Button>
                  )}
                </div>
              </div>
              {businessUnits && businessUnits.length > 0 ? (
                <div className="rounded-md border divide-y">
                  {businessUnits.map((bu) => {
                    const regionCount =
                      regions?.filter((r) => r.parent_id === bu.id).length ?? 0;
                    return (
                      <div
                        key={bu.id}
                        className="flex items-center gap-2 px-3 py-2 text-sm"
                      >
                        <span className="font-medium">{bu.name}</span>
                        {bu.description && (
                          <span className="text-muted-foreground text-xs">
                            {bu.description}
                          </span>
                        )}
                        <span className="ml-auto text-[11px] text-muted-foreground/70">
                          {regionCount} region{regionCount === 1 ? "" : "s"}
                        </span>
                        {canManageBU && (
                          <>
                            <Button
                              size="sm"
                              variant="ghost"
                              aria-label="Edit business unit"
                              onClick={() => setBUModal({ mode: "edit", unit: bu })}
                            >
                              <Pencil className="h-3.5 w-3.5" />
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              aria-label="Delete business unit"
                              onClick={() => setDeleteBU(bu)}
                            >
                              <Trash2 className="h-3.5 w-3.5 text-destructive" />
                            </Button>
                          </>
                        )}
                      </div>
                    );
                  })}
                </div>
              ) : (
                <p className="text-xs text-muted-foreground italic">
                  No business units yet. Create one to group regions by
                  organisation, service line, or agency.
                </p>
              )}
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
                                initial: {
                                  id: region.id,
                                  name: region.name,
                                  // parent_id is the region's BU when
                                  // one is assigned (backend maps it in
                                  // the list response). Empty string ⇒
                                  // unassigned.
                                  business_unit_id: region.parent_id || undefined,
                                },
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
                                                      latitude: bld.latitude,
                                                      longitude: bld.longitude,
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


function BusinessUnitForm({
  mode,
  initial,
  onCancel,
  onSaved,
}: {
  mode: "create" | "edit";
  initial?: BusinessUnit;
  onCancel: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError("Name is required.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (mode === "create") {
        await api.createBusinessUnit({
          name: name.trim(),
          description: description.trim() || undefined,
        });
      } else if (initial) {
        await api.updateBusinessUnit(initial.id, {
          name: name.trim(),
          description: description.trim(),
        });
      }
      await onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Name
        </label>
        <input
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          required
        />
      </div>
      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Description (optional)
        </label>
        <textarea
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          rows={2}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="e.g. Courts and tribunals"
        />
      </div>
      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {mode === "create" ? "Create" : "Save"}
        </Button>
      </div>
    </form>
  );
}
