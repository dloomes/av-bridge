"use client";

import { useEffect, useMemo, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { apiMessage } from "@/lib/api-error";
import type { DeviceDetail, Integration, LensDevice, NamedRow } from "@/lib/types";
import { cn } from "@/lib/utils";

const labelClass = "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

type Platform = "windows" | "android";

interface Props {
  mode: "create" | "edit";
  // edit-mode: the Lens-managed device being renamed / moved.
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

// TeamsRoomForm is the device form for a Microsoft Teams Room managed by
// Poly Lens: no Collector, protocol, address or credentials. Create picks
// the device from the customer's Lens account; edit only changes the name
// and the room (everything else comes from Lens).
export function TeamsRoomForm({
  mode,
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
  const [platform, setPlatform] = useState<Platform>(initial?.platform ?? "windows");
  const [name, setName] = useState(initial?.name ?? "");
  const [roomId, setRoomId] = useState(initial?.room_id ?? "");
  const [roomTouched, setRoomTouched] = useState(false);
  const [selected, setSelected] = useState<LensDevice | null>(null);

  const [integrations, setIntegrations] = useState<Integration[] | null>(null);
  const [search, setSearch] = useState("");
  const [devices, setDevices] = useState<LensDevice[] | null>(null);
  const [degraded, setDegraded] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [integrationsError, setIntegrationsError] = useState<string | null>(null);
  const [listLoading, setListLoading] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const lensConnected = useMemo(() => {
    const l = integrations?.find((i) => i.provider === "poly_lens");
    return l?.status === "connected" || l?.status === "error";
  }, [integrations]);

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

  // Search Lens as the person types (server-side q), after a short pause.
  useEffect(() => {
    if (isEdit || !lensConnected) return;
    const ctrl = new AbortController();
    const t = setTimeout(() => {
      setListLoading(true);
      api
        .listLensDevices(search.trim() || undefined, ctrl.signal)
        .then((r) => {
          if (ctrl.signal.aborted) return;
          setDevices(r.devices ?? []);
          setDegraded(!!r.degraded);
          setListError(null);
        })
        .catch((e) => {
          if (!ctrl.signal.aborted) setListError(apiMessage(e));
        })
        .finally(() => {
          if (!ctrl.signal.aborted) setListLoading(false);
        });
    }, search ? 300 : 0);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  }, [isEdit, lensConnected, search]);

  function pick(d: LensDevice) {
    setSelected(d);
    setName(d.name);
    if (!roomTouched) {
      const want = d.room_name.trim().toLowerCase();
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
        if (!selected) {
          setError("Pick a device from Lens first.");
          return;
        }
        const { id } = await api.createDevice({
          source: "poly_lens",
          type: "teams_room",
          platform,
          external_id: selected.external_id,
          name: name.trim() || selected.name,
          room_id: roomId || undefined,
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
      {!isEdit && selected && !roomId && selected.room_name && (
        <p className="mt-1 text-xs text-muted-foreground">
          No room named &ldquo;{selected.room_name}&rdquo; here — choose one.
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
    return (
      <form onSubmit={handleSubmit} className="space-y-4">
        {errorBox}
        <p className="text-sm text-muted-foreground">
          This Teams Room is managed by Poly Lens. Its details come from Lens; here you can
          change its name and room.
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

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {errorBox}
      {typeSelect}

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

      <div>
        <div className={labelClass}>Managed by</div>
        <div className="mt-1 space-y-1.5">
          <div
            className={cn(
              "flex items-center gap-2 rounded-md border p-2.5 text-sm",
              lensConnected ? "border-2 border-primary" : "opacity-60"
            )}
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src="/integrations/poly.svg" alt="" className="h-5 w-5" />
            <span className="font-semibold">Poly Lens</span>
            <span className={cn("ml-auto text-xs", lensConnected ? "[color:hsl(var(--success))]" : "text-muted-foreground")}>
              {integrations === null
                ? "Checking…"
                : integrationsError
                  ? integrationsError
                  : lensConnected
                    ? "● Connected"
                    : "Connect Poly Lens in Integrations first"}
            </span>
          </div>
          <div className="rounded-md border p-2.5 text-sm text-muted-foreground opacity-60" aria-disabled="true">
            Logitech Sync — connect it in Integrations first
          </div>
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
        </div>
      </div>

      {lensConnected && (
        <div className="space-y-2">
          <label className={labelClass} htmlFor="tr-search">
            Lens device
          </label>
          <input
            id="tr-search"
            className={inputClass}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search Lens devices by name, serial or room"
            autoComplete="off"
          />
          {degraded && (
            <p className="text-xs [color:hsl(var(--warning))]">
              Lens didn&apos;t return Teams details, so all devices are listed.
            </p>
          )}
          {listError && (
            <p role="alert" className="text-xs [color:hsl(var(--destructive))]">
              {listError}
            </p>
          )}
          <div className="max-h-64 overflow-y-auto rounded-md border" role="listbox" aria-label="Lens devices">
            {listLoading && !devices && (
              <div className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading Lens devices…
              </div>
            )}
            {devices && devices.length === 0 && (
              <div className="p-3 text-sm text-muted-foreground">No Lens devices match.</div>
            )}
            {devices?.map((d) => {
              const linked = !!d.linked_device_id;
              const isSel = selected?.external_id === d.external_id;
              return (
                <button
                  key={d.external_id}
                  type="button"
                  role="option"
                  aria-selected={isSel}
                  disabled={linked}
                  onClick={() => pick(d)}
                  title={[d.serial && `Serial ${d.serial}`, d.mac && `MAC ${d.mac}`].filter(Boolean).join(" · ") || undefined}
                  className={cn(
                    "flex w-full flex-col gap-0.5 border-b px-3 py-2 text-left text-sm last:border-0 min-h-[44px]",
                    isSel && "bg-primary/10",
                    linked ? "cursor-not-allowed text-muted-foreground opacity-70" : "hover:bg-muted/50"
                  )}
                >
                  <span className="flex items-center gap-2">
                    <span aria-hidden="true">{isSel ? "◉" : "○"}</span>
                    <span className="font-medium">{d.name}</span>
                    <span
                      className={cn(
                        "ml-auto text-xs",
                        linked ? "text-muted-foreground" : d.connected ? "[color:hsl(var(--success))]" : "text-muted-foreground"
                      )}
                    >
                      {linked ? "Linked already" : d.connected ? "Online" : "Offline"}
                    </span>
                  </span>
                  <span className="pl-6 text-xs text-muted-foreground">
                    {[d.model, d.room_name && `Lens room: ${d.room_name}`, d.site_name].filter(Boolean).join(" · ")}
                  </span>
                  {d.already_monitored_as && !linked && (
                    <span className="pl-6 text-xs [color:hsl(var(--warning))]">
                      Already monitored as &ldquo;{d.already_monitored_as}&rdquo;
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          <p className="text-xs text-muted-foreground">
            Only Teams Rooms devices are listed. Serial and MAC are shown on hover.
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
