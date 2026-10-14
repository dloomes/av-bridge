import { Power, PowerOff } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { DeviceStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

// Device types (by protocol) whose own power state is shown as a pill
// next to their status. Each reports it in the power_status reading; add
// a protocol here, and its values to POWER_LABELS if they differ, to show
// it for more devices.
const POWER_PILL_PROTOCOLS = new Set(["sony_bravia"]);

// power_status values -> what the pill says. Sony reports "active" /
// "standby".
const POWER_LABELS: Record<string, { label: string; on: boolean }> = {
  active: { label: "Power on", on: true },
  on: { label: "Power on", on: true },
  standby: { label: "Standby", on: false },
  off: { label: "Off", on: false },
};

interface Props {
  protocol: string;
  powerStatus?: unknown;
  // The device's status. The power state is only current while the device
  // answers, so the pill is hidden otherwise.
  status: DeviceStatus;
  className?: string;
}

// PowerBadge shows a display's own power state — "Power on" or "Standby"
// — which "Online" alone doesn't tell you: a display in standby still
// answers on the network.
export function PowerBadge({ protocol, powerStatus, status, className }: Props) {
  if (!POWER_PILL_PROTOCOLS.has(protocol) || status !== "online") return null;
  const p = typeof powerStatus === "string" ? POWER_LABELS[powerStatus.toLowerCase()] : undefined;
  if (!p) return null;
  const Icon = p.on ? Power : PowerOff;
  return (
    <Badge
      variant={p.on ? "success" : "secondary"}
      className={cn("gap-1", className)}
      title={`Power state reported by the device: ${String(powerStatus)}`}
    >
      <Icon aria-hidden="true" className="h-3 w-3" />
      {p.label}
    </Badge>
  );
}
