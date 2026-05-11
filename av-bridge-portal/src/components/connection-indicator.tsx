"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";

interface Props {
  intervalMs?: number;
}

// Derived from NEXT_PUBLIC_AV_BRIDGE_WS because it's the only target URL
// exposed to the browser (AV_BRIDGE_UPSTREAM is server-side only). Strips
// the ws:// scheme for readability.
const upstreamHost = (() => {
  const ws = process.env.NEXT_PUBLIC_AV_BRIDGE_WS ?? "ws://localhost:8080";
  return ws.replace(/^wss?:\/\//, "").replace(/\/+$/, "");
})();

export function ConnectionIndicator({ intervalMs = 10_000 }: Props) {
  const [connected, setConnected] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const check = async () => {
      try {
        await api.health();
        if (!cancelled) setConnected(true);
      } catch {
        if (!cancelled) setConnected(false);
      } finally {
        if (!cancelled) timer = setTimeout(check, intervalMs);
      }
    };
    check();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [intervalMs]);

  const label =
    connected === null ? "Checking…" : connected ? "Connected" : "Disconnected";
  const dot =
    connected === null
      ? "bg-muted-foreground/50"
      : connected
        ? "bg-success animate-pulseDot"
        : "bg-destructive";

  return (
    <div
      className="inline-flex items-center gap-2 rounded-full border bg-card px-3 py-1.5 text-xs font-medium text-muted-foreground"
      title={`Connected to ${upstreamHost}`}
    >
      <span className={cn("h-2 w-2 rounded-full", dot)} />
      <span className="text-foreground/80">av-bridge</span>
      <span className="text-muted-foreground">·</span>
      <span className="font-mono text-foreground/80">{upstreamHost}</span>
      <span className="text-muted-foreground">·</span>
      <span>{label}</span>
    </div>
  );
}
