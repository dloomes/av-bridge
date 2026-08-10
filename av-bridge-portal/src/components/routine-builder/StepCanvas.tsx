"use client";

import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  ChevronRight,
  Clock,
  Copy,
  GitFork,
  GripVertical,
  Play,
  Power,
  PowerOff,
  Radio,
  Signal,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { StepEditor } from "./StepEditor";
import {
  STEP_TYPE_LABEL,
  type Step,
  type StepType,
  type UIStep,
} from "./step-types";

// StepCanvas — ordered, drag-reorderable list of step cards.
//
// @dnd-kit choices:
//   * PointerSensor with 5px activation distance so a click on the
//     card body doesn't accidentally start a drag.
//   * KeyboardSensor so keyboard users can reorder (Tab to focus a
//     card, Space to grab, arrow keys to move, Space to drop).
//   * closestCenter collision detection — better than closestCorners
//     for vertical lists where cards are similar height.

const STEP_ICON: Record<StepType, React.ComponentType<{ className?: string }>> = {
  section: GitFork,
  wait: Clock,
  power_on: Power,
  power_off: PowerOff,
  device_command: Play,
  expect_status: Signal,
  check_metric: Radio,
};

interface StepCanvasProps {
  steps: UIStep[];
  disabled?: boolean;
  onStepsChange: (next: UIStep[]) => void;
  onUpdateStep: (uiId: string, next: Step) => void;
  onRemove: (uiId: string) => void;
  onDuplicate: (uiId: string) => void;
}

export function StepCanvas({
  steps,
  disabled,
  onStepsChange,
  onUpdateStep,
  onRemove,
  onDuplicate,
}: StepCanvasProps) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = steps.findIndex((s) => s._uiId === active.id);
    const newIndex = steps.findIndex((s) => s._uiId === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    onStepsChange(arrayMove(steps, oldIndex, newIndex));
  };

  if (steps.length === 0) {
    return (
      <Card>
        <CardContent className="p-10 text-center space-y-2">
          <div className="mx-auto h-10 w-10 rounded-md bg-muted flex items-center justify-center">
            <Play aria-hidden="true" className="h-5 w-5 text-muted-foreground" />
          </div>
          <div className="font-medium">No steps yet</div>
          <div className="text-sm text-muted-foreground">
            Add one from the palette on the right.
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={handleDragEnd}
    >
      <SortableContext
        items={steps.map((s) => s._uiId)}
        strategy={verticalListSortingStrategy}
        disabled={disabled}
      >
        <div className="space-y-2">
          {steps.map((s, i) => (
            <SortableStepCard
              key={s._uiId}
              index={i}
              uiStep={s}
              disabled={disabled}
              onUpdate={(next) => onUpdateStep(s._uiId, next)}
              onRemove={() => onRemove(s._uiId)}
              onDuplicate={() => onDuplicate(s._uiId)}
            />
          ))}
        </div>
      </SortableContext>
    </DndContext>
  );
}

// ── Sortable card ────────────────────────────────────────────────────────

interface SortableStepCardProps {
  index: number;
  uiStep: UIStep;
  disabled?: boolean;
  onUpdate: (next: Step) => void;
  onRemove: () => void;
  onDuplicate: () => void;
}

function SortableStepCard({
  index,
  uiStep,
  disabled,
  onUpdate,
  onRemove,
  onDuplicate,
}: SortableStepCardProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: uiStep._uiId, disabled });

  const [expanded, setExpanded] = useState(false);

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : undefined,
    zIndex: isDragging ? 10 : undefined,
  };

  const Icon = STEP_ICON[uiStep.step.type] ?? Play;

  return (
    <div ref={setNodeRef} style={style}>
      <Card className={isDragging ? "ring-2 ring-primary/40" : ""}>
        <CardContent className="p-3">
          <div className="flex items-center gap-3">
            <button
              type="button"
              className="h-6 w-6 shrink-0 rounded flex items-center justify-center text-muted-foreground/60 hover:text-foreground hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 cursor-grab active:cursor-grabbing focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label={`Drag to reorder step ${index + 1}`}
              disabled={disabled}
              {...attributes}
              {...listeners}
            >
              <GripVertical aria-hidden="true" className="h-4 w-4" />
            </button>
            <div className="text-xs text-muted-foreground font-mono w-6 text-right shrink-0">
              {index + 1}
            </div>
            <div className="h-7 w-7 rounded-md bg-primary/10 flex items-center justify-center shrink-0">
              <Icon aria-hidden="true" className="h-3.5 w-3.5 text-primary" />
            </div>
            <button
              type="button"
              onClick={() => setExpanded((v) => !v)}
              className="min-w-0 flex-1 text-left focus-visible:outline-none focus-visible:underline focus-visible:decoration-dotted"
              aria-expanded={expanded}
            >
              <div className="text-sm font-medium leading-tight truncate">
                {uiStep.step.name || STEP_TYPE_LABEL[uiStep.step.type]}
              </div>
              <div className="text-xs text-muted-foreground leading-tight mt-0.5 truncate">
                {STEP_TYPE_LABEL[uiStep.step.type]} · {summarise(uiStep.step)}
              </div>
            </button>
            <div className="flex items-center gap-0.5 shrink-0">
              <Button
                variant="ghost"
                size="sm"
                className="h-8 w-8 p-0"
                disabled={disabled}
                onClick={onDuplicate}
                aria-label="Duplicate step"
              >
                <Copy aria-hidden="true" className="h-3.5 w-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-8 w-8 p-0"
                disabled={disabled}
                onClick={onRemove}
                aria-label="Delete step"
              >
                <Trash2 aria-hidden="true" className="h-3.5 w-3.5" />
              </Button>
              <button
                type="button"
                onClick={() => setExpanded((v) => !v)}
                className="h-8 w-8 p-0 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted flex items-center justify-center focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={expanded ? "Collapse step" : "Expand step"}
              >
                <ChevronRight
                  aria-hidden="true"
                  className={`h-4 w-4 transition-transform ${expanded ? "rotate-90" : ""}`}
                />
              </button>
            </div>
          </div>

          {expanded && (
            <div className="mt-3 border-t pt-3">
              <StepEditor
                step={uiStep.step}
                onChange={onUpdate}
                disabled={disabled}
              />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function summarise(step: Step): string {
  switch (step.type) {
    case "wait":
      return `${step.duration_seconds}s`;
    case "section":
      return step.label || "(unnamed section)";
    case "expect_status":
      return `expect ${step.status} on ${targetSummary(step.target)}`;
    case "check_metric":
      return `${step.metric || "?"} ${step.operator} ${step.threshold} on ${targetSummary(step.target)}`;
    case "power_on":
      return `power on ${targetSummary(step.target)}`;
    case "power_off":
      return `power off ${targetSummary(step.target)}`;
    case "device_command":
      return step.command
        ? `${step.command} on ${targetSummary(step.target)}`
        : "(no command)";
  }
}

function targetSummary(
  t: { scope?: string; device_type?: string; device_id?: string } | undefined
): string {
  if (!t) return "room";
  if ("device_id" in t && t.device_id) return `device ${t.device_id.slice(0, 8)}`;
  if ("device_type" in t && t.device_type) return `type=${t.device_type}`;
  return "room";
}
