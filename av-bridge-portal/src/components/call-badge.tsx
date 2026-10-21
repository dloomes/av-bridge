import { Phone, PhoneCall, PhoneIncoming, PhoneOutgoing, Pause } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import type { DeviceStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

type Call = { label: string; variant: BadgeProps["variant"]; icon: LucideIcon };

const IN_CALL: Call = { label: "In a call", variant: "success", icon: PhoneCall };
const IDLE: Call = { label: "Idle", variant: "secondary", icon: Phone };

// call_state values -> what the pill says. Cisco reports its call's
// Status lower-cased (connected, dialling, ringing, onhold...); Poly
// reports "active" or "idle".
const CALL_STATES: Record<string, Call> = {
  idle: IDLE,
  connected: IN_CALL,
  active: IN_CALL,
  ringing: { label: "Ringing", variant: "warning", icon: PhoneIncoming },
  dialling: { label: "Connecting", variant: "warning", icon: PhoneOutgoing },
  dialing: { label: "Connecting", variant: "warning", icon: PhoneOutgoing },
  connecting: { label: "Connecting", variant: "warning", icon: PhoneOutgoing },
  earlymedia: { label: "Connecting", variant: "warning", icon: PhoneOutgoing },
  onhold: { label: "On hold", variant: "warning", icon: Pause },
  remotehold: { label: "On hold", variant: "warning", icon: Pause },
  preserved: { label: "On hold", variant: "warning", icon: Pause },
  disconnecting: { label: "Ending call", variant: "secondary", icon: Phone },
};

interface Props {
  type: string;
  activeCalls?: unknown;
  callState?: unknown;
  // The device's status. Call readings are only current while the codec
  // answers, so the pill is hidden otherwise.
  status: DeviceStatus;
  className?: string;
}

// CallBadge shows whether a codec is in a call — "In a call", "Ringing",
// "Idle" — next to its status.
export function CallBadge({ type, activeCalls, callState, status, className }: Props) {
  if (type !== "conferencing" || status !== "online") return null;
  const calls = typeof activeCalls === "number" ? activeCalls : undefined;
  const state = typeof callState === "string" ? callState.toLowerCase() : "";
  let call = CALL_STATES[state];
  if (!call || (call === IDLE && calls && calls > 0)) {
    if (calls === undefined) return null;
    call = calls > 0 ? IN_CALL : IDLE;
  }
  const label = call === IN_CALL && calls && calls > 1 ? `In ${calls} calls` : call.label;
  const Icon = call.icon;
  return (
    <Badge
      variant={call.variant}
      className={cn("gap-1", className)}
      title={`Call state reported by the device: ${state || "unknown"}${calls !== undefined ? `, ${calls} active` : ""}`}
    >
      <Icon aria-hidden="true" className="h-3 w-3" />
      {label}
    </Badge>
  );
}
