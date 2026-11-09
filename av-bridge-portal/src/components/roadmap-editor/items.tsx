"use client";

import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { api } from "@/lib/api";
import type { Roadmap, RoadmapItem, RoadmapStatus } from "@/lib/types";
import {
  ConfirmModal, ErrorBox, Field, LIMITS, MoveButtons, SaveLabel, inputCls, textareaCls, useSave,
} from "./shared";

const STATUSES: { value: RoadmapStatus; label: string }[] = [
  { value: "committed", label: "Committed" },
  { value: "planned", label: "Planned" },
  { value: "exploring", label: "Exploring" },
];

export function ItemsSection({ roadmap, onChange }: { roadmap: Roadmap; onChange: (r: Roadmap) => void }) {
  // null = closed, "new" = add form, else the item being edited.
  const [editing, setEditing] = useState<RoadmapItem | "new" | null>(null);
  const [deleting, setDeleting] = useState<RoadmapItem | null>(null);
  const move = useSave();

  const periodName = (n: number) => roadmap.periods.find((p) => p.position === n)?.name ?? `Period ${n + 1}`;

  const itemsOf = (themeId: string) =>
    roadmap.items
      .filter((i) => i.theme_id === themeId)
      .sort((a, b) => a.period - b.period || a.position - b.position);

  // Reordering works inside one theme and period: the order endpoint takes
  // that cell's full list of ids.
  const reorder = async (item: RoadmapItem, delta: -1 | 1) => {
    const cell = roadmap.items
      .filter((i) => i.theme_id === item.theme_id && i.period === item.period)
      .sort((a, b) => a.position - b.position);
    const at = cell.findIndex((i) => i.id === item.id);
    const to = at + delta;
    if (to < 0 || to >= cell.length) return;
    const ids = cell.map((i) => i.id);
    [ids[at], ids[to]] = [ids[to], ids[at]];
    const r = await move.run(() =>
      api.orderRoadmapItems({ theme_id: item.theme_id, period: item.period, ids })
    );
    if (r) onChange(r);
  };

  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold">Items</h2>
          <Button size="sm" onClick={() => setEditing("new")} disabled={roadmap.themes.length === 0}>
            <Plus className="h-3.5 w-3.5" />
            Add item
          </Button>
        </div>
        <ErrorBox message={move.error} />
        <div className="mt-3 space-y-5">
          {roadmap.themes.map((t) => {
            const items = itemsOf(t.id);
            return (
              <section key={t.id} aria-label={t.name}>
                <h3 className="mb-1 text-sm font-medium">{t.name}</h3>
                {items.length === 0 ? (
                  <p className="text-xs text-muted-foreground">No items in this theme.</p>
                ) : (
                  <div className="overflow-x-auto rounded-md border">
                    <table className="w-full min-w-[640px] text-sm">
                      <thead className="bg-muted/40 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                        <tr>
                          <th className="px-3 py-2 font-medium">Period</th>
                          <th className="px-3 py-2 font-medium">Status</th>
                          <th className="px-3 py-2 font-medium">Release</th>
                          <th className="px-3 py-2 font-medium">Title</th>
                          <th className="px-3 py-2 font-medium">Description</th>
                          <th className="px-3 py-2" />
                        </tr>
                      </thead>
                      <tbody>
                        {items.map((it) => {
                          const cell = items.filter((x) => x.period === it.period);
                          return (
                            <tr key={it.id} className="border-t align-top">
                              <td className="px-3 py-2 whitespace-nowrap">{periodName(it.period)}</td>
                              <td className="px-3 py-2 capitalize">{it.status}</td>
                              <td className="px-3 py-2 whitespace-nowrap">{it.release_month || "—"}</td>
                              <td className="px-3 py-2 font-medium">{it.title}</td>
                              <td className="px-3 py-2 text-muted-foreground">{it.description}</td>
                              <td className="px-3 py-2">
                                <div className="flex items-center justify-end whitespace-nowrap">
                                  <MoveButtons
                                    index={cell.findIndex((x) => x.id === it.id)}
                                    count={cell.length}
                                    disabled={move.busy}
                                    what={it.title}
                                    onMove={(d) => void reorder(it, d)}
                                  />
                                  <Button size="icon" variant="ghost" aria-label={`Edit ${it.title}`}
                                    onClick={() => setEditing(it)}>
                                    <Pencil className="h-3.5 w-3.5" />
                                  </Button>
                                  <Button size="icon" variant="ghost" aria-label={`Delete ${it.title}`}
                                    onClick={() => setDeleting(it)}>
                                    <Trash2 className="h-3.5 w-3.5" />
                                  </Button>
                                </div>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            );
          })}
        </div>
      </CardContent>

      <Modal
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing === "new" ? "Add roadmap item" : "Edit roadmap item"}
        wide={false}
      >
        {editing !== null && (
          <ItemForm
            roadmap={roadmap}
            item={editing === "new" ? null : editing}
            onCancel={() => setEditing(null)}
            onSaved={(r) => {
              onChange(r);
              setEditing(null);
            }}
          />
        )}
      </Modal>

      <ConfirmModal
        open={deleting !== null}
        title="Delete roadmap item"
        body={
          <p>
            Delete <span className="font-medium">{deleting?.title}</span>? It disappears from the
            roadmap straight away.
          </p>
        }
        confirmLabel="Delete"
        onCancel={() => setDeleting(null)}
        onConfirm={async () => {
          if (!deleting) return;
          onChange(await api.deleteRoadmapItem(deleting.id));
          setDeleting(null);
        }}
      />
    </Card>
  );
}

function ItemForm({
  roadmap,
  item,
  onCancel,
  onSaved,
}: {
  roadmap: Roadmap;
  item: RoadmapItem | null;
  onCancel: () => void;
  onSaved: (r: Roadmap) => void;
}) {
  const current = roadmap.periods.find((p) => p.is_current)?.position ?? 0;
  const [themeId, setThemeId] = useState(item?.theme_id ?? roadmap.themes[0]?.id ?? "");
  const [period, setPeriod] = useState(item?.period ?? current);
  const [status, setStatus] = useState<RoadmapStatus>(item?.status ?? "planned");
  const [release, setRelease] = useState(item?.release_month ?? "");
  const [title, setTitle] = useState(item?.title ?? "");
  const [description, setDescription] = useState(item?.description ?? "");
  const { busy, error, run } = useSave();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const body = {
      theme_id: themeId,
      period,
      status,
      release_month: release,
      title,
      description,
    };
    const r = await run(() =>
      item ? api.updateRoadmapItem(item.id, body) : api.createRoadmapItem(body)
    );
    if (r) onSaved(r);
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Field label="Title" htmlFor="rmi-title" hint={`${title.length}/${LIMITS.itemTitle}`}>
        <input id="rmi-title" className={inputCls} maxLength={LIMITS.itemTitle} required value={title}
          disabled={busy} onChange={(e) => setTitle(e.target.value)} />
      </Field>
      <Field label="Description" htmlFor="rmi-desc" hint={`${description.length}/${LIMITS.itemDescription}`}>
        <textarea id="rmi-desc" rows={3} className={textareaCls} maxLength={LIMITS.itemDescription} required
          value={description} disabled={busy} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Theme" htmlFor="rmi-theme">
          <select id="rmi-theme" className={inputCls} value={themeId} disabled={busy}
            onChange={(e) => setThemeId(e.target.value)}>
            {roadmap.themes.map((t) => (
              <option key={t.id} value={t.id}>{t.name}</option>
            ))}
          </select>
        </Field>
        <Field label="Period" htmlFor="rmi-period">
          <select id="rmi-period" className={inputCls} value={period} disabled={busy}
            onChange={(e) => setPeriod(Number(e.target.value))}>
            {roadmap.periods.map((p) => (
              <option key={p.position} value={p.position}>{p.name}</option>
            ))}
          </select>
        </Field>
        <Field label="Status" htmlFor="rmi-status">
          <select id="rmi-status" className={inputCls} value={status} disabled={busy}
            onChange={(e) => setStatus(e.target.value as RoadmapStatus)}>
            {STATUSES.map((s) => (
              <option key={s.value} value={s.value}>{s.label}</option>
            ))}
          </select>
        </Field>
        <Field label="Release month (optional)" htmlFor="rmi-release">
          <input id="rmi-release" className={inputCls} maxLength={LIMITS.releaseMonth} value={release}
            placeholder="e.g. Dec 2026" disabled={busy} onChange={(e) => setRelease(e.target.value)} />
        </Field>
      </div>
      <ErrorBox message={error} />
      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          <SaveLabel busy={busy} />
        </Button>
      </div>
    </form>
  );
}
