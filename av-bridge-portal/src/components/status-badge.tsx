import { Badge } from "@/components/ui/badge";
import type { DeviceStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

const dotColors: Record<DeviceStatus, string> = {
  online: "bg-success",
  offline: "bg-destructive",
  unknown: "bg-muted-foreground/40",
  switched_off: "bg-sky-500",
};

const variants: Record<
  DeviceStatus,
  "success" | "warning" | "offline" | "secondary" | "switchedOff"
> = {
  online: "success",
  offline: "offline",
  unknown: "secondary",
  switched_off: "switchedOff",
};

const labels: Record<DeviceStatus, string> = {
  online: "Online",
  offline: "Offline",
  unknown: "Unknown",
  switched_off: "Switched off",
};

interface Props {
  status: DeviceStatus;
  className?: string;
  // Optional derived state of the device's collector. When status is
  // "unknown" and collectorStatus is "offline", we know exactly WHY the
  // device is unknown — the collector isn't reporting. Rendering that
  // as its own label ("Collector offline") tells the operator to
  // investigate the collector rather than the device.
  collectorStatus?: "online" | "offline" | "unknown";
  // Why a Poly Lens device is unknown ("Poly Lens unreachable"). Shown in
  // place of "Unknown" / "Collector offline" when status is unknown.
  reason?: string;
}

export function StatusBadge({ status, className, collectorStatus, reason }: Props) {
  const hasReason = status === "unknown" && !!reason;
  const isCollectorFault =
    !hasReason && status === "unknown" && collectorStatus === "offline";
  const label = hasReason ? reason : isCollectorFault ? "Collector offline" : labels[status];
  // Amber/warning tone for collector-fault so the pill visually
  // separates from an ordinary "Unknown" and doesn't look like a
  // healthy device.
  const variant = isCollectorFault || hasReason ? "warning" : variants[status];
  const dot = isCollectorFault || hasReason ? "bg-warning" : dotColors[status];

  return (
    <Badge
      variant={variant}
      className={cn("gap-1.5", className)}
      title={
        status === "switched_off"
          ? "Switched off for the night by Room Readiness"
          : undefined
      }
    >
      <span
        className={cn(
          "inline-block h-1.5 w-1.5 rounded-full",
          dot,
          status === "online" && "animate-pulseDot"
        )}
      />
      {label}
    </Badge>
  );
}
