"use client";

import { Check } from "lucide-react";
import type { RoomTag } from "@/lib/api";
import { cn } from "@/lib/utils";

// Room tags: the chip that shows one, its colour palette, and the picker
// that applies them to a room. The palette's keys must match the API's
// (roomTagColours in av-bridge-cloud/internal/portalapi/room_tags.go).

export const TAG_COLOURS: Record<string, { chip: string; dot: string; label: string }> = {
  slate: { label: "Grey", dot: "bg-slate-500", chip: "bg-slate-500/10 text-slate-700 border-slate-500/30 dark:text-slate-300" },
  red: { label: "Red", dot: "bg-red-500", chip: "bg-red-500/10 text-red-700 border-red-500/30 dark:text-red-300" },
  orange: { label: "Orange", dot: "bg-orange-500", chip: "bg-orange-500/10 text-orange-700 border-orange-500/30 dark:text-orange-300" },
  amber: { label: "Amber", dot: "bg-amber-500", chip: "bg-amber-500/10 text-amber-800 border-amber-500/30 dark:text-amber-300" },
  green: { label: "Green", dot: "bg-green-500", chip: "bg-green-500/10 text-green-700 border-green-500/30 dark:text-green-300" },
  teal: { label: "Teal", dot: "bg-teal-500", chip: "bg-teal-500/10 text-teal-700 border-teal-500/30 dark:text-teal-300" },
  blue: { label: "Blue", dot: "bg-blue-500", chip: "bg-blue-500/10 text-blue-700 border-blue-500/30 dark:text-blue-300" },
  indigo: { label: "Indigo", dot: "bg-indigo-500", chip: "bg-indigo-500/10 text-indigo-700 border-indigo-500/30 dark:text-indigo-300" },
  purple: { label: "Purple", dot: "bg-purple-500", chip: "bg-purple-500/10 text-purple-700 border-purple-500/30 dark:text-purple-300" },
  pink: { label: "Pink", dot: "bg-pink-500", chip: "bg-pink-500/10 text-pink-700 border-pink-500/30 dark:text-pink-300" },
};

export function tagColour(colour: string | undefined) {
  return TAG_COLOURS[colour ?? ""] ?? TAG_COLOURS.slate;
}

export function TagChip({ tag, className }: { tag: Pick<RoomTag, "name" | "colour" | "description">; className?: string }) {
  return (
    <span
      title={tag.description || undefined}
      className={cn(
        "inline-flex max-w-[12rem] items-center truncate rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4",
        tagColour(tag.colour).chip,
        className
      )}
    >
      {tag.name}
    </span>
  );
}

// RoomTagChips shows a room's tags, in the tag list's order.
export function RoomTagChips({ tagIDs, tags }: { tagIDs?: string[]; tags: RoomTag[] }) {
  if (!tagIDs?.length) return null;
  const mine = tags.filter((t) => tagIDs.includes(t.id));
  if (mine.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {mine.map((t) => (
        <TagChip key={t.id} tag={t} />
      ))}
    </span>
  );
}

// TagPicker: every tag as a toggle, the selected ones ticked.
export function TagPicker({
  tags,
  selected,
  onChange,
  disabled,
}: {
  tags: RoomTag[];
  selected: string[];
  onChange: (ids: string[]) => void;
  disabled?: boolean;
}) {
  if (tags.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No tags yet. Create them on the Room tags page.
      </p>
    );
  }
  const toggle = (id: string) =>
    onChange(selected.includes(id) ? selected.filter((s) => s !== id) : [...selected, id]);
  return (
    <ul className="max-h-72 space-y-1 overflow-y-auto" role="list">
      {tags.map((t) => {
        const on = selected.includes(t.id);
        return (
          <li key={t.id}>
            <button
              type="button"
              role="checkbox"
              aria-checked={on}
              disabled={disabled}
              onClick={() => toggle(t.id)}
              className={cn(
                "flex w-full items-center gap-2.5 rounded-md border px-3 py-2 text-left text-sm transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
                on && "border-primary/40 bg-primary/5"
              )}
            >
              <span
                className={cn(
                  "flex h-4 w-4 shrink-0 items-center justify-center rounded border",
                  on ? "border-primary bg-primary text-primary-foreground" : "border-input"
                )}
              >
                {on && <Check aria-hidden="true" className="h-3 w-3" />}
              </span>
              <span className={cn("h-2.5 w-2.5 shrink-0 rounded-full", tagColour(t.colour).dot)} />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{t.name}</span>
                {t.description && (
                  <span className="block truncate text-xs text-muted-foreground">{t.description}</span>
                )}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

// TagFilter: a select of tags for filtering lists; "" means any.
export function TagFilter({
  tags,
  value,
  onChange,
  className,
}: {
  tags: RoomTag[];
  value: string;
  onChange: (id: string) => void;
  className?: string;
}) {
  if (tags.length === 0) return null;
  return (
    <select
      aria-label="Filter by room tag"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={cn(
        "rounded-md border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className
      )}
    >
      <option value="">All room tags</option>
      {tags.map((t) => (
        <option key={t.id} value={t.id}>
          {t.name}
        </option>
      ))}
    </select>
  );
}
