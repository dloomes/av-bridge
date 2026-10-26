// Plain-language summaries of a Room Readiness step's stored results, for
// the run page and its PDF. The stored expected/actual keep every detail
// (command ids, raw device replies) for the API; people see a line per
// device: what was found, and where the reading came from.

import type { NightlyStepRow, StepReading } from "@/lib/api";

export interface ActualLine {
  device?: string; // set when the step covered more than one device
  value: string;
  reading?: StepReading;
}

const OP: Record<string, string> = { gt: ">", gte: ">=", lt: "<", lte: "<=", eq: "=", "==": "=" };

function text(v: unknown): string {
  if (v === undefined || v === null) return "-";
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  const s = JSON.stringify(v);
  return s.length > 80 ? s.slice(0, 77) + "..." : s;
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

const STATUS: Record<string, string> = {
  succeeded: "Succeeded",
  failed: "Failed",
  pending: "No response",
  in_progress: "No response",
};

// summariseExpected says what the step looked for.
export function summariseExpected(step: NightlyStepRow): string {
  const e = step.expected;
  if (!isObject(e)) return e === undefined ? "-" : text(e);
  const many = typeof e.target_devices === "number" && e.target_devices > 1 ? ` (${e.target_devices} devices)` : "";
  if ("status" in e) return text(e.status);
  if ("metric" in e) return `${text(e.metric)} ${OP[String(e.operator)] ?? text(e.operator)} ${text(e.threshold)}`;
  if ("command" in e) return text(e.command) + many;
  if ("action" in e) return (e.action === "power_on" ? "Power on" : e.action === "power_off" ? "Power off" : text(e.action)) + many;
  if ("label" in e) return text(e.label);
  // A check routine's own {op, value} form.
  if ("value" in e) return `${OP[String(e.op)] ?? text(e.op ?? "=")} ${text(e.value)}`;
  return text(e);
}

// commandOutcome reads one device's command result as a word, with the
// error when it failed.
function commandOutcome(r: Record<string, unknown>): string {
  const status = STATUS[String(r.status)] ?? text(r.status);
  return r.error ? `${status}: ${text(r.error)}` : status;
}

// summariseActual says what the step found, a line per device.
export function summariseActual(step: NightlyStepRow): ActualLine[] {
  const a = step.actual;
  const readings = step.readings ?? [];

  // Checks: {device: value}, with where each reading came from.
  if (readings.length > 0) {
    const byDevice = isObject(a) ? a : null;
    if (readings.length === 1) {
      const r = readings[0];
      const v = byDevice ? byDevice[r.device] ?? Object.values(byDevice)[0] : a;
      return [{ value: text(v), reading: r }];
    }
    return readings.map((r) => ({ device: r.device, value: text(byDevice?.[r.device]), reading: r }));
  }

  if (isObject(a)) {
    // Power and command steps.
    if (a.dispatched === false) {
      return [{ value: a.note ? text(a.note) : "Not sent (dry run)" }];
    }
    const per = a.per_device;
    if (Array.isArray(per)) {
      const rows = per.filter(isObject);
      if (rows.length === 1) return [{ value: commandOutcome(rows[0]) }];
      return rows.map((r) => ({ device: text(r.device_name), value: commandOutcome(r) }));
    }
    // Scheduled power off/on: {device name: status}.
    if (isObject(per)) {
      const rows = Object.entries(per);
      if (rows.length === 1) return [{ value: STATUS[String(rows[0][1])] ?? text(rows[0][1]) }];
      return rows.map(([d, s]) => ({ device: d, value: STATUS[String(s)] ?? text(s) }));
    }
    // A check from before readings were recorded: {device: value}.
    const entries = Object.entries(a);
    if (entries.length > 0 && entries.every(([, v]) => typeof v !== "object" || v === null)) {
      if (entries.length === 1) return [{ value: text(entries[0][1]) }];
      return entries.map(([d, v]) => ({ device: d, value: text(v) }));
    }
  }
  return [{ value: a === undefined ? "-" : text(a) }];
}
