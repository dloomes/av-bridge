"use client";

import { useEffect, useMemo, useState } from "react";
import { Clock, GitFork, Info, MapPin, Play, Power, PowerOff, Radio, Signal } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { DeviceSummary, NamedRow } from "@/lib/types";
import {
  STEP_TYPE_LABEL,
  STEP_TYPE_SUBLABEL,
  canDeviceDo,
  type DeviceCapabilities,
  type StepType,
} from "./step-types";

// StepPalette — right-hand column of the routine builder.
//
//   * Room picker at the top: purely a preview lens. Routines aren't
//     bound to a room in the data model; picking a room here just
//     filters the palette so users can see "what steps would this
//     routine support if I ran it against room X?".
//   * Step type list: each item is click-to-add. Greyed out when no
//     device in the picked room supports it (per adapter capability
//     declarations). Clicking a greyed item still works — the user
//     might be targeting a device_type or specific device_id that
//     lives in a different room — but the visual cue nudges toward
//     step types the room actually supports.

const STEP_ORDER: StepType[] = [
  "section",
  "wait",
  "power_on",
  "power_off",
  "device_command",
  "expect_status",
  "check_metric",
];

const STEP_ICON: Record<StepType, React.ComponentType<{ className?: string }>> = {
  section: GitFork,
  wait: Clock,
  power_on: Power,
  power_off: PowerOff,
  device_command: Play,
  expect_status: Signal,
  check_metric: Radio,
};

interface StepPaletteProps {
  disabled?: boolean;
  onAdd?: (type: StepType) => void;
}

export function StepPalette({ disabled, onAdd }: StepPaletteProps) {
  const [rooms, setRooms] = useState<NamedRow[]>([]);
  const [devices, setDevices] = useState<DeviceSummary[]>([]);
  const [roomID, setRoomID] = useState<string>("");

  useEffect(() => {
    const ctrl = new AbortController();
    Promise.all([
      api.listRooms(ctrl.signal),
      api.listDevices(ctrl.signal),
    ])
      .then(([rs, ds]) => {
        if (ctrl.signal.aborted) return;
        setRooms(rs);
        setDevices(ds);
        // Default to first room with devices, else first room, else none.
        const firstWithDevices = rs.find((r) => ds.some((d) => d.room_id === r.id));
        setRoomID((firstWithDevices ?? rs[0])?.id ?? "");
      })
      .catch(() => {});
    return () => ctrl.abort();
  }, []);

  const roomDevices = useMemo(
    () => (roomID ? devices.filter((d) => d.room_id === roomID) : []),
    [roomID, devices]
  );

  const aggregated = useMemo(
    () => aggregateCapabilities(roomDevices),
    [roomDevices]
  );

  return (
    <Card className="sticky top-4">
      <CardContent className="p-4 space-y-4">
        {/* Room picker ─────────────────────────────────────────────── */}
        <div className="space-y-1">
          <label
            htmlFor="palette-room"
            className="text-[10px] uppercase tracking-wider font-medium text-muted-foreground flex items-center gap-1.5"
          >
            <MapPin aria-hidden="true" className="h-3 w-3" />
            Preview against room
          </label>
          <select
            id="palette-room"
            value={roomID}
            onChange={(e) => setRoomID(e.target.value)}
            className="w-full rounded-md border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">— No room selected —</option>
            {rooms.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
          <div className="text-[11px] text-muted-foreground">
            {roomID
              ? `${roomDevices.length} device${roomDevices.length === 1 ? "" : "s"} · ${
                  aggregated.commands.length
                } commands · ${aggregated.metrics.length} metrics`
              : "Pick a room to see what steps it supports."}
          </div>
        </div>

        {/* Step type list ──────────────────────────────────────────── */}
        <div className="space-y-1.5">
          <div>
            <h3 className="text-sm font-semibold">Steps</h3>
            <p className="text-xs text-muted-foreground mt-0.5">
              Click to add. Drag inside the canvas to reorder.
            </p>
          </div>

          {STEP_ORDER.map((t) => {
            const Icon = STEP_ICON[t];
            const supported = roomID
              ? // At least one device in the room supports this step type,
                // OR the step is structural (wait/section) which never needs
                // device support.
                roomDevices.some((d) => canDeviceDo(d.capabilities, t))
              : true; // No room selected → don't grey anything
            return (
              <button
                key={t}
                type="button"
                disabled={disabled}
                onClick={() => onAdd?.(t)}
                title={
                  !supported
                    ? `No device in this room advertises ${STEP_TYPE_LABEL[t].toLowerCase()}.`
                    : undefined
                }
                className={`w-full rounded-md border bg-background px-3 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                  supported
                    ? "hover:bg-accent hover:border-accent-foreground/20"
                    : "opacity-50 hover:bg-muted/30"
                } disabled:opacity-50 disabled:cursor-not-allowed`}
              >
                <div className="flex items-start gap-2.5">
                  <div
                    className={`h-7 w-7 rounded-md flex items-center justify-center shrink-0 ${
                      supported ? "bg-primary/10" : "bg-muted"
                    }`}
                  >
                    <Icon
                      aria-hidden="true"
                      className={`h-3.5 w-3.5 ${
                        supported ? "text-primary" : "text-muted-foreground"
                      }`}
                    />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium leading-tight">
                      {STEP_TYPE_LABEL[t]}
                    </div>
                    <div className="text-xs text-muted-foreground leading-tight mt-0.5">
                      {STEP_TYPE_SUBLABEL[t]}
                    </div>
                  </div>
                </div>
              </button>
            );
          })}
        </div>

        {roomID && aggregated.commands.length > 0 && (
          <div className="rounded-md border bg-muted/20 p-2.5 text-[11px] text-muted-foreground space-y-1">
            <div className="font-medium text-foreground">Available in this room</div>
            <div>
              <span className="font-medium">Commands:</span>{" "}
              <span className="font-mono">
                {aggregated.commands.slice(0, 6).join(", ")}
                {aggregated.commands.length > 6 &&
                  ` +${aggregated.commands.length - 6}`}
              </span>
            </div>
            <div>
              <span className="font-medium">Metrics:</span>{" "}
              <span className="font-mono">
                {aggregated.metrics.slice(0, 6).join(", ")}
                {aggregated.metrics.length > 6 &&
                  ` +${aggregated.metrics.length - 6}`}
              </span>
            </div>
          </div>
        )}

        {!roomID && (
          <div className="flex items-start gap-1.5 rounded-md border border-dashed bg-muted/30 p-2.5 text-[11px] text-muted-foreground">
            <Info aria-hidden="true" className="h-3 w-3 mt-0.5 shrink-0" />
            <span>Preview is optional. Palette shows all step types until you pick a room.</span>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// aggregateCapabilities unions the power/commands/metrics fields across
// the room's devices. A step type is "supported" by the room if any
// device supports it — the executor's target model (scope=room,
// device_type, device_id) picks the right subset at run time.
function aggregateCapabilities(devices: DeviceSummary[]): {
  power: { on: boolean; off: boolean };
  commands: string[];
  metrics: string[];
} {
  const power = { on: false, off: false };
  const commands = new Set<string>();
  const metrics = new Set<string>();
  for (const d of devices) {
    const c = d.capabilities as DeviceCapabilities | undefined;
    if (!c) continue;
    if (c.power?.on) power.on = true;
    if (c.power?.off) power.off = true;
    for (const cmd of c.commands ?? []) commands.add(cmd);
    for (const m of c.metrics ?? []) metrics.add(m);
  }
  return {
    power,
    commands: Array.from(commands).sort(),
    metrics: Array.from(metrics).sort(),
  };
}
