"use client";

import { useEffect, useMemo, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { apiMessage } from "@/lib/api-error";
import type { CloudSource, DeviceDetail, Integration, LensDevice, NamedRow, NeatDevice } from "@/lib/types";
import { CLOUD_SOURCE_LABELS, DEVICE_TYPE_LABELS } from "@/lib/types";
import { cn } from "@/lib/utils";

const labelClass = "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

type Platform = "windows" | "android";
export type RoomSystemType = "teams_room" | "zoom_room";

interface Props {
  mode: "create" | "edit";
  // Which kind of room is being added (create). Edit takes it from initial.
  roomType?: RoomSystemType;
  // edit-mode: the vendor-cloud device being renamed / moved.
  initial?: DeviceDetail;
  // The Type select, rendered by the parent form so it stays where it was.
  typeSelect?: React.ReactNode;
  rooms: NamedRow[];
  buildings: NamedRow[];
  loadingLookups: boolean;
  onCancel: () => void;
  onSuccess: (deviceId: string) => void;
  // Chosen "Direct (Windows remote management)": the parent shows the
  // Collector device form for protocol mtr_windows instead.
  onDirect?: () => void;
}

function roomLabel(r: NamedRow, buildings: NamedRow[]): string {
  const b = buildings.find((x) => x.id === r.parent_id);
  return b ? `${b.name} / ${r.name}` : r.name;
}

// One row of either vendor's picker, flattened for the list.
interface PickRow {
  external_id: string;
  name: string;
  connected: boolean;
  detail: string;
  hover?: string;
  linked: boolean;
  monitoredAs?: string;
  roomName: string;
  note?: string;
}

const PRIMARY_MODE_NAME: Record<RoomSystemType, string> = { teams_room: "Teams", zoom_room: "Zoom" };

// TeamsRoomForm is the device form for a room system (a Microsoft Teams Room
// or a Zoom Room) read from a vendor cloud: Poly Lens (Teams Rooms) or Neat
// Pulse (Teams and Zoom Rooms on Neat devices). No Collector, protocol,
// address or credentials. Create picks the device from the connected
// account; edit only changes the name and the room (everything else comes
// from the vendor cloud).
export function TeamsRoomForm({
  mode,
  roomType = "teams_room",
  initial,
  typeSelect,
  rooms,
  buildings,
  loadingLookups,
  onCancel,
  onSuccess,
  onDirect,
}: Props) {
  const isEdit = mode === "edit";
  const isZoom = roomType === "zoom_room";
  const [platform, setPlatform] = useState<Platform>(initial?.platform ?? "windows");
  const [name, setName] = useState(initial?.name ?? "");
  const [roomId, setRoomId] = useState(initial?.room_id ?? "");
  const [roomTouched, setRoomTouched] = useState(false);
  const [selected, setSelected] = useState<PickRow | null>(null);
  const [manager, setManager] = useState<CloudSource | null>(null);

  const [integrations, setIntegrations] = useState<Integration[] | null>(null);
  const [search, setSearch] = useState("");
  const [lensDevices, setLensDevices] = useState<LensDevice[] | null>(null);
  const [neatDevices, setNeatDevices] = useState<NeatDevice[] | null>(null);
  const [degraded, setDegraded] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [integrationsError, setIntegrationsError] = useState<string | null>(null);
  const [listLoading, setListLoading] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const connected = useMemo(() => {
    const ok = (p: CloudSource) => {
      const i = integrations?.find((x) => x.provider === p);
      return i?.status === "connected" || i?.status === "error";
    };
    return { poly_lens: ok("poly_lens"), neat_pulse: ok("neat_pulse") };
  }, [integrations]);

  // Poly Lens reads Teams Rooms only; Neat devices run Android.
  const lensAvailable = !isZoom;
  const neatAvailable = isZoom || platform === "android";

  // Pick a sensible manager once the integrations are known, and drop one
  // that no longer applies (Zoom chosen, or the platform changed).
  useEffect(() => {
    if (isEdit || integrations === null) return;
    setManager((cur) => {
      if (cur === "poly_lens" && lensAvailable && connected.poly_lens) return cur;
      if (cur === "neat_pulse" && neatAvailable && connected.neat_pulse) return cur;
      if (lensAvailable && connected.poly_lens) return "poly_lens";
      if (neatAvailable && connected.neat_pulse) return "neat_pulse";
      return null;
    });
  }, [isEdit, integrations, connected, lensAvailable, neatAvailable]);

  useEffect(() => {
    setSelected(null);
  }, [manager, roomType]);

  useEffect(() => {
    if (isEdit) return;
    const ctrl = new AbortController();
    api
      .listIntegrations(ctrl.signal)
      .then((l) => {
        if (!ctrl.signal.aborted) setIntegrations(l);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) {
          setIntegrations([]);
          setIntegrationsError(apiMessage(e));
        }
      });
    return () => ctrl.abort();
  }, [isEdit]);

  // Search the vendor as the person types (server-side q), after a short pause.
  useEffect(() => {
    if (isEdit || !manager) return;
    const ctrl = new AbortController();
    const t = setTimeout(() => {
      setListLoading(true);
      const q = search.trim() || undefined;
      const done = () => {
        if (!ctrl.signal.aborted) setListLoading(false);
      };
      const fail = (e: unknown) => {
        if (!ctrl.signal.aborted) setListError(apiMessage(e));
      };
      if (manager === "poly_lens") {
        api
          .listLensDevices(q, ctrl.signal)
          .then((r) => {
            if (ctrl.signal.aborted) return;
            setLensDevices(r.devices ?? []);
            setDegraded(!!r.degraded);
            setListError(null);
          })
          .catch(fail)
          .finally(done);
      } else {
        api
          .listNeatDevices(q, ctrl.signal)
          .then((r) => {
            if (ctrl.signal.aborted) return;
            setNeatDevices(r.devices ?? []);
            setListError(null);
          })
          .catch(fail)
          .finally(done);
      }
    }, search ? 300 : 0);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  }, [isEdit, manager, search]);

  const vendorName = manager ? CLOUD_SOURCE_LABELS[manager] : "";
  const rows: PickRow[] | null = useMemo(() => {
    if (manager === "poly_lens") {
      return (
        lensDevices?.map((d) => ({
          external_id: d.external_id,
          name: d.name,
          connected: d.connected,
          detail: [d.model, d.room_name && `Lens room: ${d.room_name}`, d.site_name].filter(Boolean).join(" · "),
          hover: [d.serial && `Serial ${d.serial}`, d.mac && `MAC ${d.mac}`].filter(Boolean).join(" · ") || undefined,
          linked: !!d.linked_device_id,
          monitoredAs: d.already_monitored_as,
          roomName: d.room_name,
        })) ?? null
      );
    }
    if (manager === "neat_pulse") {
      // A device in the other mode belongs under the other type.
      return (
        neatDevices
          ?.filter((d) => !d.suggested_type || d.suggested_type === roomType)
          .map((d) => ({
            external_id: d.external_id,
            name: d.name,
            connected: d.connected,
            detail: [d.model, d.room_name && `Pulse room: ${d.room_name}`, d.location_name].filter(Boolean).join(" · "),
            hover: d.serial ? `Serial ${d.serial}` : undefined,
            linked: !!d.linked_device_id,
            monitoredAs: d.already_monitored_as,
            roomName: d.room_name,
            note: d.suggested_type
              ? `Primary mode: ${d.primary_mode}`
              : `Pulse doesn't give its mode: it will be added as a ${DEVICE_TYPE_LABELS[roomType]}`,
          })) ?? null
      );
    }
    return null;
  }, [manager, lensDevices, neatDevices, roomType]);

  function pick(d: PickRow) {
    setSelected(d);
    setName(d.name);
    if (!roomTouched) {
      const want = d.roomName.trim().toLowerCase();
      const match = want ? rooms.find((r) => r.name.trim().toLowerCase() === want) : undefined;
      setRoomId(match?.id ?? "");
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (isEdit) {
        if (!initial) throw new Error("missing initial device for edit");
        await api.updateDevice(initial.id, { name, room_id: roomId });
        onSuccess(initial.id);
      } else {
        if (!selected || !manager) {
          setError(`Pick a device from ${vendorName || "the vendor cloud"} first.`);
          return;
        }
        const common = {
          external_id: selected.external_id,
          name: name.trim() || selected.name,
          room_id: roomId || undefined,
        };
        const { id } =
          manager === "poly_lens"
            ? await api.createDevice({ source: "poly_lens", type: "teams_room", platform, ...common })
            : await api.createDevice({
                source: "neat_pulse",
                // Sent only when Pulse doesn't give the mode; otherwise Pulse decides.
                type: neatDevices?.find((d) => d.external_id === selected.external_id)?.suggested_type ? undefined : roomType,
                ...common,
              });
        onSuccess(id);
      }
    } catch (err) {
      setError(apiMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  const errorBox = error && (
    <div
      role="alert"
      className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]"
    >
      {error}
    </div>
  );

  const roomSelect = (
    <div>
      <label className={labelClass} htmlFor="tr-room">
        Room in M.A.R.C.U.S.
      </label>
      <select
        id="tr-room"
        className={inputClass}
        value={roomId}
        onChange={(e) => {
          setRoomTouched(true);
          setRoomId(e.target.value);
        }}
        disabled={loadingLookups}
      >
        <option value="">— Unassigned —</option>
        {rooms.map((r) => (
          <option key={r.id} value={r.id}>
            {roomLabel(r, buildings)}
          </option>
        ))}
      </select>
      {!isEdit && selected && !roomId && selected.roomName && (
        <p className="mt-1 text-xs text-muted-foreground">
          No room named &ldquo;{selected.roomName}&rdquo; here — choose one.
        </p>
      )}
    </div>
  );

  const buttons = (
    <div className="flex items-center justify-end gap-2 pt-2 border-t">
      <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
        Cancel
      </Button>
      <Button type="submit" disabled={submitting || loadingLookups || (!isEdit && !selected)}>
        {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
        {isEdit ? "Save changes" : "Create device"}
      </Button>
    </div>
  );

  if (isEdit) {
    const src = initial?.source === "neat_pulse" ? "Neat Pulse" : "Poly Lens";
    const kind = initial?.type === "zoom_room" ? "Zoom Room" : "Teams Room";
    return (
      <form onSubmit={handleSubmit} className="space-y-4">
        {errorBox}
        <p className="text-sm text-muted-foreground">
          This {kind} is managed by {src}. Its details come from {src}; here you can change its name and room.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label className={labelClass} htmlFor="tr-name">
              Name
            </label>
            <input id="tr-name" className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          {roomSelect}
        </div>
        {buttons}
      </form>
    );
  }

  const status = (p: CloudSource, available: boolean, why: string) =>
    integrations === null
      ? "Checking…"
      : integrationsError
        ? integrationsError
        : !available
          ? why
          : connected[p]
            ? "● Connected"
            : `Connect ${CLOUD_SOURCE_LABELS[p]} in Integrations first`;

  const managerOption = (p: CloudSource, logo: string, available: boolean, why: string) => {
    const usable = available && connected[p];
    const isSel = manager === p;
    return (
      <button
        key={p}
        type="button"
        role="radio"
        aria-checked={isSel}
        disabled={!usable}
        onClick={() => setManager(p)}
        className={cn(
          "flex w-full items-center gap-2 rounded-md border p-2.5 text-left text-sm min-h-[44px]",
          isSel ? "border-2 border-primary" : usable ? "hover:bg-muted/50" : "cursor-not-allowed opacity-60"
        )}
      >
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={logo} alt="" className="h-5 w-5 object-contain" />
        <span className="font-semibold">Managed by {CLOUD_SOURCE_LABELS[p]}</span>
        <span
          className={cn("ml-auto text-xs", usable ? "[color:hsl(var(--success))]" : "text-muted-foreground")}
        >
          {status(p, available, why)}
        </span>
      </button>
    );
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {errorBox}
      {typeSelect}

      {!isZoom && (
        <div>
          <div className={labelClass}>Platform</div>
          <div className="mt-1 grid grid-cols-2 gap-2" role="radiogroup" aria-label="Platform">
            {(
              [
                ["windows", "Windows", "Teams Rooms on Windows"],
                ["android", "Android", "Teams Rooms on Android"],
              ] as const
            ).map(([v, label, hint]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={platform === v}
                onClick={() => setPlatform(v)}
                className={cn(
                  "rounded-md border p-2.5 text-center text-sm min-h-[44px]",
                  platform === v ? "border-2 border-primary bg-primary/5" : "hover:bg-muted/50"
                )}
              >
                <div className="font-semibold">{label}</div>
                <div className="text-[11px] text-muted-foreground">{hint}</div>
              </button>
            ))}
          </div>
        </div>
      )}

      <div>
        <div className={labelClass}>Managed by</div>
        <div className="mt-1 space-y-1.5" role="radiogroup" aria-label="Managed by">
          {!isZoom && managerOption("poly_lens", "/integrations/poly.svg", lensAvailable, "Teams Rooms only")}
          {managerOption("neat_pulse", "/integrations/neat.svg", neatAvailable, "Neat devices run Android")}
          <div className="rounded-md border p-2.5 text-sm text-muted-foreground opacity-60" aria-disabled="true">
            Logitech Sync — coming soon
          </div>
          {!isZoom && (
            <button
              type="button"
              onClick={() => onDirect?.()}
              disabled={platform !== "windows" || !onDirect}
              className={cn(
                "flex w-full items-center gap-2 rounded-md border p-2.5 text-left text-sm min-h-[44px]",
                platform === "windows" && onDirect ? "hover:bg-muted/50" : "cursor-not-allowed text-muted-foreground opacity-60"
              )}
            >
              <span className="font-semibold">Direct (Windows remote management)</span>
              <span className="ml-auto text-xs text-muted-foreground">
                {platform === "windows" ? "Read by a Collector over WinRM" : "Windows only"}
              </span>
            </button>
          )}
        </div>
      </div>

      {manager && (
        <div className="space-y-2">
          <label className={labelClass} htmlFor="tr-search">
            {vendorName} device
          </label>
          <input
            id="tr-search"
            className={inputClass}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={`Search ${vendorName} devices by name, serial or room`}
            autoComplete="off"
          />
          {manager === "poly_lens" && degraded && (
            <p className="text-xs [color:hsl(var(--warning))]">
              Lens didn&apos;t return Teams details, so all devices are listed.
            </p>
          )}
          {listError && (
            <p role="alert" className="text-xs [color:hsl(var(--destructive))]">
              {listError}
            </p>
          )}
          <div className="max-h-64 overflow-y-auto rounded-md border" role="listbox" aria-label={`${vendorName} devices`}>
            {listLoading && !rows && (
              <div className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading {vendorName} devices…
              </div>
            )}
            {rows && rows.length === 0 && (
              <div className="p-3 text-sm text-muted-foreground">No {vendorName} devices match.</div>
            )}
            {rows?.map((d) => {
              const isSel = selected?.external_id === d.external_id;
              return (
                <button
                  key={d.external_id}
                  type="button"
                  role="option"
                  aria-selected={isSel}
                  disabled={d.linked}
                  onClick={() => pick(d)}
                  title={d.hover}
                  className={cn(
                    "flex w-full flex-col gap-0.5 border-b px-3 py-2 text-left text-sm last:border-0 min-h-[44px]",
                    isSel && "bg-primary/10",
                    d.linked ? "cursor-not-allowed text-muted-foreground opacity-70" : "hover:bg-muted/50"
                  )}
                >
                  <span className="flex items-center gap-2">
                    <span aria-hidden="true">{isSel ? "◉" : "○"}</span>
                    <span className="font-medium">{d.name}</span>
                    <span
                      className={cn(
                        "ml-auto text-xs",
                        d.linked ? "text-muted-foreground" : d.connected ? "[color:hsl(var(--success))]" : "text-muted-foreground"
                      )}
                    >
                      {d.linked ? "Linked already" : d.connected ? "Online" : "Offline"}
                    </span>
                  </span>
                  <span className="pl-6 text-xs text-muted-foreground">{d.detail}</span>
                  {d.note && <span className="pl-6 text-xs text-muted-foreground">{d.note}</span>}
                  {d.monitoredAs && !d.linked && (
                    <span className="pl-6 text-xs [color:hsl(var(--warning))]">
                      Already monitored as &ldquo;{d.monitoredAs}&rdquo;
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          <p className="text-xs text-muted-foreground">
            {manager === "poly_lens"
              ? "Only Teams Rooms devices are listed. Serial and MAC are shown on hover."
              : `Room main units in ${PRIMARY_MODE_NAME[roomType]} mode (or whose mode Pulse doesn't give) are listed; paired Neat Pads show as the room's peripherals. Serial is shown on hover.`}
          </p>
        </div>
      )}

      {selected && (
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label className={labelClass} htmlFor="tr-name">
              Name
            </label>
            <input id="tr-name" className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          {roomSelect}
        </div>
      )}

      {errorBox}
      {buttons}
    </form>
  );
}
