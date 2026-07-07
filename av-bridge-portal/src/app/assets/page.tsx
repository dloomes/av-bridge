"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import {
  Boxes,
  Download,
  ExternalLink,
  Loader2,
  Pencil,
  Plus,
  Radio,
  RefreshCcw,
  Search,
  Trash2,
  Upload,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { DeviceForm } from "@/components/device-form";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import type {
  AssetCategory,
  AssetRow,
  AssetStatus,
  BuildingRow,
  CreateAssetBody,
  NamedRow,
  UpdateAssetBody,
} from "@/lib/types";

// Category / status labels mirror the backend allowlist. Labels are picked
// to be readable in a filter dropdown and a badge without extra styling.
const CATEGORIES: { key: AssetCategory; label: string }[] = [
  { key: "display", label: "Display" },
  { key: "camera", label: "Camera" },
  { key: "audio", label: "Audio" },
  { key: "conferencing", label: "Conferencing" },
  { key: "control_panel", label: "Control panel" },
  { key: "touch_panel", label: "Touch panel" },
  { key: "cable", label: "Cable" },
  { key: "mount", label: "Mount" },
  { key: "rack", label: "Rack" },
  { key: "remote", label: "Remote" },
  { key: "microphone", label: "Microphone" },
  { key: "speaker", label: "Speaker" },
  { key: "projector", label: "Projector" },
  { key: "screen", label: "Screen" },
  { key: "computer", label: "Computer" },
  { key: "furniture", label: "Furniture" },
  { key: "storage", label: "Storage" },
  { key: "other", label: "Other" },
];

const STATUSES: { key: AssetStatus; label: string }[] = [
  { key: "in_service", label: "In service" },
  { key: "in_storage", label: "In storage" },
  { key: "in_repair", label: "In repair" },
  { key: "retired", label: "Retired" },
];

// Variant for the status badge — matches the Badge variants palette.
// in_storage uses secondary because there's no "neutral" variant and
// "outline" reads as placeholder-y in the table.
function statusVariant(
  s: AssetStatus
): "success" | "warning" | "secondary" | "destructive" {
  switch (s) {
    case "in_service":
      return "success";
    case "in_repair":
      return "warning";
    case "in_storage":
      return "secondary";
    case "retired":
      return "destructive";
  }
}

function statusLabel(s: AssetStatus): string {
  return STATUSES.find((x) => x.key === s)?.label ?? s;
}

function categoryLabel(c: AssetCategory): string {
  return CATEGORIES.find((x) => x.key === c)?.label ?? c;
}

