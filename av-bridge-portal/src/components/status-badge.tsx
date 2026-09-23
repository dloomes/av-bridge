import { Badge } from "@/components/ui/badge";
import type { DeviceStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

const dotColors: Record<DeviceStatus, string> = {
  online: "bg-success",
  offline: "bg-destructive",
  unknown: "bg-muted-foreground/40",
};

const variants: Record<DeviceStatus, "success" | "warning" | "offline" | "secondary"> = {
  online: "success",
  offline: "offline",
  unknown: "secondary",
};

const labels: Record<DeviceStatus, string> = {
  online: "Online",
  offline: "Offline",
  unknown: "Unknown",
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
}

export function StatusBadge({ status, className, collectorStatus }: Props) {
  const isCollectorFault =
    status === "unknown" && collectorStatus === "offline";
  const label = isCollectorFault ? "Collector offline" : labels[status];
  // Amber/warning tone for collector-fault so the pill visually
  // separates from an ordinary "Unknown" and doesn't look like a
  // healthy device.
  const variant = isCollectorFault ? "warning" : variants[status];
  const dot = isCollectorFault ? "bg-warning" : dotColors[status];

  return (
    <Badge variant={variant} className={cn("gap-1.5", className)}>
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
