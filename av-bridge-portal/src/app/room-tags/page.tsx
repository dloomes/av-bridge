"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Loader2, Pencil, Plus, Tags, Trash2, Upload } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { useToast } from "@/components/toast";
import { TAG_COLOURS, TagChip } from "@/components/room-tags";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import type { RoomTag, RoomTagImportResult } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import { cn } from "@/lib/utils";

// Room tags: the customer's own labels for rooms ("Courtroom", "Pilot").
// Created here, then applied to rooms on the Locations page, one room or
// many at a time, or from a CSV here.

export default function RoomTagsPage() {
  const session = useSession();
  const canManage = !!session.user?.is_vendor || hasPermission(session.user, "hierarchy.crud");
  const { toast } = useToast();
  const [tags, setTags] = useState<RoomTag[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ tag?: RoomTag } | null>(null);
  const [deleting, setDeleting] = useState<RoomTag | null>(null);
  const [importing, setImporting] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const t = await api.listRoomTags(signal);
      if (!signal?.aborted) {
        setTags(t);
        setLoadError(null);
      }
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  return (
    <div className="min-h-app">
      <div className="max-w-4xl mx-auto p-6 space-y-4">
        <header className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
              <Tags aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div>
              <h1 className="text-2xl font-semibold leading-tight">Room tags</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                Label rooms by type or purpose, then filter by them across the portal.
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {canManage && (
              <>
                <Button variant="outline" onClick={() => setImporting(true)}>
                  <Upload className="h-4 w-4" />
                  Import CSV
                </Button>
                <Button onClick={() => setEditing({})}>
                  <Plus className="h-4 w-4" />
                  New tag
                </Button>
              </>
            )}
            <UserMenu />
          </div>
        </header>

        {loadError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {loadError}
          </div>
        )}

        {tags === null ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : tags.length === 0 ? (
          <Card>
            <CardContent className="p-10 text-center space-y-3">
              <p className="text-sm text-muted-foreground">
                No room tags yet. Create tags such as Courtroom, Hearing room or Pilot, then
                apply them to rooms on the Locations page.
              </p>
              {canManage && (
                <Button onClick={() => setEditing({})}>
                  <Plus className="h-4 w-4" />
                  Create your first tag
                </Button>
              )}
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <ul className="divide-y" role="list">
                {tags.map((t) => (
                  <li key={t.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                    <TagChip tag={t} />
                    <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
                      {t.description}
                    </span>
                    <Link
                      href={`/locations?tag=${encodeURIComponent(t.id)}`}
                      className="text-xs text-muted-foreground hover:text-foreground hover:underline"
                    >
                      {t.room_count} room{t.room_count === 1 ? "" : "s"}
                    </Link>
                    {canManage && (
                      <span className="flex items-center">
                        <Button size="sm" variant="ghost" aria-label={`Edit ${t.name}`} onClick={() => setEditing({ tag: t })}>
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button size="sm" variant="ghost" aria-label={`Delete ${t.name}`} onClick={() => setDeleting(t)}>
                          <Trash2 className="h-3.5 w-3.5 text-destructive" />
                        </Button>
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>
        )}

        {canManage && tags && tags.length > 0 && (
          <p className="text-xs text-muted-foreground">
            To tag rooms, open <Link href="/locations" className="underline">Locations</Link>: use a
            room&apos;s tag button, or <span className="font-medium">Select rooms</span> to tag many at once.
          </p>
        )}
      </div>

      <Modal open={editing !== null} onClose={() => setEditing(null)} title={editing?.tag ? "Edit tag" : "New tag"} wide={false}>
        {editing && (
          <TagForm
            tag={editing.tag}
            onCancel={() => setEditing(null)}
            onSaved={(name) => {
              setEditing(null);
              toast({ title: editing.tag ? `Saved ${name}` : `Created ${name}`, variant: "success" });
              void load();
            }}
          />
        )}
      </Modal>

      <Modal open={deleting !== null} onClose={() => setDeleting(null)} title="Delete tag" wide={false}>
        {deleting && (
          <DeleteTag
            tag={deleting}
            onCancel={() => setDeleting(null)}
            onDeleted={() => {
              toast({ title: `Deleted ${deleting.name}`, variant: "success" });
              setDeleting(null);
              void load();
            }}
          />
        )}
      </Modal>

      <Modal open={importing} onClose={() => setImporting(false)} title="Import room tags" wide={false}>
        {importing && <ImportTags onClose={() => setImporting(false)} onImported={() => void load()} />}
      </Modal>
    </div>
  );
}

const inputCls =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function TagForm({
  tag,
  onCancel,
  onSaved,
}: {
  tag?: RoomTag;
  onCancel: () => void;
  onSaved: (name: string) => void;
}) {
  const [name, setName] = useState(tag?.name ?? "");
  const [colour, setColour] = useState(tag?.colour ?? "blue");
  const [description, setDescription] = useState(tag?.description ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Give the tag a name.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const body = { name: trimmed, colour, description: description.trim() };
      if (tag) await api.updateRoomTag(tag.id, body);
      else await api.createRoomTag(body);
      onSaved(trimmed);
    } catch (err) {
      const msg = (err as Error).message;
      setError(msg.includes("409") ? "A tag with that name already exists." : msg);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={save} className="space-y-4">
      <div className="space-y-1">
        <label htmlFor="tag-name" className="text-xs font-medium text-muted-foreground">
          Name
        </label>
        <input
          id="tag-name"
          autoFocus
          maxLength={60}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. Courtroom"
          className={inputCls}
        />
      </div>
      <div className="space-y-1">
        <span className="text-xs font-medium text-muted-foreground">Colour</span>
        <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Colour">
          {Object.entries(TAG_COLOURS).map(([key, c]) => (
            <button
              key={key}
              type="button"
              role="radio"
              aria-checked={colour === key}
              aria-label={c.label}
              title={c.label}
              onClick={() => setColour(key)}
              className={cn(
                "h-8 w-8 rounded-full border-2 transition-transform focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                c.dot,
                colour === key ? "scale-110 border-foreground" : "border-transparent hover:scale-105"
              )}
            />
          ))}
        </div>
      </div>
      <div className="space-y-1">
        <label htmlFor="tag-description" className="text-xs font-medium text-muted-foreground">
          Description (optional)
        </label>
        <input
          id="tag-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="What the tag means, for whoever applies it"
          className={inputCls}
        />
      </div>
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        Preview: <TagChip tag={{ name: name.trim() || "Tag name", colour, description }} />
      </div>
      {error && <p className="text-sm [color:hsl(var(--destructive))]">{error}</p>}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
        <Button type="submit" disabled={saving}>
          {saving && <Loader2 className="h-4 w-4 animate-spin" />}
          {tag ? "Save" : "Create tag"}
        </Button>
      </div>
    </form>
  );
}

function DeleteTag({
  tag,
  onCancel,
  onDeleted,
}: {
  tag: RoomTag;
  onCancel: () => void;
  onDeleted: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.deleteRoomTag(tag.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <p className="text-sm">
        Delete <TagChip tag={tag} />?{" "}
        {tag.room_count > 0
          ? `It's on ${tag.room_count} room${tag.room_count === 1 ? "" : "s"}, which will lose it. The rooms themselves aren't changed.`
          : "No rooms have it."}
      </p>
      {error && <p className="text-sm [color:hsl(var(--destructive))]">{error}</p>}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button variant="destructive" onClick={remove} disabled={busy}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          Delete tag
        </Button>
      </div>
    </div>
  );
}

function ImportTags({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const fileRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RoomTagImportResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const upload = async (file: File) => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.importRoomTags(file);
      setResult(r);
      if (r.errors.length === 0) onImported();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const template = () => {
    const csv = "region,location,building,room,tags\nNorth,Leeds,Crown Court,Court 1,Courtroom;Video link\n";
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "room-tags-template.csv";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <div className="space-y-4 text-sm">
      <p className="text-muted-foreground">
        A CSV with <span className="font-mono">building</span>, <span className="font-mono">room</span> and{" "}
        <span className="font-mono">tags</span> columns. Separate several tags with <span className="font-mono">;</span>.
        Add <span className="font-mono">region</span> and <span className="font-mono">location</span> columns if two
        buildings share a name.
      </p>
      <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
        <li>Tags are added to rooms; none are removed.</li>
        <li>Tags that don&apos;t exist yet are created, in grey. Recolour them afterwards.</li>
        <li>Every row is checked first. If any row has a problem, nothing changes.</li>
      </ul>
      <input
        ref={fileRef}
        type="file"
        accept=".csv,text/csv"
        className="hidden"
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) void upload(f);
        }}
      />
      {result && result.errors.length === 0 && (
        <div className="rounded-md border border-green-500/30 bg-green-500/5 px-3 py-2">
          Tagged {result.rooms_tagged} room{result.rooms_tagged === 1 ? "" : "s"} from {result.processed} row
          {result.processed === 1 ? "" : "s"}.
          {result.tags_created.length > 0 && ` Created ${result.tags_created.join(", ")}.`}
        </div>
      )}
      {result && result.errors.length > 0 && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 space-y-1">
          <p className="font-medium [color:hsl(var(--destructive))]">Nothing was imported. Fix these rows and try again:</p>
          <ul className="max-h-48 overflow-y-auto text-xs">
            {result.errors.map((e, i) => (
              <li key={i}>
                Row {e.row}: {e.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      {error && <p className="[color:hsl(var(--destructive))]">{error}</p>}
      <div className="flex flex-wrap justify-between gap-2">
        <Button variant="ghost" onClick={template}>
          Download template
        </Button>
        <div className="flex gap-2">
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
          <Button onClick={() => fileRef.current?.click()} disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />}
            Choose CSV
          </Button>
        </div>
      </div>
    </div>
  );
}
