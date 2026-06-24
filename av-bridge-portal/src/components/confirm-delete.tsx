"use client";

import { useState } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";

export interface ImpactCounts {
  locations?: number;
  buildings?: number;
  rooms?: number;
  devicesOrphaned: number;
}

interface ConfirmDeleteProps {
  // What is being deleted, used in copy: "Delete <kind>: <name>?"
  kind: string;
  name: string;
  // Counts to show under "This will also delete". Zero values hidden.
  impact: ImpactCounts;
  onCancel: () => void;
  onConfirm: () => Promise<void> | void;
}

// Confirm dialog used for hierarchy deletes. Shows the cascade impact so the
// admin sees exactly what disappears + how many devices end up unparented
// before they commit. Devices are never destroyed — the schema's
// ON DELETE SET NULL on devices.room_id orphans them.
export function ConfirmDelete({
  kind,
  name,
  impact,
  onCancel,
  onConfirm,
}: ConfirmDeleteProps) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const items: { label: string; count: number }[] = [];
  if (impact.locations) items.push({ label: "locations", count: impact.locations });
  if (impact.buildings) items.push({ label: "buildings", count: impact.buildings });
  if (impact.rooms) items.push({ label: "rooms", count: impact.rooms });

  const handleConfirm = async () => {
    setError(null);
    setSubmitting(true);
    try {
      await onConfirm();
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };

  return (
    <div className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="flex gap-3 items-start">
        <AlertTriangle className="h-5 w-5 mt-0.5 text-amber-500 shrink-0" />
        <div className="text-sm">
          <p>
            Permanently delete {kind} <span className="font-semibold">{name}</span>?
          </p>
          {(items.length > 0 || impact.devicesOrphaned > 0) && (
            <>
              <p className="mt-2 text-muted-foreground">This will also:</p>
              <ul className="mt-1 list-disc pl-5 text-muted-foreground space-y-0.5">
                {items.map((it) => (
                  <li key={it.label}>
                    Delete {it.count} {it.label}
                  </li>
                ))}
                {impact.devicesOrphaned > 0 && (
                  <li>
                    Orphan {impact.devicesOrphaned} device
                    {impact.devicesOrphaned === 1 ? "" : "s"} (device records
                    are kept, but unassigned from any room)
                  </li>
                )}
              </ul>
            </>
          )}
        </div>
      </div>

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button
          type="button"
          variant="destructive"
          onClick={handleConfirm}
          disabled={submitting}
        >
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Delete {kind}
        </Button>
      </div>
    </div>
  );
}
