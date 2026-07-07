"use client";

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type {
  AssetRow,
  CollectorSummary,
  CreateDeviceBody,
  DeviceAssetInput,
  DeviceDetail,
  NamedRow,
  Subscription,
  UpdateDeviceBody,
} from "@/lib/types";

const PROTOCOLS = [
  "rest",
  "websocket",
  "telnet",
  "serial",
  "tesira",
  "sony_bravia",
  "poly_videoos",
  "aurora_rxt",
] as const;
const TYPES = ["display", "conferencing", "audio", "camera", "control"] as const;

interface DeviceFormProps {
  mode: "create" | "edit";
  // edit-mode: pre-filled current values (creds excluded from the API).
  initial?: DeviceDetail;
  onCancel: () => void;
  // On success the parent decides what to do (close + refresh, navigate, etc).
  // It receives the device id so the parent can navigate to it on create.
  onSuccess: (deviceId: string) => void;
}

// Internal shape — all strings/numbers so the inputs can bind directly. JSON
// fields are kept as their stringified form in textareas; we parse on submit.
interface FormState {
  collector_id: string;
  reported_id: string;
  name: string;
  type: string;
  protocol: string;
  address: string;
  baud_rate: string;
  username: string;
  password: string;
  poll_rate_seconds: string;
  room_id: string;
  asset_id: string;
  // Physical inventory section — populated from initial.asset on edit,
  // then either PATCHed (asset_id set) or turned into a new asset row
  // (asset_id empty + any field filled) on submit.
  asset_tag: string;
  asset_category: string;
  asset_manufacturer: string;
  asset_model: string;
  asset_serial: string;
  asset_status: string;
  asset_purchase_date: string;
  asset_warranty_end: string;
  asset_notes: string;
  commands_json: string;
  tags_json: string;
  subscriptions_json: string;
}

function emptyForm(): FormState {
  return {
    collector_id: "",
    reported_id: "",
    name: "",
    type: "",
    protocol: "",
    address: "",
    baud_rate: "",
    username: "",
    password: "",
    poll_rate_seconds: "60",
    room_id: "",
    asset_id: "",
    asset_tag: "",
    asset_category: "",
    asset_manufacturer: "",
    asset_model: "",
    asset_serial: "",
    asset_status: "",
    asset_purchase_date: "",
    asset_warranty_end: "",
    asset_notes: "",
    commands_json: "",
    tags_json: "",
    subscriptions_json: "",
  };
}

function formFromDetail(d: DeviceDetail): FormState {
  return {
    collector_id: d.collector_id,
    reported_id: d.reported_id,
    name: d.name ?? "",
    type: d.type ?? "",
    protocol: d.protocol ?? "",
    address: d.address ?? "",
    baud_rate: d.baud_rate ? String(d.baud_rate) : "",
    username: "",
    password: "",
    poll_rate_seconds: d.poll_rate_seconds ? String(d.poll_rate_seconds) : "",
    room_id: d.room_id ?? "",
    asset_id: d.asset_id ?? "",
    asset_tag: d.asset?.asset_tag ?? "",
    asset_category: d.asset?.category ?? "",
    asset_manufacturer: d.asset?.manufacturer ?? "",
    asset_model: d.asset?.model ?? "",
    asset_serial: d.asset?.serial_number ?? "",
    asset_status: d.asset?.status ?? "",
    asset_purchase_date: d.asset?.purchase_date ?? "",
    asset_warranty_end: d.asset?.warranty_end ?? "",
    asset_notes: d.asset?.notes ?? "",
    commands_json: d.commands ? JSON.stringify(d.commands, null, 2) : "",
    tags_json: d.tags ? JSON.stringify(d.tags, null, 2) : "",
    subscriptions_json: d.subscriptions
      ? JSON.stringify(d.subscriptions, null, 2)
      : "",
  };
}

// buildAssetPayload returns a DeviceAssetInput built from the form's
// Physical inventory fields — or undefined when nothing is populated, so
// the backend leaves any existing asset alone. Empty-string fields are
// dropped rather than sent; the backend treats unpopulated fields as
// "don't touch" (patch semantics) on the linked-asset path.
function buildAssetPayload(form: FormState): DeviceAssetInput | undefined {
  const payload: DeviceAssetInput = {};
  if (form.asset_tag) payload.asset_tag = form.asset_tag;
  if (form.asset_category) payload.category = form.asset_category;
  if (form.asset_manufacturer) payload.manufacturer = form.asset_manufacturer;
  if (form.asset_model) payload.model = form.asset_model;
  if (form.asset_serial) payload.serial_number = form.asset_serial;
  if (form.asset_status) payload.status = form.asset_status;
  if (form.asset_purchase_date) payload.purchase_date = form.asset_purchase_date;
  if (form.asset_warranty_end) payload.warranty_end = form.asset_warranty_end;
  if (form.asset_notes) payload.notes = form.asset_notes;
  return Object.keys(payload).length > 0 ? payload : undefined;
}

