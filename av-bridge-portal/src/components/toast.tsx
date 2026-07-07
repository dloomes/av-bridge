"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { CheckCircle2, X, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";

// Toast primitives — a small, dependency-free replacement for the various
// alert() calls scattered around the portal. Deliberately narrow: title +
// optional description + optional action button (used for "Undo" on
// destructive flows). Anything more sophisticated (progress toasts,
// stacked positional variants) can layer on later.
//
// Screen readers pick these up via role="status" on the container, and
// each individual toast is aria-live="polite" — announcements interrupt
// nothing important but don't get missed either.

export type ToastVariant = "default" | "success" | "destructive";

export interface ToastAction {
  label: string;
  onClick: () => void;
}

export interface ToastInput {
  title: string;
  description?: string;
  variant?: ToastVariant;
  action?: ToastAction;
  // ms before auto-dismiss. Actions get a longer default so an operator
  // has time to click Undo. Set to 0 to disable auto-dismiss.
  duration?: number;
}

interface ToastRecord extends Required<Pick<ToastInput, "title" | "variant">> {
  id: number;
  description?: string;
  action?: ToastAction;
  duration: number;
}

interface ToastContextValue {
  toast: (input: ToastInput) => void;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    // Non-fatal fallback: no provider mounted means no toaster in the tree
    // (e.g. sign-in page). Fall back to a no-op so callers don't crash.
    return { toast: () => {}, dismiss: () => {} };
  }
  return ctx;
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<ToastRecord[]>([]);
  const idRef = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: number) => {
    setToasts((list) => list.filter((t) => t.id !== id));
    const timer = timers.current.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.current.delete(id);
    }
  }, []);

  const toast = useCallback(
    (input: ToastInput) => {
      const id = ++idRef.current;
      const duration = input.duration ?? (input.action ? 6000 : 4000);
      const record: ToastRecord = {
        id,
        title: input.title,
        description: input.description,
        variant: input.variant ?? "default",
        action: input.action,
        duration,
      };
      setToasts((list) => [...list, record]);
      if (duration > 0) {
        const timer = setTimeout(() => dismiss(id), duration);
        timers.current.set(id, timer);
      }
    },
    [dismiss]
  );

  // Cleanup on unmount so straggler timers don't call setState on a dead
  // provider (React 18 dev-mode double-mount is the common case).
  useEffect(() => {
    const currentTimers = timers.current;
    return () => {
      currentTimers.forEach(clearTimeout);
      currentTimers.clear();
    };
  }, []);

  const ctx = useMemo(() => ({ toast, dismiss }), [toast, dismiss]);

  return (
    <ToastContext.Provider value={ctx}>
      {children}
      <div
        role="status"
        aria-live="polite"
        aria-atomic="false"
        className="pointer-events-none fixed bottom-4 right-4 z-[100] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2"
      >
        {toasts.map((t) => (
          <ToastItem key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  );
}

// Individual toast card. `pointer-events-auto` re-enables clicks (the
// container disables them so the empty space doesn't intercept clicks on
// content underneath). Uses CSS transitions rather than Motion so the
// component stays dependency-free.
function ToastItem({
  toast,
  onDismiss,
}: {
  toast: ToastRecord;
  onDismiss: () => void;
}) {
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    // Trigger the enter transition on the next frame — mounting with
    // opacity-0 and immediately setting opacity-100 in the same synchronous
    // pass would skip the transition.
    const raf = requestAnimationFrame(() => setVisible(true));
    return () => cancelAnimationFrame(raf);
  }, []);

  const tone =
    toast.variant === "destructive"
      ? "border-destructive/40 bg-destructive/10"
      : toast.variant === "success"
        ? "border-[color:hsl(var(--success))]/40 bg-[color:hsl(var(--success))]/10"
        : "border-border bg-card";
  const Icon =
    toast.variant === "destructive"
      ? XCircle
      : toast.variant === "success"
        ? CheckCircle2
        : null;
  const iconColor =
    toast.variant === "destructive"
      ? "[color:hsl(var(--destructive))]"
      : toast.variant === "success"
        ? "[color:hsl(var(--success))]"
        : "text-muted-foreground";

  return (
    <div
      role={toast.variant === "destructive" ? "alert" : "status"}
      className={`pointer-events-auto flex items-start gap-3 rounded-lg border px-4 py-3 shadow-lg transition-all duration-200 ease-out ${tone} ${
        visible ? "translate-y-0 opacity-100" : "translate-y-2 opacity-0"
      }`}
    >
      {Icon && <Icon className={`h-4 w-4 mt-0.5 shrink-0 ${iconColor}`} />}
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{toast.title}</div>
        {toast.description && (
          <div className="mt-0.5 text-xs text-muted-foreground">
            {toast.description}
          </div>
        )}
      </div>
      {toast.action && (
        <Button
          variant="ghost"
          size="sm"
          className="shrink-0 h-8 -my-1"
          onClick={() => {
            toast.action?.onClick();
            onDismiss();
          }}
        >
          {toast.action.label}
        </Button>
      )}
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss notification"
        className="shrink-0 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  );
}
