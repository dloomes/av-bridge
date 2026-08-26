"use client";

// RoutineContext ferries the two lookup lists the step editor needs
// (devices + adapter catalogue) down through the RoutineBuilder ->
// StepCanvas -> StepEditor tree without prop drilling. Both lists are
// fetched once at the editor page level and rarely change during
// editing, so passing them via context is cheaper than re-fetching
// per field and keeps the leaf editors readable.

import { createContext, useContext, type ReactNode } from "react";
import type { AdapterInfo, DeviceSummary } from "@/lib/types";

// Constrain device type UI to the five categories the platform uses.
// Kept as data (not an enum) so the label text is easy to iterate on
// without touching consuming components.
export const DEVICE_TYPE_OPTIONS: Array<{ value: string; label: string }> = [
  { value: "display", label: "Display" },
  { value: "camera", label: "Camera" },
  { value: "conferencing", label: "Video codec" },
  { value: "audio", label: "DSP / audio" },
  { value: "control", label: "Control panel" },
];

interface RoutineContextValue {
  devices: DeviceSummary[];
  adapters: AdapterInfo[];
}

const Ctx = createContext<RoutineContextValue>({ devices: [], adapters: [] });

export function RoutineContextProvider({
  devices,
  adapters,
  children,
}: RoutineContextValue & { children: ReactNode }) {
  return <Ctx.Provider value={{ devices, adapters }}>{children}</Ctx.Provider>;
}

export function useRoutineContext(): RoutineContextValue {
  return useContext(Ctx);
}

// commandsForDevice returns the union of commands available on the
// device's capabilities (bridge-reported) OR — if capabilities are
// missing / empty — the adapter catalogue's static list for that
// device's protocol. The bridge-reported list wins because it reflects
// what the adapter actually accepts for this specific device (e.g. an
// operator-defined command in cfg.Commands).
export function commandsForDevice(
  device: DeviceSummary,
  adapters: AdapterInfo[]
): string[] {
  const fromDevice = device.capabilities?.commands;
  if (fromDevice && fromDevice.length > 0) return fromDevice;
  const adapter = adapters.find((a) => a.id === device.protocol);
  return adapter?.commands ?? [];
}

// commandsForDeviceType unions the commands across every adapter that
// serves the given device type. Used when the target is a device type
// rather than a specific device — the caller picks a command that any
// device of that type could accept.
export function commandsForDeviceType(
  deviceType: string,
  adapters: AdapterInfo[]
): string[] {
  const set = new Set<string>();
  for (const a of adapters) {
    if (!a.device_types.includes(deviceType)) continue;
    for (const cmd of a.commands ?? []) set.add(cmd);
  }
  return Array.from(set).sort();
}
