"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { NamedRow } from "@/lib/types";

export type HierarchyKind = "region" | "location" | "building" | "room";
export type HierarchyMode = "create" | "edit";

// Initial values used in edit mode. Building entries can also carry address
// and timezone; the form ignores them for other kinds.
export interface HierarchyEditInitial {
  id: string;
  name: string;
  address?: string;
  timezone?: string;
}

interface HierarchyFormProps {
  kind: HierarchyKind;
  mode?: HierarchyMode;
  // Edit: pre-fill the form with current values.
  initial?: HierarchyEditInitial;
  // Create: parent id required for everything except "region". The page
  // passes it pre-filled so the operator never picks the wrong parent.
  parentId?: string;
  parentLabel?: string;
  onCancel: () => void;
  onSuccess: (saved: NamedRow) => void;
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function HierarchyForm({
  kind,
  mode = "create",
  initial,
  parentId,
  parentLabel,
  onCancel,
  onSuccess,
}: HierarchyFormProps) {
  const [name, setName] = useState(initial?.name ?? "");
  const [address, setAddress] = useState(initial?.address ?? "");
  const [timezone, setTimezone] = useState(initial?.timezone ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isEdit = mode === "edit";
  const needsParent = !isEdit && kind !== "region";

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError("Name is required");
      return;
    }
    if (needsParent && !parentId) {
      setError("Parent is missing");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      let saved: { id: string; name?: string };
      if (isEdit) {
        if (!initial) throw new Error("missing initial value for edit");
        switch (kind) {
          case "region":
            saved = await api.updateRegion(initial.id, name.trim());
            break;
          case "location":
            saved = await api.updateLocation(initial.id, name.trim());
            break;
          case "building":
            saved = await api.updateBuilding(initial.id, {
              name: name.trim(),
              address: address.trim(),
              timezone: timezone.trim(),
            });
            break;
          case "room":
            saved = await api.updateRoom(initial.id, name.trim());
            break;
        }
        onSuccess({
          id: initial.id,
          name: name.trim(),
          parent_id: parentId,
        });
      } else {
        switch (kind) {
          case "region":
            saved = await api.createRegion(name.trim());
            break;
          case "location":
            saved = await api.createLocation(parentId!, name.trim());
            break;
          case "building":
            saved = await api.createBuilding({
              location_id: parentId!,
              name: name.trim(),
              address: address.trim() || undefined,
              timezone: timezone.trim() || undefined,
            });
            break;
          case "room":
            saved = await api.createRoom(parentId!, name.trim());
            break;
        }
        onSuccess({
          id: saved.id,
          name: saved.name ?? name.trim(),
          parent_id: parentId,
        });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      {needsParent && parentLabel && (
        <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs">
          <span className="text-muted-foreground">In </span>
          <span className="font-medium">{parentLabel}</span>
        </div>
      )}

      <div>
        <label className={labelClass}>Name</label>
        <input
          className={inputClass}
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          required
        />
      </div>

      {kind === "building" && (
        <>
          <div>
            <label className={labelClass}>Address (optional)</label>
            <input
              className={inputClass}
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              placeholder="123 Example Street"
            />
          </div>
          <div>
            <label className={labelClass}>Timezone (optional)</label>
            <input
              className={inputClass}
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              placeholder="Europe/London"
            />
          </div>
        </>
      )}

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {isEdit ? "Save changes" : `Create ${kind}`}
        </Button>
      </div>
    </form>
  );
}
