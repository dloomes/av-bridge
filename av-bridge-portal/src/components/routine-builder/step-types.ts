// step-types.ts — client-side type model for Room Readiness routine steps.
//
// Mirrors the schema the executor at av-bridge-cloud/internal/nightly/executor.go
// unmarshals from each step's JSON. The wire shape is intentionally loose
// (steps are stored as opaque jsonb) — this file adds the typing the DnD
// builder needs to render forms + validate before save.
//
// Client-side we wrap each step with a stable `_uiId` for @dnd-kit's
// sortable context. That id is stripped in `serializeSteps()` so the
// wire payload stays clean and future schema-strict validators don't
// choke on unknown keys.

// ── Discriminator ───────────────────────────────────────────────────────

export type StepType =
  | "wait"
  | "section"
  | "expect_status"
  | "check_metric"
  | "power_on"
  | "power_off"
  | "device_command";

export const STEP_TYPE_LABEL: Record<StepType, string> = {
  wait: "Wait",
  section: "Section",
  expect_status: "Expect status",
  check_metric: "Check metric",
  power_on: "Power on",
  power_off: "Power off",
  device_command: "Device command",
};

export const STEP_TYPE_SUBLABEL: Record<StepType, string> = {
  wait: "Pause for a fixed duration",
  section: "Structural heading, always passes",
  expect_status: "Assert devices report a given status",
  check_metric: "Compare a telemetry metric to a threshold",
  power_on: "Send power-on to matching devices",
  power_off: "Send power-off to matching devices",
  device_command: "Send a named command with optional args",
};

// ── Target ──────────────────────────────────────────────────────────────

export type Target =
  | { scope: "room" }
  | { device_type: string }
  | { device_id: string };

export type TargetKind = "room" | "device_type" | "device_id";

export function targetKind(t: Target | undefined): TargetKind {
  if (!t) return "room";
  if ("device_id" in t && t.device_id) return "device_id";
  if ("device_type" in t && t.device_type) return "device_type";
  return "room";
}

// ── on_failure ──────────────────────────────────────────────────────────

// "abort" halts the run, "continue" records the failure but keeps
// going, "retry:N" re-attempts up to N times before giving up. Stored
// as a string on the wire so future modes can be added without a
// schema migration.
export type OnFailure = "abort" | "continue" | `retry:${number}`;

export const ON_FAILURE_OPTIONS = ["abort", "continue", "retry:1", "retry:3"] as const;

// ── Per-type step shapes ────────────────────────────────────────────────

export interface StepCommon {
  name?: string;
  type: StepType;
}

export interface WaitStep extends StepCommon {
  type: "wait";
  duration_seconds: number;
}

export interface SectionStep extends StepCommon {
  type: "section";
  label: string;
}

export interface ExpectStatusStep extends StepCommon {
  type: "expect_status";
  target: Target;
  status: "online" | "offline" | "degraded";
  on_failure?: OnFailure;
}

export type MetricOperator = "gt" | "lt" | "gte" | "lte" | "eq";

export interface CheckMetricStep extends StepCommon {
  type: "check_metric";
  target: Target;
  metric: string;
  operator: MetricOperator;
  threshold: number;
  sample_window_seconds?: number;
  on_failure?: OnFailure;
}

export interface PowerStep extends StepCommon {
  type: "power_on" | "power_off";
  target: Target;
  timeout_seconds?: number;
  on_failure?: OnFailure;
}

export interface DeviceCommandStep extends StepCommon {
  type: "device_command";
  target: Target;
  command: string;
  parameters?: Record<string, unknown>;
  expected?: Record<string, unknown>;
  timeout_seconds?: number;
  on_failure?: OnFailure;
}

export type Step =
  | WaitStep
  | SectionStep
  | ExpectStatusStep
  | CheckMetricStep
  | PowerStep
  | DeviceCommandStep;

// ── UI wrapper ──────────────────────────────────────────────────────────

// UIStep carries an opaque client-side id (used by @dnd-kit for sort
// stability) alongside the wire step. `serializeSteps` strips the id.
export interface UIStep {
  _uiId: string;
  step: Step;
}

// ── Defaults ────────────────────────────────────────────────────────────

// newStepOfType returns a fresh step with sane defaults so a drop from
// the palette lands as a valid, editable card without further clicks.
export function newStepOfType(t: StepType): Step {
  switch (t) {
    case "wait":
      return { type: "wait", duration_seconds: 30 };
    case "section":
      return { type: "section", label: "New section" };
    case "expect_status":
      return {
        type: "expect_status",
        target: { scope: "room" },
        status: "online",
        on_failure: "abort",
      };
    case "check_metric":
      return {
        type: "check_metric",
        target: { scope: "room" },
        metric: "",
        operator: "gt",
        threshold: 0,
        sample_window_seconds: 5,
        on_failure: "continue",
      };
    case "power_on":
    case "power_off":
      return {
        type: t,
        target: { scope: "room" },
        timeout_seconds: 120,
        on_failure: "abort",
      };
    case "device_command":
      return {
        type: "device_command",
        target: { scope: "room" },
        command: "",
        timeout_seconds: 30,
        on_failure: "abort",
      };
  }
}

// ── Load / save conversion ──────────────────────────────────────────────

export function hydrateSteps(raw: unknown[]): UIStep[] {
  return (raw ?? []).map((s, i) => ({
    _uiId: cryptoRandomId() ?? `s${i}`,
    step: s as Step,
  }));
}

export function serializeSteps(ui: UIStep[]): Step[] {
  return ui.map((u) => u.step);
}

function cryptoRandomId(): string | undefined {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `s-${Math.random().toString(36).slice(2, 10)}`;
}

// ── Capability gating ───────────────────────────────────────────────────

export interface DeviceCapabilities {
  power: { on: boolean; off: boolean };
  commands?: string[];
  metrics?: string[];
}

// canDeviceDo — used by the palette to grey out step types that no
// device in the picked room supports. Structural steps (wait, section)
// always pass; power/command/metric steps consult the capability
// declaration each adapter publishes on telemetry.
export function canDeviceDo(
  cap: DeviceCapabilities | undefined,
  stepType: StepType
): boolean {
  if (!cap) return false;
  switch (stepType) {
    case "wait":
    case "section":
      return true;
    case "expect_status":
      return true;
    case "check_metric":
      return (cap.metrics?.length ?? 0) > 0;
    case "power_on":
      return !!cap.power?.on;
    case "power_off":
      return !!cap.power?.off;
    case "device_command":
      return (cap.commands?.length ?? 0) > 0;
  }
}