export default function AssetsPage() {
  const session = useSession();
  const admin = isAdmin(session.user?.role) || !!session.user?.is_vendor;

  const [assets, setAssets] = useState<AssetRow[] | null>(null);
  const [buildings, setBuildings] = useState<BuildingRow[] | null>(null);
  const [rooms, setRooms] = useState<NamedRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  // Filters — kept as controlled strings so an empty value means "not
  // filtered". Search is debounced via a timer below.
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [categoryFilter, setCategoryFilter] = useState<string>("");
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [buildingFilter, setBuildingFilter] = useState<string>("");

  const [editing, setEditing] = useState<{ mode: "create" | "edit"; existing?: AssetRow } | null>(null);
  const [deleting, setDeleting] = useState<AssetRow | null>(null);
  // monitoring: the asset the operator wants to bind to a fresh device
  // via the "Set up monitoring" action. Non-null means the DeviceForm
  // modal is open in create-mode with the asset pre-linked.
  const [monitoring, setMonitoring] = useState<AssetRow | null>(null);
  const [importing, setImporting] = useState(false);
  // The result of the last import attempt — held so we can render the
  // summary + row-level errors in a modal until the operator dismisses.
  const [importResult, setImportResult] = useState<{
    processed: number;
    created: number;
    updated: number;
    errors: { row: number; asset_tag?: string; message: string }[];
  } | null>(null);
  const importInputRef = useRef<HTMLInputElement>(null);

  // Debounce the free-text search so we don't fire on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 250);
    return () => clearTimeout(t);
  }, [search]);

  const loadAssets = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const list = await api.listAssets(
          {
            q: debouncedSearch || undefined,
            category: categoryFilter || undefined,
            status: statusFilter || undefined,
            building_id: buildingFilter || undefined,
          },
          signal
        );
        if (signal?.aborted) return;
        setAssets(list);
        setLoadError(null);
      } catch (e) {
        if (!signal?.aborted) setLoadError((e as Error).message);
      }
    },
    [debouncedSearch, categoryFilter, statusFilter, buildingFilter]
  );

  // Reload assets when filters change. Buildings + rooms load once — they
  // don't change often and the form picker needs them regardless.
  useEffect(() => {
    const ctrl = new AbortController();
    void loadAssets(ctrl.signal);
    return () => ctrl.abort();
  }, [loadAssets]);

  useEffect(() => {
    const ctrl = new AbortController();
    Promise.all([api.listBuildings(ctrl.signal), api.listRooms(ctrl.signal)])
      .then(([bs, rs]) => {
        if (ctrl.signal.aborted) return;
        setBuildings(bs);
        setRooms(rs);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setLoadError((e as Error).message);
      });
    return () => ctrl.abort();
  }, []);

  const refresh = async () => {
    setRefreshing(true);
    try {
      await loadAssets();
    } finally {
      setRefreshing(false);
    }
  };

  const handleDelete = async () => {
    if (!deleting) return;
    try {
      await api.deleteAsset(deleting.id);
      setDeleting(null);
      await loadAssets();
    } catch (e) {
      alert((e as Error).message);
    }
  };

  // Export triggers a client-side download of the CSV blob. Filename
  // matches the backend's Content-Disposition so opening in Excel later
  // is unambiguous (assets-YYYY-MM-DD.csv).
  const handleExport = async () => {
    try {
      const csv = await api.exportAssetsCSV();
      const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `assets-${new Date().toISOString().slice(0, 10)}.csv`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (e) {
      alert(`Export failed: ${(e as Error).message}`);
    }
  };

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    // Reset the input so re-choosing the same file works — file inputs
    // don't fire change if the value doesn't change.
    e.target.value = "";
    if (!file) return;
    setImporting(true);
    try {
      const result = await api.importAssets(file);
      setImportResult(result);
      // Refresh the list even on validation errors — nothing was written
      // but the operator will want a clean list after dismissing the modal.
      await loadAssets();
    } catch (err) {
      alert(`Import failed: ${(err as Error).message}`);
    } finally {
      setImporting(false);
    }
  };

  const anyFilterActive =
    debouncedSearch !== "" ||
    categoryFilter !== "" ||
    statusFilter !== "" ||
    buildingFilter !== "";

  // Room name lookup for the edit form — the API returns room by name for
  // the list, but the form picker needs id→name pairs.
  const roomsById = useMemo(() => {
    const m = new Map<string, NamedRow>();
    for (const r of rooms ?? []) m.set(r.id, r);
    return m;
  }, [rooms]);

  return (
    <div className="min-h-screen">
      <div className="max-w-6xl mx-auto p-6 space-y-6">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Assets</h1>
            <p className="text-sm text-muted-foreground">
              Physical inventory for this tenant — monitored devices and
              everything else you want to track.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={refresh}
              disabled={refreshing}
            >
              <RefreshCcw
                className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`}
              />
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={handleExport}>
              <Download className="h-3.5 w-3.5" />
              Export CSV
            </Button>
            {admin && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => importInputRef.current?.click()}
                  disabled={importing}
                >
                  {importing ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Upload className="h-3.5 w-3.5" />
                  )}
                  Import CSV
                </Button>
                <input
                  ref={importInputRef}
                  type="file"
                  accept=".csv,text/csv"
                  className="hidden"
                  onChange={handleImportFile}
                />
                <Button size="sm" onClick={() => setEditing({ mode: "create" })}>
                  <Plus className="h-4 w-4" />
                  New asset
                </Button>
              </>
            )}
            <UserMenu />
          </div>
        </header>

        {loadError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {loadError}
          </div>
        )}

        <Card>
          <CardContent className="p-4 flex flex-wrap items-center gap-3">
            <div className="relative flex-1 min-w-[200px]">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search name, model, serial, tag…"
                className="w-full rounded-md border bg-background pl-9 pr-3 py-2 text-sm"
              />
            </div>
            <select
              value={categoryFilter}
              onChange={(e) => setCategoryFilter(e.target.value)}
              className="rounded-md border bg-background px-3 py-2 text-sm"
              aria-label="Filter by category"
            >
              <option value="">All categories</option>
              {CATEGORIES.map((c) => (
                <option key={c.key} value={c.key}>
                  {c.label}
                </option>
              ))}
            </select>
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
              className="rounded-md border bg-background px-3 py-2 text-sm"
              aria-label="Filter by status"
            >
              <option value="">All statuses</option>
              {STATUSES.map((s) => (
                <option key={s.key} value={s.key}>
                  {s.label}
                </option>
              ))}
            </select>
            <select
              value={buildingFilter}
              onChange={(e) => setBuildingFilter(e.target.value)}
              className="rounded-md border bg-background px-3 py-2 text-sm"
              aria-label="Filter by building"
            >
              <option value="">All buildings</option>
              {(buildings ?? []).map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                </option>
              ))}
            </select>
            {anyFilterActive && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setSearch("");
                  setCategoryFilter("");
                  setStatusFilter("");
                  setBuildingFilter("");
                }}
              >
                Clear
              </Button>
            )}
          </CardContent>
        </Card>

        {assets === null ? (
          <div className="space-y-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : assets.length === 0 ? (
          <Card>
            <CardContent className="p-10 text-center space-y-2">
              <Boxes className="h-8 w-8 text-muted-foreground/50 mx-auto" />
              <div className="text-sm font-medium">
                {anyFilterActive ? "No assets match those filters" : "No assets yet"}
              </div>
              <p className="text-xs text-muted-foreground max-w-md mx-auto">
                {anyFilterActive
                  ? "Try clearing filters or search terms."
                  : admin
                    ? "Assets track everything the tenant owns — monitored gear plus mounts, cables, remotes, and anything else worth cataloguing."
                    : "Nothing has been added yet."}
              </p>
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b bg-muted/30 text-left text-xs uppercase tracking-wide text-muted-foreground">
                    <th className="px-4 py-2.5 font-medium">Asset</th>
                    <th className="px-4 py-2.5 font-medium">Category</th>
                    <th className="px-4 py-2.5 font-medium">Location</th>
                    <th className="px-4 py-2.5 font-medium">Status</th>
                    <th className="px-4 py-2.5 font-medium">Monitored</th>
                    <th className="px-4 py-2.5 font-medium sr-only">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {assets.map((a) => (
                    <tr
                      key={a.id}
                      className="border-b last:border-0 hover:bg-muted/20"
                    >
                      <td className="px-4 py-3">
                        <div className="font-medium">{a.name}</div>
                        <div className="text-xs text-muted-foreground flex flex-wrap gap-x-2">
                          {a.asset_tag && <span>#{a.asset_tag}</span>}
                          {a.manufacturer && <span>{a.manufacturer}</span>}
                          {a.model && <span>{a.model}</span>}
                          {a.serial_number && (
                            <span className="font-mono">sn:{a.serial_number}</span>
                          )}
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <span className="text-xs">{categoryLabel(a.category)}</span>
                      </td>
                      <td className="px-4 py-3">
                        {a.room ? (
                          <>
                            <div className="text-xs font-medium">{a.room}</div>
                            <div className="text-[11px] text-muted-foreground">
                              {[a.region, a.location, a.building].filter(Boolean).join(" · ")}
                            </div>
                          </>
                        ) : (
                          <span className="text-xs text-muted-foreground">Unplaced</span>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <Badge variant={statusVariant(a.status)}>
                          {statusLabel(a.status)}
                        </Badge>
                      </td>
                      <td className="px-4 py-3">
                        {a.device_id ? (
                          <Link
                            href={`/devices/${encodeURIComponent(a.device_id)}`}
                            className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
                          >
                            Yes
                            <ExternalLink className="h-3 w-3" />
                          </Link>
                        ) : admin ? (
                          <button
                            type="button"
                            onClick={() => setMonitoring(a)}
                            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-primary"
                          >
                            <Radio className="h-3 w-3" />
                            Set up
                          </button>
                        ) : (
                          <span className="text-xs text-muted-foreground">No</span>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex justify-end gap-1">
                          {admin && (
                            <>
                              <Button
                                variant="ghost"
                                size="icon"
                                aria-label="Edit"
                                onClick={() => setEditing({ mode: "edit", existing: a })}
                              >
                                <Pencil className="h-3.5 w-3.5" />
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon"
                                aria-label="Delete"
                                onClick={() => setDeleting(a)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </CardContent>
          </Card>
        )}

        {editing && (
          <Modal
            open
            onClose={() => setEditing(null)}
            title={editing.mode === "create" ? "New asset" : "Edit asset"}
          >
            <AssetForm
              mode={editing.mode}
              existing={editing.existing}
              rooms={rooms ?? []}
              roomsById={roomsById}
              onCancel={() => setEditing(null)}
              onSuccess={() => {
                setEditing(null);
                void loadAssets();
              }}
            />
          </Modal>
        )}

        {importResult && (
          <Modal
            open
            onClose={() => setImportResult(null)}
            title={
              importResult.errors.length > 0
                ? "Import needs attention"
                : "Import complete"
            }
          >
            <div className="space-y-3 text-sm">
              <div className="grid grid-cols-3 gap-3">
                <div className="rounded-md border p-3">
                  <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    Processed
                  </div>
                  <div className="text-2xl font-semibold">
                    {importResult.processed}
                  </div>
                </div>
                <div className="rounded-md border p-3">
                  <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    Created
                  </div>
                  <div className="text-2xl font-semibold text-[color:hsl(var(--success))]">
                    {importResult.created}
                  </div>
                </div>
                <div className="rounded-md border p-3">
                  <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    Updated
                  </div>
                  <div className="text-2xl font-semibold text-[color:hsl(var(--primary))]">
                    {importResult.updated}
                  </div>
                </div>
              </div>

              {importResult.errors.length > 0 && (
                <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 space-y-2">
                  <div className="text-sm font-medium [color:hsl(var(--destructive))]">
                    {importResult.errors.length} row
                    {importResult.errors.length === 1 ? "" : "s"} rejected —
                    nothing was written.
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Fix the rows below in your CSV and re-import. The upload is
                    all-or-nothing so a partial import can never leave your data
                    in a half-updated state.
                  </p>
                  <div className="max-h-64 overflow-y-auto rounded border bg-background">
                    <table className="w-full text-xs">
                      <thead className="sticky top-0 bg-muted/40">
                        <tr>
                          <th className="px-2 py-1.5 text-left font-medium">Row</th>
                          <th className="px-2 py-1.5 text-left font-medium">Tag</th>
                          <th className="px-2 py-1.5 text-left font-medium">
                            Problem
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {importResult.errors.map((e, i) => (
                          <tr key={i} className="border-t">
                            <td className="px-2 py-1 font-mono">{e.row}</td>
                            <td className="px-2 py-1 font-mono">
                              {e.asset_tag || "—"}
                            </td>
                            <td className="px-2 py-1">{e.message}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>
              )}

              <div className="flex justify-end gap-2 pt-2 border-t">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setImportResult(null)}
                >
                  Close
                </Button>
              </div>
            </div>
          </Modal>
        )}

        {monitoring && (
          <Modal
            open
            onClose={() => setMonitoring(null)}
            title={`Set up monitoring — ${monitoring.name}`}
          >
            <p className="mb-3 text-xs text-muted-foreground">
              Creates a monitored device bound to this asset. The bridge
              will start polling as soon as it picks up the config. Fields
              are pre-filled from the asset — tweak protocol, address,
              credentials as needed.
            </p>
            <DeviceForm
              mode="create"
              assetToLink={monitoring}
              onCancel={() => setMonitoring(null)}
              onSuccess={() => {
                setMonitoring(null);
                void loadAssets();
              }}
            />
          </Modal>
        )}

        {deleting && (
          <Modal
            open
            onClose={() => setDeleting(null)}
            title={`Delete ${deleting.name}?`}
            wide={false}
          >
            <p className="text-sm text-muted-foreground">
              This removes the asset from the CMDB. If it's currently linked to
              a monitored device, the device stays online — it just loses the
              asset link.
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={() => setDeleting(null)}>
                Cancel
              </Button>
              <Button variant="destructive" size="sm" onClick={handleDelete}>
                Delete
              </Button>
            </div>
          </Modal>
        )}
      </div>
    </div>
  );
}

// ------ form ---------------------------------------------------------------

interface AssetFormProps {
  mode: "create" | "edit";
  existing?: AssetRow;
  rooms: NamedRow[];
  roomsById: Map<string, NamedRow>;
  onCancel: () => void;
  onSuccess: () => void;
}

// AssetForm handles both create + edit — the shape of the two requests is
// nearly identical and the wire types differ only in which fields are
// required. Keeping them together avoids duplicating the field layout.
function AssetForm({ mode, existing, rooms, onCancel, onSuccess }: AssetFormProps) {
  const [name, setName] = useState(existing?.name ?? "");
  const [category, setCategory] = useState<AssetCategory>(
    existing?.category ?? "display"
  );
  const [status, setStatus] = useState<AssetStatus>(existing?.status ?? "in_service");
  const [roomID, setRoomID] = useState(existing?.room_id ?? "");
  const [assetTag, setAssetTag] = useState(existing?.asset_tag ?? "");
  const [manufacturer, setManufacturer] = useState(existing?.manufacturer ?? "");
  const [model, setModel] = useState(existing?.model ?? "");
  const [serial, setSerial] = useState(existing?.serial_number ?? "");
  const [purchaseDate, setPurchaseDate] = useState(existing?.purchase_date ?? "");
  const [warrantyEnd, setWarrantyEnd] = useState(existing?.warranty_end ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSave(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!name.trim()) {
      setError("Name is required.");
      return;
    }
    setSaving(true);
    try {
      if (mode === "create") {
        const body: CreateAssetBody = {
          name: name.trim(),
          category,
          status,
          asset_tag: assetTag.trim() || undefined,
          manufacturer: manufacturer.trim() || undefined,
          model: model.trim() || undefined,
          serial_number: serial.trim() || undefined,
          room_id: roomID || undefined,
          purchase_date: purchaseDate || undefined,
          warranty_end: warrantyEnd || undefined,
          notes: notes.trim() || undefined,
        };
        await api.createAsset(body);
      } else if (existing) {
        // On edit, send the whole set — send empty strings for cleared
        // fields so the backend clears them (PATCH semantics distinguish
        // "" from omitted).
        const body: UpdateAssetBody = {
          name: name.trim(),
          category,
          status,
          asset_tag: assetTag.trim(),
          manufacturer: manufacturer.trim(),
          model: model.trim(),
          serial_number: serial.trim(),
          room_id: roomID,
          purchase_date: purchaseDate,
          warranty_end: warrantyEnd,
          notes: notes.trim(),
        };
        await api.updateAsset(existing.id, body);
      }
      onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <form onSubmit={handleSave} className="space-y-4">
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <label className="space-y-1 sm:col-span-2">
          <span className="text-xs font-medium">Name *</span>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Category *</span>
          <select
            value={category}
            onChange={(e) => setCategory(e.target.value as AssetCategory)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          >
            {CATEGORIES.map((c) => (
              <option key={c.key} value={c.key}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Status</span>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value as AssetStatus)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          >
            {STATUSES.map((s) => (
              <option key={s.key} value={s.key}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
        <label className="space-y-1 sm:col-span-2">
          <span className="text-xs font-medium">Room</span>
          <select
            value={roomID}
            onChange={(e) => setRoomID(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          >
            <option value="">— unplaced —</option>
            {rooms.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Asset tag</span>
          <input
            type="text"
            value={assetTag}
            onChange={(e) => setAssetTag(e.target.value)}
            placeholder="AV-042"
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Serial number</span>
          <input
            type="text"
            value={serial}
            onChange={(e) => setSerial(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm font-mono"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Manufacturer</span>
          <input
            type="text"
            value={manufacturer}
            onChange={(e) => setManufacturer(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Model</span>
          <input
            type="text"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Purchase date</span>
          <input
            type="date"
            value={purchaseDate}
            onChange={(e) => setPurchaseDate(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1">
          <span className="text-xs font-medium">Warranty end</span>
          <input
            type="date"
            value={warrantyEnd}
            onChange={(e) => setWarrantyEnd(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
        <label className="space-y-1 sm:col-span-2">
          <span className="text-xs font-medium">Notes</span>
          <textarea
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            rows={2}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm"
          />
        </label>
      </div>

      {error && (
        <div className="text-sm [color:hsl(var(--destructive))]">{error}</div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="outline" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={saving}>
          {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {mode === "create" ? "Create asset" : "Save changes"}
        </Button>
      </div>
    </form>
  );
}
