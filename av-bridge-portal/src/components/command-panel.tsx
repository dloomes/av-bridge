"use client";

import { useMemo, useState } from "react";
import { Loader2, Lock, Send, Zap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { api } from "@/lib/api";
import { templatePlaceholders } from "@/lib/command-template";
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
// each entry describes ONE arg to prompt for — either a bounded integer
// (preset numbers, zoom positions) or a free-text field (SIP addresses,
// phone numbers). Kept as data (not a switch in send()) so adding a new
// prompt-command is a one-line change.
//
// Invalid input keeps the modal open with an inline error rather than
// sending garbage to the device.
type PromptedArg =
  | {
      kind: "number";
      arg: string;    // outbound args key
      label: string;  // shown as the input's field label
      min: number;
      max: number;
      helpText?: string; // optional line under the input; defaults to "Whole number between MIN and MAX"
    }
  | {
      kind: "text";
      arg: string;
      label: string;
      placeholder?: string;
      helpText?: string;
    };

const COMMAND_PROMPTS: Record<string, PromptedArg> = {
  preset_recall: { kind: "number", arg: "preset", label: "Recall preset number", min: 0, max: 255 },
  preset_set:    { kind: "number", arg: "preset", label: "Save current position as preset", min: 0, max: 255 },
  zoom_direct:   { kind: "number", arg: "position", label: "Zoom position (0 = wide, 16384 = full tele)", min: 0, max: 16384 },
  // ATEN PDU outlet switching — max here is the PE6108G's 8. Sites running a
  // larger model (PE7 / PE8 with 16 outlets) override outlet_count in YAML;
  // the bridge validates the actual bound and rejects out-of-range values.
  outlet_on:     { kind: "number", arg: "outlet", label: "Outlet number to switch ON", min: 1, max: 8 },
  outlet_off:    { kind: "number", arg: "outlet", label: "Outlet number to switch OFF", min: 1, max: 8 },
  outlet_reboot: { kind: "number", arg: "outlet", label: "Outlet number to reboot (power cycle)", min: 1, max: 8 },
  dial: {
    kind: "text",
    arg: "address",
    label: "Address to dial",
    placeholder: "sip:room@example.com",
    helpText: "SIP URI, H.323 address (host[:port] or user@host), or a phone number for POTS.",
  },
};

// Modal state for arg prompts. `template` is the device's command string
// when the fields came from its placeholders (shown as a hint).
interface PromptState {
  command: string;
  specs: PromptedArg[];
  values: string[];
  template?: string;
  error: string | null;
}

export function CommandPanel({ device, telemetry }: Props) {
  const [pending, setPending] = useState<string | null>(null);
  const [result, setResult] = useState<ResultState | null>(null);
  const [prompt, setPrompt] = useState<PromptState | null>(null);

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

  // Click handler for a command button. If the command needs an argument,
  // opens the prompt modal (deferring dispatch); otherwise sends immediately.
  const onCommandClick = (name: string) => {
    // A per-device template with {placeholders} is the source of truth for
    // what to ask; the fixed COMMAND_PROMPTS cover the vendor adapters.
    const template = device.commands?.[name];
    const placeholders = templatePlaceholders(template);
    const specs: PromptedArg[] =
      placeholders.length > 0
        ? placeholders.map((arg) => ({ kind: "text", arg, label: prettyName(arg) }))
        : COMMAND_PROMPTS[name]
          ? [COMMAND_PROMPTS[name]]
          : [];
    if (specs.length > 0) {
      setPrompt({
        command: name,
        specs,
        values: specs.map(() => ""),
        template: placeholders.length > 0 ? template : undefined,
        error: null,
      });
      return;
    }
    void dispatch(name);
  };

  // dispatch actually posts the command. Split from the click handler so the
  // modal's submit path can call it once validated. args is optional — no
  // args = the pre-prompt-command flow.
  const dispatch = async (name: string, args?: Record<string, unknown>) => {
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

  // Modal submit — validate every field, close on success, dispatch. Leaves
  // the modal open with an inline error on invalid input so the user can
  // correct without re-clicking the command button.
  const submitPrompt = () => {
    if (!prompt) return;
    const args: Record<string, unknown> = {};
    for (let i = 0; i < prompt.specs.length; i++) {
      const spec = prompt.specs[i];
      const trimmed = prompt.values[i].trim();
      if (trimmed === "") {
        setPrompt({ ...prompt, error: `${spec.label} is required.` });
        return;
      }
      if (spec.kind === "number") {
        const n = Number(trimmed);
        if (!Number.isInteger(n) || n < spec.min || n > spec.max) {
          setPrompt({
            ...prompt,
            error: `${spec.label}: enter an integer between ${spec.min} and ${spec.max}.`,
          });
          return;
        }
        args[spec.arg] = n;
        continue;
      }
      // Line-based protocols (Tesira TTP, Telnet) treat a newline as the end
      // of a command — never let one through inside a value.
      if (/[\r\n]/.test(trimmed)) {
        setPrompt({ ...prompt, error: `${spec.label} must be a single line.` });
        return;
      }
      // Further shape validation (SIP URI, dB range…) is the adapter's job.
      args[spec.arg] = trimmed;
    }
    setPrompt(null);
    void dispatch(prompt.command, args);
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
                  onClick={() => onCommandClick(cmd)}
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

        <Modal
          open={prompt !== null}
          onClose={() => setPrompt(null)}
          title={prompt ? prettyName(prompt.command) : ""}
          wide={false}
        >
          {prompt && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                submitPrompt();
              }}
              className="space-y-4"
            >
              {prompt.specs.map((spec, i) => {
                const onChange = (v: string) => {
                  const values = [...prompt.values];
                  values[i] = v;
                  setPrompt({ ...prompt, values, error: null });
                };
                return (
                  <div key={spec.arg} className="space-y-1.5">
                    <label
                      htmlFor={`prompt-${spec.arg}`}
                      className="text-sm font-medium text-foreground"
                    >
                      {spec.label}
                    </label>
                    {spec.kind === "number" ? (
                      <input
                        id={`prompt-${spec.arg}`}
                        type="number"
                        inputMode="numeric"
                        min={spec.min}
                        max={spec.max}
                        step={1}
                        autoFocus={i === 0}
                        value={prompt.values[i]}
                        onChange={(e) => onChange(e.target.value)}
                        placeholder={`${spec.min}–${spec.max}`}
                        className="w-full h-10 rounded-md border border-border bg-background px-3 text-sm outline-none transition-colors focus:border-primary focus:shadow-[0_0_0_3px_hsl(var(--primary)/0.15)]"
                      />
                    ) : (
                      <input
                        id={`prompt-${spec.arg}`}
                        type="text"
                        autoFocus={i === 0}
                        autoComplete="off"
                        spellCheck={false}
                        value={prompt.values[i]}
                        onChange={(e) => onChange(e.target.value)}
                        placeholder={spec.placeholder}
                        className="w-full h-10 rounded-md border border-border bg-background px-3 text-sm outline-none transition-colors focus:border-primary focus:shadow-[0_0_0_3px_hsl(var(--primary)/0.15)]"
                      />
                    )}
                    {(spec.helpText || spec.kind === "number") && (
                      <p className="text-xs text-muted-foreground">
                        {spec.helpText ??
                          (spec.kind === "number"
                            ? `Whole number between ${spec.min} and ${spec.max}.`
                            : "")}
                      </p>
                    )}
                  </div>
                );
              })}
              {prompt.template && (
                <p className="text-xs text-muted-foreground">
                  Sends{" "}
                  <code className="rounded bg-muted px-1 py-0.5 text-[11px] text-foreground/80">
                    {prompt.template}
                  </code>
                </p>
              )}
              {prompt.error && (
                <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs [color:hsl(var(--destructive))]">
                  {prompt.error}
                </div>
              )}
              <div className="flex justify-end gap-2 pt-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setPrompt(null)}
                >
                  Cancel
                </Button>
                <Button type="submit" size="sm">
                  Send
                </Button>
              </div>
            </form>
          )}
        </Modal>

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
