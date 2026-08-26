"use client";

import { useCallback } from "react";
import { StepCanvas } from "./StepCanvas";
import { StepPalette } from "./StepPalette";
import { RoutineContextProvider } from "./RoutineContext";
import type { AdapterInfo, DeviceSummary } from "@/lib/types";
import {
  hydrateSteps,
  newStepOfType,
  serializeSteps,
  type Step,
  type StepType,
  type UIStep,
} from "./step-types";

// RoutineBuilder — orchestrator that owns the ordered UIStep[] state
// and lays out canvas + palette. Kept dumb: parent controls save
// (dirty tracking, PATCH, toasts) so this component can be lifted into
// a "New routine" flow later without dragging save logic with it.
//
// Slice 1 is click-to-add + delete. Reorder-via-DnD, per-step forms,
// and capability gating layer on top in the next slices.

interface RoutineBuilderProps {
  steps: UIStep[];
  onStepsChange: (next: UIStep[]) => void;
  disabled?: boolean;
  // Devices and adapters power the target + command dropdowns inside
  // the per-step editors. Fetched once by the parent page and passed in
  // so the builder itself stays framework-agnostic (no API calls).
  devices?: DeviceSummary[];
  adapters?: AdapterInfo[];
}

export function RoutineBuilder({
  steps,
  onStepsChange,
  disabled,
  devices = [],
  adapters = [],
}: RoutineBuilderProps) {
  const handleAdd = useCallback(
    (type: StepType) => {
      const fresh: UIStep = {
        _uiId: newUiId(),
        step: newStepOfType(type),
      };
      onStepsChange([...steps, fresh]);
    },
    [steps, onStepsChange]
  );

  const handleRemove = useCallback(
    (uiId: string) => {
      onStepsChange(steps.filter((s) => s._uiId !== uiId));
    },
    [steps, onStepsChange]
  );

  const handleUpdate = useCallback(
    (uiId: string, next: Step) => {
      onStepsChange(
        steps.map((s) => (s._uiId === uiId ? { ...s, step: next } : s))
      );
    },
    [steps, onStepsChange]
  );

  const handleDuplicate = useCallback(
    (uiId: string) => {
      const idx = steps.findIndex((s) => s._uiId === uiId);
      if (idx === -1) return;
      const original = steps[idx];
      // Deep-clone via JSON so nested parameters/expected objects don't
      // share references with the source step — otherwise editing one
      // would mutate the other.
      const clone: UIStep = {
        _uiId: newUiId(),
        step: JSON.parse(JSON.stringify(original.step)) as Step,
      };
      const next = [...steps.slice(0, idx + 1), clone, ...steps.slice(idx + 1)];
      onStepsChange(next);
    },
    [steps, onStepsChange]
  );

  return (
    <RoutineContextProvider devices={devices} adapters={adapters}>
      <div className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_320px] gap-4 items-start">
        <StepCanvas
          steps={steps}
          disabled={disabled}
          onStepsChange={onStepsChange}
          onUpdateStep={handleUpdate}
          onRemove={handleRemove}
          onDuplicate={handleDuplicate}
        />
        <StepPalette disabled={disabled} onAdd={handleAdd} />
      </div>
    </RoutineContextProvider>
  );
}

// re-exported helpers so the outer page doesn't have to reach into
// step-types.ts directly.
export { hydrateSteps, serializeSteps };
export type { UIStep, Step };

// Local id generator that mirrors the one in step-types.ts. Duplicated
// intentionally so callers can construct UIStep values without a full
// import surface — the id is opaque either way.
function newUiId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `s-${Math.random().toString(36).slice(2, 10)}`;
}
