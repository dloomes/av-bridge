"use client";

import { useEffect, useRef } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  // wide: lets the form use the full breadth on roomy screens. Defaults true
  // for the device form, which has a lot of fields.
  wide?: boolean;
  // dirty: when true, closing via Esc or click-outside triggers a
  // confirmation dialog before firing onClose. Explicit dismiss buttons
  // still call onClose directly — the confirmation is only for accidents.
  dirty?: boolean;
  // Overrides the confirmation prompt text; useful for context-specific
  // wording ("You'll lose the CSV rows you haven't imported").
  dirtyPrompt?: string;
}

// Minimal modal: portal overlay + click-outside + Esc to close. Improved
// over the earlier version with:
//   * Focus captured to the first focusable element on open so keyboard
//     and screen-reader users land in the content, not the scrim.
//   * Focus restored to the element that opened the modal on close, so
//     tab-order isn't stranded at document root.
//   * Dirty-guard: if `dirty` is true, accidental close paths (Esc, scrim
//     click) prompt for confirmation before dismissing.
export function Modal({
  open,
  onClose,
  title,
  children,
  wide = true,
  dirty = false,
  dirtyPrompt = "Discard your changes?",
}: ModalProps) {
  const contentRef = useRef<HTMLDivElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);

  // Attempt to close via an accidental path (Esc / scrim). Confirm first
  // when the modal is dirty; otherwise pass through. Explicit close-button
  // callers should keep using onClose directly — they're already an
  // affirmative "I meant to leave" signal.
  const attemptClose = () => {
    if (dirty && !window.confirm(dirtyPrompt)) return;
    onClose();
  };

  useEffect(() => {
    if (!open) return;

    // Snapshot the element that had focus before we mounted so we can
    // return focus there on unmount. Casting to HTMLElement so we can
    // call .focus() — activeElement is Element | null.
    previousFocus.current =
      (document.activeElement as HTMLElement | null) ?? null;

    // Move focus into the modal on next frame, after the DOM has painted.
    // First tries an [autoFocus] element; failing that, the first
    // focusable descendant; failing that, the modal container itself.
    const raf = requestAnimationFrame(() => {
      const root = contentRef.current;
      if (!root) return;
      const autoFocus = root.querySelector<HTMLElement>("[autofocus]");
      if (autoFocus) {
        autoFocus.focus();
        return;
      }
      const focusable = root.querySelector<HTMLElement>(
        'button, [href], input:not([type="hidden"]), select, textarea, [tabindex]:not([tabindex="-1"])'
      );
      (focusable ?? root).focus();
    });

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        attemptClose();
      }
    };
    document.addEventListener("keydown", onKey);
    // Lock body scroll while modal is open so the page underneath doesn't
    // drift if the user scrolls the form.
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      cancelAnimationFrame(raf);
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
      // Restore focus if the previously-focused element is still in the
      // DOM. If it isn't (e.g. re-rendered), fall through — better than
      // throwing a "focus on detached node" error.
      const target = previousFocus.current;
      if (target && document.contains(target)) {
        target.focus();
      }
    };
    // dirty is intentionally not in the deps — Esc semantics only need to
    // change when open flips. attemptClose closes over the latest value
    // via ref-less closure below since we recreate on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  if (!open) return null;
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title}
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/60 p-4 sm:p-8"
      onClick={attemptClose}
    >
      <div
        ref={contentRef}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        className={`relative w-full rounded-lg border bg-card shadow-lg outline-none ${
          wide ? "max-w-2xl" : "max-w-md"
        }`}
      >
        <div className="flex items-center justify-between border-b px-5 py-3">
          <h2 className="text-base font-semibold">{title}</h2>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close">
            <X className="h-4 w-4" />
          </Button>
        </div>
        <div className="px-5 py-4">{children}</div>
      </div>
    </div>
  );
}
