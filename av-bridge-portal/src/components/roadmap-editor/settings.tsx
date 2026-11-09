"use client";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Roadmap } from "@/lib/types";
import { ErrorBox, Field, LIMITS, SaveLabel, inputCls, textareaCls, useDraft, useSave } from "./shared";

export function SettingsSection({
  roadmap,
  onChange,
}: {
  roadmap: Roadmap;
  onChange: (r: Roadmap) => void;
}) {
  const s = roadmap.settings;
  const { draft, setDraft, dirty } = useDraft({
    title: s.title,
    headline: s.headline,
    lede: s.lede,
    disclaimer: s.disclaimer,
    as_of: s.as_of,
  });
  const { busy, error, run } = useSave();
  const set = (k: keyof typeof draft, v: string) => setDraft({ ...draft, [k]: v });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const r = await run(() => api.saveRoadmapSettings(draft));
    if (r) onChange(r);
  };

  return (
    <Card>
      <CardContent className="p-4">
        <h2 className="mb-3 text-base font-semibold">Settings</h2>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Field label="Page title" htmlFor="rm-title">
            <input id="rm-title" className={inputCls} maxLength={LIMITS.settingsTitle} required
              value={draft.title} disabled={busy} onChange={(e) => set("title", e.target.value)} />
          </Field>
          <Field label="Headline" htmlFor="rm-headline" hint={`${draft.headline.length}/${LIMITS.headline}`}>
            <input id="rm-headline" className={inputCls} maxLength={LIMITS.headline} required
              value={draft.headline} disabled={busy} onChange={(e) => set("headline", e.target.value)} />
          </Field>
          <Field label="Introduction" htmlFor="rm-lede" hint={`${draft.lede.length}/${LIMITS.lede}`}>
            <textarea id="rm-lede" rows={3} className={textareaCls} maxLength={LIMITS.lede} required
              value={draft.lede} disabled={busy} onChange={(e) => set("lede", e.target.value)} />
          </Field>
          <Field label="As of" htmlFor="rm-asof">
            <input id="rm-asof" type="date" className={`${inputCls} sm:w-48`} required
              value={draft.as_of} disabled={busy} onChange={(e) => set("as_of", e.target.value)} />
          </Field>
          <Field label="Disclaimer" htmlFor="rm-disclaimer" hint={`${draft.disclaimer.length}/${LIMITS.disclaimer}`}>
            <textarea id="rm-disclaimer" rows={3} className={textareaCls} maxLength={LIMITS.disclaimer} required
              value={draft.disclaimer} disabled={busy} onChange={(e) => set("disclaimer", e.target.value)} />
          </Field>
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
