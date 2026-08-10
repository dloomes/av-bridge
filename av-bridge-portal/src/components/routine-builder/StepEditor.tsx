"use client";

import { useState } from "react";
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
  const kind = targetKind(target);
  const setKind = (k: TargetKind) => {
    if (k === "room") onChange({ scope: "room" });
    else if (k === "device_type") onChange({ device_type: "" });
    else onChange({ device_id: "" });
  };
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
          <input
            type="text"
            value={"device_type" in target ? target.device_type : ""}
            placeholder="e.g. display, vc, dsp"
            onChange={(e) => onChange({ device_type: e.target.value })}
            disabled={disabled}
            className={inputCls}
          />
        </Field>
      )}
      {kind === "device_id" && (
        <Field label="Device ID">
          <input
            type="text"
            value={"device_id" in target ? target.device_id : ""}
            placeholder="uuid"
            onChange={(e) => onChange({ device_id: e.target.value })}
            disabled={disabled}
            className={inputCls}
          />
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
  return (
    <Grid>
      <TargetField
        target={step.target}
        onChange={(target) => onChange({ ...step, target })}
        disabled={disabled}
      />
      <Field label="Command" hint="Must match an adapter-declared command.">
        <input
          type="text"
          value={step.command}
          placeholder="e.g. dial, power_on, recall_preset"
          onChange={(e) => onChange({ ...step, command: e.target.value })}
          disabled={disabled}
          className={inputCls}
        />
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