// Asset category / status labels — kept in the device form so the picker
// stays self-contained. Values match backend allowedAssetCategories /
// allowedAssetStatuses in portalapi/assets.go.
const ASSET_CATEGORIES = [
  "display", "camera", "audio", "conferencing", "control_panel",
  "touch_panel", "cable", "mount", "rack", "remote", "microphone",
  "speaker", "projector", "screen", "computer", "furniture", "storage",
  "other",
] as const;
const ASSET_STATUSES = [
  { key: "in_service", label: "In service" },
  { key: "in_storage", label: "In storage" },
  { key: "in_repair", label: "In repair" },
  { key: "retired", label: "Retired" },
] as const;

// parseJsonField returns the parsed value or throws with a friendly label so
// the form can surface "tags: invalid JSON" rather than a stack trace.
function parseJsonField<T>(label: string, raw: string): T | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  try {
    return JSON.parse(trimmed) as T;
  } catch (e) {
    throw new Error(`${label}: invalid JSON — ${(e as Error).message}`);
  }
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const textareaClass =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-xs font-mono shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function DeviceForm({ mode, initial, onCancel, onSuccess }: DeviceFormProps) {
  const [form, setForm] = useState<FormState>(
    initial ? formFromDetail(initial) : emptyForm()
  );
  const [collectors, setCollectors] = useState<CollectorSummary[]>([]);
  const [rooms, setRooms] = useState<NamedRow[]>([]);
  const [buildings, setBuildings] = useState<NamedRow[]>([]);
  const [assets, setAssets] = useState<AssetRow[]>([]);
  const [loadingLookups, setLoadingLookups] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    Promise.all([
      api.listCollectors(ctrl.signal),
      api.listRooms(ctrl.signal),
      api.listBuildings(ctrl.signal),
      // Only assets not already linked to another device (device_id null)
      // are useful for a fresh link; on edit we still need this asset's
      // own row visible, so include everything and let the dropdown filter
      // the "in use elsewhere" ones itself.
      api.listAssets({}, ctrl.signal).catch(() => [] as AssetRow[]),
    ])
      .then(([cs, rs, bs, as]) => {
        if (ctrl.signal.aborted) return;
        setCollectors(cs);
        setRooms(rs);
        setBuildings(bs);
        setAssets(as);
        // Auto-select the only collector when creating, so the operator
        // doesn't have to pick from a list of one.
        if (mode === "create" && cs.length === 1 && !form.collector_id) {
          setForm((f) => ({ ...f, collector_id: cs[0].id }));
        }
      })
      .catch((e) => {
        if (!ctrl.signal.aborted)
          setError(`Could not load form options: ${(e as Error).message}`);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoadingLookups(false);
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) =>
    setForm((f) => ({ ...f, [k]: v }));

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    let commands: Record<string, string> | undefined;
    let tags: Record<string, string> | undefined;
    let subscriptions: Subscription[] | undefined;
    try {
      commands = parseJsonField<Record<string, string>>("commands", form.commands_json);
      tags = parseJsonField<Record<string, string>>("tags", form.tags_json);
      subscriptions = parseJsonField<Subscription[]>(
        "subscriptions",
        form.subscriptions_json
      );
    } catch (e) {
      setError((e as Error).message);
      return;
    }

    // Build the inline asset sub-object from the Physical inventory
    // section — omitted entirely when no field is populated so the
    // backend leaves any existing asset alone.
    const assetInline = buildAssetPayload(form);

    setSubmitting(true);
    try {
      if (mode === "create") {
        if (!form.collector_id || !form.reported_id) {
          throw new Error("collector and reported_id are required");
        }
        const body: CreateDeviceBody = {
          collector_id: form.collector_id,
          reported_id: form.reported_id,
          name: form.name || undefined,
          type: form.type || undefined,
          protocol: form.protocol || undefined,
          address: form.address || undefined,
          baud_rate: form.baud_rate ? Number(form.baud_rate) : undefined,
          username: form.username || undefined,
          password: form.password || undefined,
          poll_rate_seconds: form.poll_rate_seconds
            ? Number(form.poll_rate_seconds)
            : undefined,
          room_id: form.room_id || undefined,
          asset_id: form.asset_id || undefined,
          asset: assetInline,
          commands,
          tags,
          subscriptions,
        };
        const { id } = await api.createDevice(body);
        onSuccess(id);
      } else {
        // Edit: send everything; the cloud's PATCH treats supplied fields as
        // the new value (empty string clears). Username/password only sent
        // when the operator typed something — otherwise the existing values
        // stay put.
        const body: UpdateDeviceBody = {
          name: form.name,
          type: form.type || "",
          protocol: form.protocol || "",
          address: form.address,
          baud_rate: form.baud_rate ? Number(form.baud_rate) : 0,
          poll_rate_seconds: form.poll_rate_seconds
            ? Number(form.poll_rate_seconds)
            : 0,
          room_id: form.room_id,
          asset_id: form.asset_id,
          asset: assetInline,
          commands: commands ?? {},
          tags: tags ?? {},
          subscriptions: subscriptions ?? [],
        };
        if (form.username) body.username = form.username;
        if (form.password) body.password = form.password;
        if (!initial) throw new Error("missing initial device for edit");
        await api.updateDevice(initial.id, body);
        onSuccess(initial.id);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  const isEdit = mode === "edit";

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <label className={labelClass}>Collector</label>
          <select
            className={inputClass}
            value={form.collector_id}
            onChange={(e) => set("collector_id", e.target.value)}
            disabled={isEdit || loadingLookups}
            required
          >
            <option value="">— Select —</option>
            {collectors.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name} ({c.bridge_collector_id})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className={labelClass}>Reported ID</label>
          <input
            className={inputClass}
            value={form.reported_id}
            onChange={(e) => set("reported_id", e.target.value)}
            placeholder="e.g. tesira-boardroom-01"
            disabled={isEdit}
            required
          />
        </div>
        <div>
          <label className={labelClass}>Name</label>
          <input
            className={inputClass}
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="Friendly name"
          />
        </div>
        <div>
          <label className={labelClass}>Room</label>
          <select
            className={inputClass}
            value={form.room_id}
            onChange={(e) => set("room_id", e.target.value)}
            disabled={loadingLookups}
          >
            <option value="">— Unassigned —</option>
            {rooms.map((r) => {
              const building = buildings.find((b) => b.id === r.parent_id);
              const label = building ? `${building.name} / ${r.name}` : r.name;
              return (
                <option key={r.id} value={r.id}>
                  {label}
                </option>
              );
            })}
          </select>
        </div>
        <div>
          <label className={labelClass}>Linked asset</label>
          <select
            className={inputClass}
            value={form.asset_id}
            onChange={(e) => set("asset_id", e.target.value)}
            disabled={loadingLookups}
          >
            <option value="">— No CMDB link —</option>
            {assets
              // Hide assets already linked to a different device; the current
              // device's own linked asset is always shown so the operator can
              // see + change it. Backend enforces one-device-per-asset via
              // FK semantics; this just prevents the awkward silent swap.
              .filter(
                (a) => !a.device_id || a.device_id === initial?.id
              )
              .map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                  {a.asset_tag ? ` (#${a.asset_tag})` : ""}
                  {a.model ? ` — ${a.model}` : ""}
                </option>
              ))}
          </select>
        </div>
        <div>
          <label className={labelClass}>Type</label>
          <select
            className={inputClass}
            value={form.type}
            onChange={(e) => set("type", e.target.value)}
          >
            <option value="">— Select —</option>
            {TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className={labelClass}>Protocol</label>
          <select
            className={inputClass}
            value={form.protocol}
            onChange={(e) => set("protocol", e.target.value)}
          >
            <option value="">— Select —</option>
            {PROTOCOLS.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>
        <div className="sm:col-span-2">
          <label className={labelClass}>Address</label>
          <input
            className={inputClass}
            value={form.address}
            onChange={(e) => set("address", e.target.value)}
            placeholder="IP, hostname, or COM port"
          />
        </div>
        <div>
          <label className={labelClass}>Poll rate (seconds)</label>
          <input
            type="number"
            className={inputClass}
            value={form.poll_rate_seconds}
            onChange={(e) => set("poll_rate_seconds", e.target.value)}
            min={1}
          />
        </div>
        <div>
          <label className={labelClass}>Baud rate (serial)</label>
          <input
            type="number"
            className={inputClass}
            value={form.baud_rate}
            onChange={(e) => set("baud_rate", e.target.value)}
            placeholder="9600"
          />
        </div>
        <div>
          <label className={labelClass}>Username</label>
          <input
            className={inputClass}
            value={form.username}
            onChange={(e) => set("username", e.target.value)}
            autoComplete="off"
            placeholder={isEdit ? "(leave blank to keep)" : ""}
          />
        </div>
        <div>
          <label className={labelClass}>Password</label>
          <input
            type="password"
            className={inputClass}
            value={form.password}
            onChange={(e) => set("password", e.target.value)}
            autoComplete="new-password"
            placeholder={isEdit ? "(leave blank to keep)" : ""}
          />
        </div>
      </div>

      <details
        className="rounded-md border bg-muted/30 p-3"
        open={
          // Auto-open the section when there's existing CMDB data so the
          // operator can see it at a glance during edit. Stays collapsed
          // for pristine create-mode forms so the section doesn't dominate
          // the modal for people just adding a device.
          !!form.asset_tag ||
          !!form.asset_manufacturer ||
          !!form.asset_model ||
          !!form.asset_serial ||
          !!form.asset_purchase_date ||
          !!form.asset_warranty_end ||
          !!form.asset_notes
        }
      >
        <summary className="cursor-pointer text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Physical inventory (CMDB)
        </summary>
        <p className="mt-2 text-[11px] text-muted-foreground">
          Optional. Fill any of these to catalogue this device in the asset
          register (warranty, serial, purchase history). {form.asset_id
            ? "The linked asset row will be updated."
            : "A new asset row will be created and linked."}
        </p>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <div>
            <label className={labelClass}>Asset tag</label>
            <input
              className={inputClass}
              value={form.asset_tag}
              onChange={(e) => set("asset_tag", e.target.value)}
              placeholder="AV-042"
            />
          </div>
          <div>
            <label className={labelClass}>Category</label>
            <select
              className={inputClass}
              value={form.asset_category}
              onChange={(e) => set("asset_category", e.target.value)}
            >
              <option value="">— Auto from device type —</option>
              {ASSET_CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {c.replace("_", " ")}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className={labelClass}>Manufacturer</label>
            <input
              className={inputClass}
              value={form.asset_manufacturer}
              onChange={(e) => set("asset_manufacturer", e.target.value)}
              placeholder="Sony"
            />
          </div>
          <div>
            <label className={labelClass}>Model</label>
            <input
              className={inputClass}
              value={form.asset_model}
              onChange={(e) => set("asset_model", e.target.value)}
              placeholder="FW-65BZ40H"
            />
          </div>
          <div>
            <label className={labelClass}>Serial number</label>
            <input
              className={inputClass}
              value={form.asset_serial}
              onChange={(e) => set("asset_serial", e.target.value)}
            />
          </div>
          <div>
            <label className={labelClass}>Status</label>
            <select
              className={inputClass}
              value={form.asset_status}
              onChange={(e) => set("asset_status", e.target.value)}
            >
              <option value="">— Default (in service) —</option>
              {ASSET_STATUSES.map((s) => (
                <option key={s.key} value={s.key}>
                  {s.label}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className={labelClass}>Purchase date</label>
            <input
              type="date"
              className={inputClass}
              value={form.asset_purchase_date}
              onChange={(e) => set("asset_purchase_date", e.target.value)}
            />
          </div>
          <div>
            <label className={labelClass}>Warranty end</label>
            <input
              type="date"
              className={inputClass}
              value={form.asset_warranty_end}
              onChange={(e) => set("asset_warranty_end", e.target.value)}
            />
          </div>
          <div className="sm:col-span-2">
            <label className={labelClass}>Notes</label>
            <textarea
              className={textareaClass}
              rows={2}
              value={form.asset_notes}
              onChange={(e) => set("asset_notes", e.target.value)}
              placeholder="Purchased under PO#1234 — spare bulb in AV cupboard."
            />
          </div>
        </div>
      </details>

      <details className="rounded-md border bg-muted/30 p-3">
        <summary className="cursor-pointer text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Advanced (JSON)
        </summary>
        <div className="mt-3 space-y-3">
          <div>
            <label className={labelClass}>Commands (object)</label>
            <textarea
              rows={4}
              className={textareaClass}
              value={form.commands_json}
              onChange={(e) => set("commands_json", e.target.value)}
              placeholder='{"mute": "...", "unmute": "..."}'
            />
          </div>
          <div>
            <label className={labelClass}>Tags (object)</label>
            <textarea
              rows={3}
              className={textareaClass}
              value={form.tags_json}
              onChange={(e) => set("tags_json", e.target.value)}
              placeholder='{"make": "Sony", "model": "..."}'
            />
          </div>
          <div>
            <label className={labelClass}>Subscriptions (array)</label>
            <textarea
              rows={4}
              className={textareaClass}
              value={form.subscriptions_json}
              onChange={(e) => set("subscriptions_json", e.target.value)}
              placeholder='[{"tag":"master_level","attribute":"level","channel":1,"label":"db"}]'
            />
          </div>
        </div>
      </details>

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting || loadingLookups}>
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {mode === "create" ? "Create device" : "Save changes"}
        </Button>
      </div>
    </form>
  );
}
