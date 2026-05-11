"use client";

import { useMemo, useState } from "react";
import { Loader2, Lock, Send, Zap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { CommandResponse, DeviceDetail, Telemetry } from "@/lib/types";
import { cn } from "@/lib/utils";

interface Props {
  device: DeviceDetail;
  telemetry: Telemetry | null;
}

interface ResultState {
  ok: boolean;
  command: string;
  message: string;
  raw?: string;
  latencyMs?: number;
  at: number;
}

const TYPE_DEFAULTS: Record<string, string[]> = {
  display: ["power_on", "power_off", "input_hdmi1", "input_hdmi2", "input_hdmi3"],
  conferencing: ["mute", "unmute", "dial", "hangup"],
  audio: ["mute", "unmute", "vol_up", "vol_dn"],
  camera: ["home"],
};

function prettyName(name: string): string {
  return name
    .replace(/_/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

export function CommandPanel({ device, telemetry }: Props) {
  const [pending, setPending] = useState<string | null>(null);
  const [result, setResult] = useState<ResultState | null>(null);

  const commands = useMemo(() => {
    const fromDevice = device.commands ? Object.keys(device.commands) : [];
    if (fromDevice.length > 0) return fromDevice;
    return TYPE_DEFAULTS[device.type] ?? [];
  }, [device]);

  const callActive = isCallActive(device.type, telemetry);

  const visibleCommands = commands.filter((cmd) => {
    if (device.type !== "conferencing") return true;
    if (cmd === "dial" || cmd === "hangup") {
      // hide dial when a call is active, hide hangup when no call
      return cmd === "dial" ? !callActive : callActive;
    }
    return true;
  });

  const send = async (name: string) => {
    setPending(name);
    try {
      const res: CommandResponse = await api.sendCommand(device.id, { name });
      setResult({
        ok: true,
        command: name,
        message: "Sent successfully",
        raw: res.raw,
        latencyMs: res.latency_ms,
        at: Date.now(),
      });
    } catch (err) {
      setResult({
        ok: false,
        command: name,
        message: err instanceof Error ? err.message : String(err),
        at: Date.now(),
      });
    } finally {
      setPending(null);
    }
  };

  // Devices running as appliances (Microsoft Teams Rooms, Zoom Rooms, etc.)
  // expose device_mode_active=true and reject every write endpoint with 403.
  // Hide the buttons rather than letting users press them and see errors.
  const applianceLocked = telemetry?.metrics?.device_mode_active === true;

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <Zap className="h-4 w-4 text-primary" />
          <CardTitle>Commands</CardTitle>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {applianceLocked ? (
          <div className="flex items-start gap-3 rounded-md border border-warning/30 bg-warning/5 p-3 text-sm">
            <Lock className="h-4 w-4 mt-0.5 flex-shrink-0 [color:hsl(var(--warning))]" />
            <div className="space-y-1">
              <div className="font-medium [color:hsl(var(--warning))]">
                Control disabled — appliance mode
              </div>
              <p className="text-muted-foreground text-xs leading-relaxed">
                This device is running as a Microsoft Teams Rooms or Zoom Rooms
                appliance, so its REST API is read-only. Telemetry is still
                live, but commands are locked at the device.
              </p>
            </div>
          </div>
        ) : visibleCommands.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No commands defined for this device.
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {visibleCommands.map((cmd) => {
              const isPending = pending === cmd;
              const tone = commandTone(cmd);
              return (
                <Button
                  key={cmd}
                  size="sm"
                  variant={tone}
                  disabled={pending !== null}
                  onClick={() => send(cmd)}
                >
                  {isPending ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Send className="h-3.5 w-3.5" />
                  )}
                  {prettyName(cmd)}
                </Button>
              );
            })}
          </div>
        )}

        {result && (
          <div
            className={cn(
              "rounded-md border p-3 text-xs space-y-1",
              result.ok
                ? "border-success/30 bg-success/5"
                : "border-destructive/30 bg-destructive/5"
            )}
          >
            <div className="flex items-center justify-between font-medium">
              <span
                className={cn(
                  result.ok
                    ? "[color:hsl(var(--success))]"
                    : "[color:hsl(var(--destructive))]"
                )}
              >
                {result.ok ? "OK" : "Error"} · {prettyName(result.command)}
              </span>
              {result.latencyMs != null && (
                <span className="text-muted-foreground">
                  {Math.round(result.latencyMs / 1_000_000) || result.latencyMs} ms
                </span>
              )}
            </div>
            <div className="text-muted-foreground">{result.message}</div>
            {result.raw && (
              <pre className="text-[11px] text-foreground/70 bg-muted/50 rounded p-2 overflow-x-auto whitespace-pre-wrap">
                {result.raw}
              </pre>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function isCallActive(type: string, tel: Telemetry | null): boolean {
  if (type !== "conferencing" || !tel?.metrics) return false;
  const active = tel.metrics["active_calls"];
  if (typeof active === "number" && active > 0) return true;
  const state = tel.metrics["call_state"];
  if (typeof state === "string" && state.toLowerCase() !== "idle") return true;
  return false;
}

function commandTone(
  name: string
): "default" | "destructive" | "outline" | "secondary" | "success" {
  if (/off|hangup|mute(?!_off)/i.test(name)) return "destructive";
  if (/on|dial|unmute/i.test(name)) return "success";
  return "outline";
}
