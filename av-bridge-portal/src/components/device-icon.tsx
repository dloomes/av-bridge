import { Camera, Mic, Monitor, Network, PlugZap, SlidersHorizontal, Video } from "lucide-react";
import type { DeviceType } from "@/lib/types";
import { cn } from "@/lib/utils";

const map = {
  display: Monitor,
  conferencing: Video,
  audio: Mic,
  camera: Camera,
  control: SlidersHorizontal,
  avoip: Network,
  power: PlugZap,
} as const;

interface Props {
  type: DeviceType;
  className?: string;
}

export function DeviceIcon({ type, className }: Props) {
  const Icon = map[type] ?? Monitor;
  return <Icon className={cn("h-4 w-4", className)} />;
}
