"use client";

import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Roadmap } from "@/lib/types";
import {
  ErrorBox, LIMITS, MoveButtons, SaveLabel, inputCls, moveInList, useDraft, useSave,
} from "./shared";

// Periods are fixed slots (exactly four). Items belong to a slot, so the
// slots can be renamed and one marked current but not reordered.
export function PeriodsSection({ roadmap, onChange }: { roadmap: Roadmap; onChange: (r: Roadmap) => void }) {
  const { draft, setDraft, dirty } = useDraft(
    roadmap.periods.map((p) => ({ name: p.name, months: p.months, is_current: p.is_current }))
  );
  const { busy, error, run } = useSave();
  const patch = (i: number, v: Partial<(typeof draft)[number]>) =>
    setDraft(draft.map((p, j) => (j === i ? { ...p, ...v } : p)));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const r = await run(() => api.saveRoadmapPeriods(draft));
    if (r) onChange(r);
  };

  return (
    <Card>
      <CardContent className="p-4">
        <h2 className="mb-1 text-base font-semibold">Periods</h2>
        <p className="mb-3 text-xs text-muted-foreground">
          Four periods, left to right. Choose which one is current.
        </p>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <fieldset className="flex flex-col gap-2">
            <legend className="sr-only">Current period</legend>
            {draft.map((p, i) => (
              <div key={i} className="grid grid-cols-[auto_1fr] items-center gap-2 sm:grid-cols-[auto_1fr_1fr]">
                <input
                  type="radio"
                  name="rm-current-period"
                  aria-label={`Period ${i + 1} is the current period`}
                  className="h-4 w-4"
                  checked={p.is_current}
                  disabled={busy}
                  onChange={() => setDraft(draft.map((q, j) => ({ ...q, is_current: j === i })))}
                />
                <input aria-label={`Period ${i + 1} name`} className={inputCls} maxLength={LIMITS.periodName}
                  required value={p.name} disabled={busy} onChange={(e) => patch(i, { name: e.target.value })} />
                <input aria-label={`Period ${i + 1} months`} className={`${inputCls} col-span-2 sm:col-span-1`}
                  maxLength={LIMITS.periodMonths} required value={p.months} disabled={busy}
                  onChange={(e) => patch(i, { months: e.target.value })} />
              </div>
            ))}
          </fieldset>
          <ErrorBox message={error} />
          <div className="flex justify-end">
            <Button type="submit" disabled={busy || !dirty}>
              <SaveLabel busy={busy} />
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

export function ThemesSection({ roadmap, onChange }: { roadmap: Roadmap; onChange: (r: Roadmap) => void }) {
  const { draft, setDraft, dirty } = useDraft(
    roadmap.themes.map((t) => ({ id: t.id, name: t.name, note: t.note }))
  );
  const { busy, error, run } = useSave();
  const patch = (i: number, v: Partial<(typeof draft)[number]>) =>
    setDraft(draft.map((t, j) => (j === i ? { ...t, ...v } : t)));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const r = await run(() => api.saveRoadmapThemes(draft));
    if (r) onChange(r);
  };

  return (
    <Card>
      <CardContent className="p-4">
        <h2 className="mb-1 text-base font-semibold">Themes</h2>
        <p className="mb-3 text-xs text-muted-foreground">
          The rows of the roadmap. A theme can only be removed once it has no items.
        </p>
        <form onSubmit={submit} className="flex flex-col gap-3">
          {draft.map((t, i) => (
            <div key={t.id || `new-${i}`} className="flex flex-wrap items-center gap-2">
              <input aria-label={`Theme ${i + 1} name`} placeholder="Name" className={`${inputCls} min-w-40 flex-1`}
                maxLength={LIMITS.themeName} required value={t.name} disabled={busy}
                onChange={(e) => patch(i, { name: e.target.value })} />
              <input aria-label={`Theme ${i + 1} note`} placeholder="Note (optional)"
                className={`${inputCls} min-w-40 flex-[2]`} maxLength={LIMITS.themeNote} value={t.note}
                disabled={busy} onChange={(e) => patch(i, { note: e.target.value })} />
              <div className="flex items-center">
                <MoveButtons index={i} count={draft.length} disabled={busy} what={`theme ${t.name || i + 1}`}
                  onMove={(d) => setDraft(moveInList(draft, i, d))} />
                <Button type="button" size="icon" variant="ghost" aria-label={`Remove theme ${t.name || i + 1}`}
                  disabled={busy || draft.length === 1} onClick={() => setDraft(draft.filter((_, j) => j !== i))}>
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          ))}
          <ErrorBox message={error} />
          <div className="flex items-center justify-between">
            <Button type="button" size="sm" variant="outline" disabled={busy}
              onClick={() => setDraft([...draft, { id: "", name: "", note: "" }])}>
              <Plus className="h-3.5 w-3.5" />
              Add theme
            </Button>
            <Button type="submit" disabled={busy || !dirty}>
              <SaveLabel busy={busy} />
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
