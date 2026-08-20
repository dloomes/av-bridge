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

function prettyName(name: string): string {
  return name
    .replace(/_/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

// Commands that need an argument before sending. Keyed by command name;
// the entry describes ONE numeric arg to prompt for. Kept as data (not a
// switch in send()) so adding a new prompt-command is a one-line change.
// Non-integer input and out-of-range values cancel the send with a
// visible error rather than sending garbage to the device.
interface PromptedArg {
  arg: string;   // key to place in the outbound args map
  label: string; // human prompt copy
  min: number;
  max: number;
}
const COMMAND_PROMPTS: Record<string, PromptedArg> = {
  preset_recall: { arg: "preset", label: "Recall preset number", min: 0, max: 255 },
  preset_set:    { arg: "preset", label: "Save current position as preset", min: 0, max: 255 },
  zoom_direct:   { arg: "position", label: "Zoom position (0 = wide, 16384 = full tele)", min: 0, max: 16384 },
};

export function CommandPanel({ device, telemetry }: Props) {
  const [pending, setPending] = useState<string | null>(null);
  const [result, setResult] = useState<ResultState | null>(null);

  // Commands come from two sources merged: the adapter's declared
  // Capabilities().Commands (what the vendor integration supports out of
  // the box) plus any per-device YAML `commands:` map (site-specific
  // extensions — Tesira's whole model is that commands are user-defined
  // TTP strings). Union, dedup, preserve adapter order first.
  const commands = useMemo(() => {
    const seen = new Set<string>();
    const merged: string[] = [];
    for (const c of device.capabilities?.commands ?? []) {
      if (!seen.has(c)) {
        seen.add(c);
        merged.push(c);
      }
    }
    for (const c of Object.keys(device.commands ?? {})) {
      if (!seen.has(c)) {
        seen.add(c);
        merged.push(c);
      }
    }
    return merged;
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
    // If this command takes an argument, prompt for it before sending.
    // Using window.prompt for now — it's zero-plumbing and matches the
    // native "get me a number" browser behaviour. Swap for a proper modal
    // if the prompt list grows or we need richer arg types.
    let args: Record<string, unknown> | undefined;
    const promptSpec = COMMAND_PROMPTS[name];
    if (promptSpec) {
      const raw = window.prompt(
        `${promptSpec.label} (${promptSpec.min}-${promptSpec.max})`
      );
      if (raw === null) return; // user cancelled — silent no-op, matches every other cancel path
      const trimmed = raw.trim();
      const n = Number(trimmed);
      if (
        trimmed === "" ||
        !Number.isInteger(n) ||
        n < promptSpec.min ||
        n > promptSpec.max
      ) {
        setResult({
          ok: false,
          command: name,
          message: `${promptSpec.label} must be an integer ${promptSpec.min}-${promptSpec.max} (got "${raw}")`,
          at: Date.now(),
        });
        return;
      }
      args = { [promptSpec.arg]: n };
    }

    setPending(name);
    try {
      const res: CommandResponse = await api.sendCommand(device.id, { name, args });
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
