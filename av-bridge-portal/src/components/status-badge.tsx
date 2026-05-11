import { Badge } from "@/components/ui/badge";
import type { DeviceStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

const dotColors: Record<DeviceStatus, string> = {
  online: "bg-success",
  offline: "bg-destructive",
  degraded: "bg-warning",
  unknown: "bg-muted-foreground/40",
};

const variants: Record<DeviceStatus, "success" | "warning" | "offline" | "secondary"> = {
  online: "success",
  offline: "offline",
  degraded: "warning",
  unknown: "secondary",
};

const labels: Record<DeviceStatus, string> = {
  online: "Online",
  offline: "Offline",
  degraded: "Degraded",
  unknown: "Unknown",
};

interface Props {
  status: DeviceStatus;
  className?: string;
}

export function StatusBadge({ status, className }: Props) {
  return (
    <Badge variant={variants[status]} className={cn("gap-1.5", className)}>
      <span
        className={cn(
          "inline-block h-1.5 w-1.5 rounded-full",
          dotColors[status],
          status === "online" && "animate-pulseDot"
        )}
      />
      {labels[status]}
    </Badge>
  );
}
