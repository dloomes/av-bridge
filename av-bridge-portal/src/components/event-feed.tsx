"use client";

import { Radio, RadioTower } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useDeviceEvents } from "@/hooks/useDeviceEvents";
import { DeviceIcon } from "@/components/device-icon";
import { cn, formatTimestamp } from "@/lib/utils";
import type { DeviceEvent } from "@/lib/types";

interface Props {
  /** If set, show only events for this device id */
  deviceId?: string;
  className?: string;
  bufferSize?: number;
  emptyHint?: string;
}

export function EventFeed({ deviceId, className, bufferSize = 20, emptyHint }: Props) {
  const { events, status } = useDeviceEvents({ bufferSize });
  const filtered = deviceId
    ? events.filter((e) => e.device_id === deviceId)
    : events;

  return (
    <Card className={cn("flex flex-col h-full", className)}>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <div className="flex items-center gap-2">
          <RadioTower className="h-4 w-4 text-primary" />
          <CardTitle>Live events</CardTitle>
        </div>
        <WsStatus status={status} />
      </CardHeader>
      <CardContent className="flex-1 min-h-0 p-0">
        <ScrollArea className="h-full px-5 pb-5 scrollbar-thin">
          {filtered.length === 0 ? (
            <div className="text-sm text-muted-foreground py-8 text-center">
              {emptyHint ?? "Waiting for events…"}
            </div>
          ) : (
            <ul className="space-y-2">
              {filtered.map((evt, i) => (
                <EventRow key={`${evt.timestamp}-${i}`} event={evt} />
              ))}
            </ul>
          )}
        </ScrollArea>
      </CardContent>
    </Card>
  );
}

function EventRow({ event }: { event: DeviceEvent }) {
  return (
    <li className="rounded-md border bg-card/50 px-3 py-2 text-sm animate-fade-in">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <DeviceIcon
            type={event.device_type}
            className="h-3.5 w-3.5 text-primary flex-shrink-0"
          />
          <span className="font-medium truncate">{event.device_name}</span>
        </div>
        <span className="text-xs text-muted-foreground flex-shrink-0">
          {formatTimestamp(event.timestamp)}
        </span>
      </div>
      <div className="text-xs text-muted-foreground mt-1">
        <span className="font-mono">{event.event_type}</span>
        {event.payload && Object.keys(event.payload).length > 0 && (
          <span className="ml-2 text-foreground/70 truncate inline-block max-w-full">
            {summarize(event.payload)}
          </span>
        )}
      </div>
    </li>
  );
}

function summarize(payload: Record<string, unknown>): string {
  const entries = Object.entries(payload).slice(0, 3);
  return entries
    .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : v}`)
    .join(" ");
}

function WsStatus({ status }: { status: "connecting" | "open" | "closed" }) {
  const map = {
    open: { text: "Live", dot: "bg-success animate-pulseDot" },
    connecting: { text: "Connecting", dot: "bg-warning" },
    closed: { text: "Offline", dot: "bg-destructive" },
  } as const;
  const s = map[status];
  return (
    <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <Radio className="h-3 w-3" />
      <span className={cn("h-1.5 w-1.5 rounded-full", s.dot)} />
      <span>{s.text}</span>
    </div>
  );
}
