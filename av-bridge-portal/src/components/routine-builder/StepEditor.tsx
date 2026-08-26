"use client";

import { useMemo, useState } from "react";
import {
  DEVICE_TYPE_OPTIONS,
  commandsForDevice,
  commandsForDeviceType,
  useRoutineContext,
} from "./RoutineContext";
import {
  ON_FAILURE_OPTIONS,
  targetKind,
  type CheckMetricStep,
  type DeviceCommandStep,
  type ExpectStatusStep,
  type MetricOperator,
  type OnFailure,
  type PowerStep,
  type SectionStep,
  type Step,
  type Target,
  type TargetKind,
  type WaitStep,
} from "./step-types";
import { buildingFor, roomFor } from "@/lib/utils";

// StepEditor — renders the per-type config form inside an expanded
// step card. Kept in one file so future step types are a single edit
// away; if this grows past ~400 lines split per type into a folder.

interface StepEditorProps {
  step: Step;
  onChange: (next: Step) => void;
  disabled?: boolean;
}

export function StepEditor({ step, onChange, disabled }: StepEditorProps) {
  return (
    <div className="space-y-4">
      <NameField step={step} onChange={onChange} disabled={disabled} />
      {step.type === "wait" && (
        <WaitFields step={step} onChange={onChange} disabled={disabled} />
      )}
      {step.type === "section" && (
        <SectionFields step={step} onChange={onChange} disabled={disabled} />
      )}
      {step.type === "expect_status" && (
        <ExpectStatusFields step={step} onChange={onChange} disabled={disabled} />
      )}
      {step.type === "check_metric" && (
        <CheckMetricFields step={step} onChange={onChange} disabled={disabled} />
      )}
      {(step.type === "power_on" || step.type === "power_off") && (
        <PowerFields step={step} onChange={onChange} disabled={disabled} />
      )}
      {step.type === "device_command" && (
        <DeviceCommandFields step={step} onChange={onChange} disabled={disabled} />
      )}
    </div>
  );
}

// ── Common helpers ──────────────────────────────────────────────────────

const inputCls =
  "w-full rounded-md border bg-background px-3 py-1.5 text-sm disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const labelCls = "text-xs font-medium text-muted-foreground";

function Field({
  label,
  children,
  hint,
  full,
}: {
  label: string;
  children: React.ReactNode;
  hint?: string;
  full?: boolean;
}) {
  return (
    <div className={full ? "col-span-2 space-y-1" : "space-y-1"}>
      <label className={labelCls}>{label}</label>
      {children}
      {hint && <div className="text-[11px] text-muted-foreground">{hint}</div>}
    </div>
  );
}

function Grid({ children }: { children: React.ReactNode }) {
  return <div className="grid grid-cols-2 gap-x-4 gap-y-3">{children}</div>;
}

function NameField({ step, onChange, disabled }: StepEditorProps) {
  return (
    <Field label="Step name (optional)">
      <input
        type="text"
        value={step.name ?? ""}
        placeholder="Auto-generated from step type"
        onChange={(e) => onChange({ ...step, name: e.target.value || undefined })}
        disabled={disabled}
        className={inputCls}
      />
    </Field>
  );
}

// ── target + on_failure ─────────────────────────────────────────────────

