"use client";

// The dashboard grid: widgets in a responsive 12-column grid, sized by
// preset, reorderable by drag (or keyboard) while editing. Used by the
// Dashboards page and the wallboard view (app/display).

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
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical, Settings2, X } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { SIZE_CLASS, SIZE_LABEL, WIDGET_BY_TYPE, type WidgetConfig } from "@/components/dashboard/widgets";
import type { DashboardWidget, DashboardWidgetSize } from "@/lib/api";
import { cn } from "@/lib/utils";

// ── Grid ─────────────────────────────────────────────────────────────────

export function DashboardGrid({
  widgets,
  editing,
  onChange,
  onSettings,
  tall,
}: {
  widgets: DashboardWidget[];
  editing: boolean;
  onChange: (next: DashboardWidget[]) => void;
  onSettings: (id: string) => void;
  // Wallboards: no inner scrolling, widgets grow to fit.
  tall?: boolean;
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  );
  const onDragEnd = (e: DragEndEvent) => {
    if (!e.over || e.active.id === e.over.id) return;
    const from = widgets.findIndex((w) => w.id === e.active.id);
    const to = widgets.findIndex((w) => w.id === e.over!.id);
    if (from >= 0 && to >= 0) onChange(arrayMove(widgets, from, to));
  };
  const update = (id: string, patch: Partial<DashboardWidget>) =>
    onChange(widgets.map((w) => (w.id === id ? { ...w, ...patch } : w)));

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={widgets.map((w) => w.id)} strategy={rectSortingStrategy} disabled={!editing}>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-12">
          {widgets.map((w) => (
            <WidgetCard
              key={w.id}
              widget={w}
              editing={editing}
              onSize={(size) => update(w.id, { size })}
              onRemove={() => onChange(widgets.filter((x) => x.id !== w.id))}
              onSettings={() => onSettings(w.id)}
              tall={tall}
            />
          ))}
        </div>
      </SortableContext>
    </DndContext>
  );
}

function WidgetCard({
  widget,
  editing,
  onSize,
  onRemove,
  onSettings,
  tall,
}: {
  widget: DashboardWidget;
  editing: boolean;
  onSize: (s: DashboardWidgetSize) => void;
  onRemove: () => void;
  onSettings: () => void;
  tall?: boolean;
}) {
  const def = WIDGET_BY_TYPE[widget.type];
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: widget.id,
    disabled: !editing,
  });
  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    zIndex: isDragging ? 20 : undefined,
  };
  const config = (widget.config ?? {}) as WidgetConfig;
  const title = (def?.heading?.(config)) || def?.title || widget.type;
  const Icon = def?.icon;

  return (
    <div ref={setNodeRef} style={style} className={cn("col-span-1", SIZE_CLASS[widget.size], isDragging && "opacity-70")}>
      <Card className={cn("flex h-full flex-col", editing && "ring-1 ring-dashed ring-border")}>
        <div className="flex items-center gap-2 border-b px-3 py-2">
          {editing && (
            <button
              type="button"
              className="cursor-grab touch-none rounded p-0.5 text-muted-foreground hover:bg-accent/40 active:cursor-grabbing"
              aria-label={`Move ${title}`}
              {...attributes}
              {...listeners}
            >
              <GripVertical className="h-4 w-4" />
            </button>
          )}
          {Icon && <Icon className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" />}
          <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">{title}</h2>
          {editing && (
            <>
              <select
                aria-label="Widget size"
                className="h-7 rounded border border-input bg-background px-1.5 text-xs"
                value={widget.size}
                onChange={(e) => onSize(e.target.value as DashboardWidgetSize)}
              >
                {(Object.keys(SIZE_LABEL) as DashboardWidgetSize[]).map((s) => (
                  <option key={s} value={s}>{SIZE_LABEL[s]}</option>
                ))}
              </select>
              {def && def.fields.length > 0 && (
                <button type="button" onClick={onSettings} aria-label={`${title} settings`} className="rounded p-1 text-muted-foreground hover:bg-accent/40">
                  <Settings2 className="h-3.5 w-3.5" />
                </button>
              )}
              <button type="button" onClick={onRemove} aria-label={`Remove ${title}`} className="rounded p-1 text-muted-foreground hover:bg-accent/40">
                <X className="h-3.5 w-3.5" />
              </button>
            </>
          )}
        </div>
        <CardContent className={cn("flex-1 p-3", !tall && "max-h-96 overflow-y-auto")}>
          {def ? def.render(config) : <p className="text-xs text-muted-foreground">This widget isn&apos;t available.</p>}
        </CardContent>
      </Card>
    </div>
  );
}

