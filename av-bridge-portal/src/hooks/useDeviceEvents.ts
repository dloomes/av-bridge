"use client";

import { useEffect, useRef, useState, useCallback } from "react";
import { currentToken, WS_BASE } from "@/lib/api";
import type { DeviceEvent } from "@/lib/types";

export type WsStatus = "connecting" | "open" | "closed";

interface Options {
  bufferSize?: number;
  reconnectDelayMs?: number;
}

export function useDeviceEvents(opts: Options = {}) {
  const { bufferSize = 20, reconnectDelayMs = 3000 } = opts;
  const [events, setEvents] = useState<DeviceEvent[]>([]);
  const [status, setStatus] = useState<WsStatus>("connecting");
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelled = useRef(false);

  const connect = useCallback(() => {
    if (cancelled.current) return;
    setStatus("connecting");
    try {
      const tok = currentToken();
      const url = tok
        ? `${WS_BASE}/ws/events?token=${encodeURIComponent(tok)}`
        : `${WS_BASE}/ws/events`;
      const ws = new WebSocket(url);
      wsRef.current = ws;

      ws.onopen = () => setStatus("open");
      ws.onclose = () => {
        setStatus("closed");
        wsRef.current = null;
        if (!cancelled.current) {
          reconnectTimer.current = setTimeout(connect, reconnectDelayMs);
        }
      };
      ws.onerror = () => {
        // onclose will handle the retry
      };
      ws.onmessage = (msg) => {
        try {
          const evt = JSON.parse(msg.data) as DeviceEvent;
          setEvents((prev) => {
            const next = [evt, ...prev];
            return next.length > bufferSize ? next.slice(0, bufferSize) : next;
          });
        } catch {
          // ignore malformed frames
        }
      };
    } catch {
      setStatus("closed");
      reconnectTimer.current = setTimeout(connect, reconnectDelayMs);
    }
  }, [bufferSize, reconnectDelayMs]);

  useEffect(() => {
    cancelled.current = false;
    connect();
    return () => {
      cancelled.current = true;
      if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
      wsRef.current?.close();
      wsRef.current = null;
    };
  }, [connect]);

  return { events, status };
}