function TargetField({
  target,
  onChange,
  disabled,
}: {
  target: Target;
  onChange: (t: Target) => void;
  disabled?: boolean;
}) {
  const { devices } = useRoutineContext();
  const kind = targetKind(target);
  const setKind = (k: TargetKind) => {
    if (k === "room") onChange({ scope: "room" });
    else if (k === "device_type") onChange({ device_type: "" });
    else onChange({ device_id: "" });
  };

  // Group devices for the specific-device select by Building / Room so
  // an operator can find "the TV in Boardroom" rather than parsing a
  // flat name list. Devices with no room bucket under "Unassigned".
  const grouped = useMemo(() => {
    const map = new Map<string, typeof devices>();
    for (const d of devices) {
      const b = buildingFor(d);
      const r = roomFor(d);
      const key = b ? `${b} · ${r}` : r || "Unassigned";
      const arr = map.get(key) ?? [];
      arr.push(d);
      map.set(key, arr);
    }
    return Array.from(map.entries())
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([label, list]) => ({
        label,
        devices: [...list].sort((a, b) => a.name.localeCompare(b.name)),
      }));
  }, [devices]);

  return (
    <>
      <Field label="Target">
        <select
          value={kind}
          onChange={(e) => setKind(e.target.value as TargetKind)}
          disabled={disabled}
          className={inputCls}
        >
          <option value="room">Whole room</option>
          <option value="device_type">By device type</option>
          <option value="device_id">Specific device</option>
        </select>
      </Field>
      {kind === "device_type" && (
        <Field label="Device type">
          <select
            value={"device_type" in target ? target.device_type : ""}
            onChange={(e) => onChange({ device_type: e.target.value })}
            disabled={disabled}
            className={inputCls}
          >
            <option value="">Choose a type…</option>
            {DEVICE_TYPE_OPTIONS.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
        </Field>
      )}
      {kind === "device_id" && (
        <Field label="Device">
          <select
            value={"device_id" in target ? target.device_id : ""}
            onChange={(e) => onChange({ device_id: e.target.value })}
            disabled={disabled}
            className={inputCls}
          >
            <option value="">Choose a device…</option>
            {grouped.map((g) => (
              <optgroup key={g.label} label={g.label}>
                {g.devices.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name} · {d.type}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </Field>
      )}
    </>
  );
}

function OnFailureField({
  value,
  onChange,
  disabled,
}: {
  value: OnFailure | undefined;
  onChange: (v: OnFailure) => void;
  disabled?: boolean;
}) {
  return (
    <Field label="On failure">
      <select
        value={value ?? "abort"}
        onChange={(e) => onChange(e.target.value as OnFailure)}
        disabled={disabled}
        className={inputCls}
      >
        {ON_FAILURE_OPTIONS.map((opt) => (
          <option key={opt} value={opt}>
            {opt}
          </option>
        ))}
      </select>
    </Field>
  );
}

// ── Per-type forms ──────────────────────────────────────────────────────

function WaitFields({
  step,
  onChange,
  disabled,
}: {
  step: WaitStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  return (
    <Grid>
      <Field label="Duration (seconds)">
        <input
          type="number"
          min={0}
          value={step.duration_seconds}
          onChange={(e) =>
            onChange({ ...step, duration_seconds: Number(e.target.value) || 0 })
          }
          disabled={disabled}
          className={inputCls}
        />
      </Field>
    </Grid>
  );
}

function SectionFields({
  step,
  onChange,
  disabled,
}: {
  step: SectionStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  return (
    <Grid>
      <Field label="Section label" full>
        <input
          type="text"
          value={step.label}
          onChange={(e) => onChange({ ...step, label: e.target.value })}
          disabled={disabled}
          className={inputCls}
        />
      </Field>
    </Grid>
  );
}

function ExpectStatusFields({
  step,
  onChange,
  disabled,
}: {
  step: ExpectStatusStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  return (
    <Grid>
      <TargetField
        target={step.target}
        onChange={(target) => onChange({ ...step, target })}
        disabled={disabled}
      />
      <Field label="Expected status">
        <select
          value={step.status}
          onChange={(e) =>
            onChange({ ...step, status: e.target.value as ExpectStatusStep["status"] })
          }
          disabled={disabled}
          className={inputCls}
        >
          <option value="online">online</option>
          <option value="degraded">degraded</option>
          <option value="offline">offline</option>
        </select>
      </Field>
      <OnFailureField
        value={step.on_failure}
        onChange={(v) => onChange({ ...step, on_failure: v })}
        disabled={disabled}
      />
    </Grid>
  );
}

function CheckMetricFields({
  step,
  onChange,
  disabled,
}: {
  step: CheckMetricStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  return (
    <Grid>
      <TargetField
        target={step.target}
        onChange={(target) => onChange({ ...step, target })}
        disabled={disabled}
      />
      <Field label="Metric name" hint="Must match an adapter-declared metric.">
        <input
          type="text"
          value={step.metric}
          placeholder="e.g. input_level_dbfs"
          onChange={(e) => onChange({ ...step, metric: e.target.value })}
          disabled={disabled}
          className={inputCls}
        />
      </Field>
      <Field label="Operator">
        <select
          value={step.operator}
          onChange={(e) =>
            onChange({ ...step, operator: e.target.value as MetricOperator })
          }
          disabled={disabled}
          className={inputCls}
        >
          <option value="gt">&gt;</option>
          <option value="gte">&gt;=</option>
          <option value="lt">&lt;</option>
          <option value="lte">&lt;=</option>
          <option value="eq">=</option>
        </select>
      </Field>
      <Field label="Threshold">
        <input
          type="number"
          value={step.threshold}
          onChange={(e) => onChange({ ...step, threshold: Number(e.target.value) || 0 })}
          disabled={disabled}
          className={inputCls}
        />
      </Field>
      <Field label="Sample window (seconds)">
        <input
          type="number"
          min={0}
          value={step.sample_window_seconds ?? 5}
          onChange={(e) =>
            onChange({ ...step, sample_window_seconds: Number(e.target.value) || 0 })
          }
          disabled={disabled}
          className={inputCls}
        />
      </Field>
      <OnFailureField
        value={step.on_failure}
        onChange={(v) => onChange({ ...step, on_failure: v })}
        disabled={disabled}
      />
    </Grid>
  );
}

function PowerFields({
  step,
  onChange,
  disabled,
}: {
  step: PowerStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  return (
    <Grid>
      <TargetField
        target={step.target}
        onChange={(target) => onChange({ ...step, target })}
        disabled={disabled}
      />
      <Field label="Timeout (seconds)">
        <input
          type="number"
          min={0}
          value={step.timeout_seconds ?? 120}
          onChange={(e) =>
            onChange({ ...step, timeout_seconds: Number(e.target.value) || 0 })
          }
          disabled={disabled}
          className={inputCls}
        />
      </Field>
      <OnFailureField
        value={step.on_failure}
        onChange={(v) => onChange({ ...step, on_failure: v })}
        disabled={disabled}
      />
    </Grid>
  );
}

function DeviceCommandFields({
  step,
  onChange,
  disabled,
}: {
  step: DeviceCommandStep;
  onChange: (s: Step) => void;
  disabled?: boolean;
}) {
  const { devices, adapters } = useRoutineContext();

  // Sourcing rules for the command dropdown, in priority order:
  //   * specific device → its own capabilities (or its adapter's catalogue
  //     entry if capabilities aren't reported yet).
  //   * device type     → union of commands across every adapter that
  //     serves that type.
  //   * whole room      → union across every adapter, since a room step
  //     can hit any device type at once.
  const commandOptions = useMemo<string[]>(() => {
    const t = step.target;
    if ("device_id" in t && t.device_id) {
      const dev = devices.find((d) => d.id === t.device_id);
      if (dev) return commandsForDevice(dev, adapters);
      return [];
    }
    if ("device_type" in t && t.device_type) {
      return commandsForDeviceType(t.device_type, adapters);
    }
    // Whole-room fallback: dedup across every adapter.
    const set = new Set<string>();
    for (const a of adapters) for (const c of a.commands ?? []) set.add(c);
    return Array.from(set).sort();
  }, [step.target, devices, adapters]);

  // If the current command isn't in the option list (legacy step, or
  // switched target after picking), keep it in the dropdown so it's not
  // silently wiped — it renders as an italic "custom" entry.
  const commandInList = commandOptions.includes(step.command);

  return (
    <Grid>
      <TargetField
        target={step.target}
        onChange={(target) => onChange({ ...step, target })}
        disabled={disabled}
      />
      <Field
        label="Command"
        hint={
          commandOptions.length === 0
            ? "No adapter-declared commands for this target — pick one or use JSON mode for a raw command."
            : "From the target device's adapter catalogue."
        }
      >
        <select
          value={step.command}
          onChange={(e) => onChange({ ...step, command: e.target.value })}
          disabled={disabled}
          className={inputCls}
        >
          {!step.command && <option value="">Choose a command…</option>}
          {!commandInList && step.command && (
            <option value={step.command}>{step.command} (custom)</option>
          )}
          {commandOptions.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Parameters (JSON)" full hint="Optional. Passed to the adapter verbatim.">
        <JsonField
          value={step.parameters}
          onChange={(v) => onChange({ ...step, parameters: v })}
          disabled={disabled}
          placeholder='{"uri": "sip:room@example"}'
        />
      </Field>
      <Field label="Expected result (JSON)" full hint="Optional. Compared against the command response.">
        <JsonField
          value={step.expected}
          onChange={(v) => onChange({ ...step, expected: v })}
          disabled={disabled}
          placeholder='{"call_state": "connected"}'
        />
      </Field>
      <Field label="Timeout (seconds)">
        <input
          type="number"
          min={0}
          value={step.timeout_seconds ?? 30}
          onChange={(e) =>
            onChange({ ...step, timeout_seconds: Number(e.target.value) || 0 })
          }
          disabled={disabled}
          className={inputCls}
        />
      </Field>
      <OnFailureField
        value={step.on_failure}
        onChange={(v) => onChange({ ...step, on_failure: v })}
        disabled={disabled}
      />
    </Grid>
  );
}

// ── JSON textarea helper ────────────────────────────────────────────────

// JsonField — small textarea for the parameters/expected objects on
// device_command. Kept intentionally minimal: two-line box, red border
// when the JSON is malformed, empty box → undefined value. We could
// build a full key/value grid later; for now this covers the shapes
// operators actually paste in.
function JsonField({
  value,
  onChange,
  disabled,
  placeholder,
}: {
  value: Record<string, unknown> | undefined;
  onChange: (v: Record<string, unknown> | undefined) => void;
  disabled?: boolean;
  placeholder?: string;
}) {
  const [text, setText] = useState<string>(
    value ? JSON.stringify(value, null, 2) : ""
  );
  const [err, setErr] = useState<string | null>(null);

  const handleChange = (t: string) => {
    setText(t);
    const trimmed = t.trim();
    if (trimmed === "") {
      setErr(null);
      onChange(undefined);
      return;
    }
    try {
      const parsed = JSON.parse(trimmed);
      if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
        setErr("Must be a JSON object.");
        return;
      }
      setErr(null);
      onChange(parsed as Record<string, unknown>);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <>
      <textarea
        value={text}
        onChange={(e) => handleChange(e.target.value)}
        disabled={disabled}
        placeholder={placeholder}
        rows={3}
        spellCheck={false}
        className={`${inputCls} font-mono text-xs ${err ? "border-[color:hsl(var(--destructive))]" : ""}`}
      />
      {err && <div className="text-[11px] [color:hsl(var(--destructive))]">{err}</div>}
    </>
  );
}
