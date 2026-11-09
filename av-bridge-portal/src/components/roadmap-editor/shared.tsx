"use client";

import { useEffect, useRef, useState } from "react";
import { ArrowDown, ArrowUp, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/modal";
import { plainError } from "@/lib/download";

export const inputCls =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm outline-none focus:border-primary disabled:opacity-60";
export const textareaCls =
  "w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none focus:border-primary disabled:opacity-60";

// Server limits (cloud internal/roadmap), mirrored as maxLength on the inputs.
export const LIMITS = {
  settingsTitle: 80,
  headline: 160,
  lede: 600,
  disclaimer: 600,
  itemTitle: 80,
  itemDescription: 240,
  releaseMonth: 20,
  periodName: 40,
  periodMonths: 60,
  themeName: 60,
  themeNote: 160,
  shippedWhen: 40,
  shippedTitle: 160,
  linkName: 80,
};

// useSave runs one save at a time: a second call while busy is ignored, and
// a failure is kept as a plain sentence for an inline message.
export function useSave() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const lock = useRef(false);
  async function run<T>(fn: () => Promise<T>): Promise<T | undefined> {
    if (lock.current) return undefined;
    lock.current = true;
    setBusy(true);
    setError(null);
    try {
      return await fn();
    } catch (e) {
      setError(plainError(e));
      return undefined;
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return { busy, error, setError, run };
}

// useDraft holds an editable copy of a server value. It resets only when the
// server value itself changes, so saving another section doesn't discard
// edits here.
export function useDraft<T>(server: T) {
  const key = JSON.stringify(server);
  const [draft, setDraft] = useState<T>(server);
  const last = useRef(key);
  useEffect(() => {
    if (last.current !== key) {
      last.current = key;
      setDraft(JSON.parse(key) as T);
    }
  }, [key]);
  return { draft, setDraft, dirty: JSON.stringify(draft) !== key };
}

export function ErrorBox({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]"
    >
      {message}
    </div>
  );
}

export function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="text-xs font-medium text-muted-foreground">
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function SaveLabel({ busy, label = "Save" }: { busy: boolean; label?: string }) {
  return busy ? (
    <>
      <Loader2 className="h-3.5 w-3.5 animate-spin" />
      Saving…
    </>
  ) : (
    <>{label}</>
  );
}

export function MoveButtons({
  index,
  count,
  disabled,
  onMove,
  what,
}: {
  index: number;
  count: number;
  disabled?: boolean;
  onMove: (delta: -1 | 1) => void;
  what: string;
}) {
  return (
    <>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={`Move ${what} up`}
        disabled={disabled || index === 0}
        onClick={() => onMove(-1)}
      >
        <ArrowUp className="h-3.5 w-3.5" />
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={`Move ${what} down`}
        disabled={disabled || index === count - 1}
        onClick={() => onMove(1)}
      >
        <ArrowDown className="h-3.5 w-3.5" />
      </Button>
    </>
  );
}

export function moveInList<T>(list: T[], i: number, delta: -1 | 1): T[] {
  const j = i + delta;
  if (j < 0 || j >= list.length) return list;
  const next = list.slice();
  [next[i], next[j]] = [next[j], next[i]];
  return next;
}

// ConfirmModal asks before a destructive action and shows the failure inline.
export function ConfirmModal({
  open,
  title,
  body,
  confirmLabel,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  title: string;
  body: React.ReactNode;
  confirmLabel: string;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  const { busy, error, run } = useSave();
  return (
    <Modal open={open} onClose={onCancel} title={title} wide={false}>
      <div className="flex flex-col gap-4 text-sm">
        <div>{body}</div>
        <ErrorBox message={error} />
        <div className="flex items-center justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button type="button" disabled={busy} onClick={() => void run(onConfirm)}>
            {busy ? (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Working…
              </>
            ) : (
              confirmLabel
            )}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
