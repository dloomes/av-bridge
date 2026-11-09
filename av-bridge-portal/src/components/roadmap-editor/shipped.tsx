"use client";

import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Roadmap } from "@/lib/types";
import { ErrorBox, LIMITS, MoveButtons, SaveLabel, inputCls, moveInList, useDraft, useSave } from "./shared";

export function ShippedSection({ roadmap, onChange }: { roadmap: Roadmap; onChange: (r: Roadmap) => void }) {
  const { draft, setDraft, dirty } = useDraft(
    roadmap.shipped.map((s) => ({ id: s.id, when: s.when, title: s.title }))
  );
  const { busy, error, run } = useSave();
  const patch = (i: number, v: Partial<(typeof draft)[number]>) =>
    setDraft(draft.map((s, j) => (j === i ? { ...s, ...v } : s)));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const r = await run(() => api.saveRoadmapShipped(draft));
    if (r) onChange(r);
  };

  return (
    <Card>
      <CardContent className="p-4">
        <h2 className="mb-3 text-base font-semibold">Recently shipped</h2>
        <form onSubmit={submit} className="flex flex-col gap-3">
          {draft.length === 0 && <p className="text-sm text-muted-foreground">Nothing listed.</p>}
          {draft.map((s, i) => (
            <div key={s.id || `new-${i}`} className="flex flex-wrap items-center gap-2">
              <input aria-label={`Shipped ${i + 1} date`} placeholder="When, e.g. Oct 2026"
                className={`${inputCls} w-40`} maxLength={LIMITS.shippedWhen} required value={s.when}
                disabled={busy} onChange={(e) => patch(i, { when: e.target.value })} />
              <input aria-label={`Shipped ${i + 1} title`} placeholder="What shipped"
                className={`${inputCls} min-w-48 flex-1`} maxLength={LIMITS.shippedTitle} required
                value={s.title} disabled={busy} onChange={(e) => patch(i, { title: e.target.value })} />
              <div className="flex items-center">
                <MoveButtons index={i} count={draft.length} disabled={busy} what={`shipped entry ${i + 1}`}
                  onMove={(d) => setDraft(moveInList(draft, i, d))} />
                <Button type="button" size="icon" variant="ghost" aria-label={`Remove shipped entry ${i + 1}`}
                  disabled={busy} onClick={() => setDraft(draft.filter((_, j) => j !== i))}>
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          ))}
          <ErrorBox message={error} />
          <div className="flex items-center justify-between">
            <Button type="button" size="sm" variant="outline" disabled={busy}
              onClick={() => setDraft([...draft, { id: "", when: "", title: "" }])}>
              <Plus className="h-3.5 w-3.5" />
              Add entry
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
