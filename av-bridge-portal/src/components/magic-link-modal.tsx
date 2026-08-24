"use client";

import { useEffect, useState } from "react";
import { Check, Copy, Loader2, Zap } from "lucide-react";
import { Button } from "@/components/ui/button";

// MagicLinkModal — post-mint display for a vendor-issued break-glass
// sign-in URL. Shows the URL with a copy button, the target user's
// label so the vendor sends the right one, and a countdown so they
// don't hoard the link past its expiry.
//
// The parent handles the mint call and passes the fresh result down;
// this component is a display-only surface with a single clipboard
// side-effect. No fetch inside it — that keeps error handling in the
// parent's UI where it can be styled to match the surrounding page.

interface MagicLinkModalProps {
  url: string;
  expiresAt: string; // RFC3339
  targetLabel: string; // "alice@acme.com" or a full name
  onClose: () => void;
}

export function MagicLinkModal({ url, expiresAt, targetLabel, onClose }: MagicLinkModalProps) {
  const [copied, setCopied] = useState(false);
  const [remainingSec, setRemainingSec] = useState(() =>
    Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000)),
  );

  // Tick every second so the countdown updates. Clears on unmount.
  useEffect(() => {
    const timer = setInterval(() => {
      setRemainingSec(
        Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000)),
      );
    }, 1000);
    return () => clearInterval(timer);
  }, [expiresAt]);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard API unavailable — the input's still selectable, so
      // the user can copy manually. No inline error surface needed.
    }
  };

  const mins = Math.floor(remainingSec / 60);
  const secs = remainingSec % 60;
  const expired = remainingSec === 0;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start gap-3 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-3 text-sm">
        <Zap className="mt-0.5 h-4 w-4 shrink-0 [color:hsl(var(--warning))]" />
        <div>
          <p className="[color:hsl(var(--warning))] font-medium">
            Send this link only to <span className="font-mono">{targetLabel}</span>.
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            Anyone who opens it lands signed in as this user — no password,
            no SSO. Single use, expires in {mins}:{String(secs).padStart(2, "0")}.
          </p>
        </div>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground">
          Sign-in URL
        </label>
        <div className="mt-1 flex gap-2">
          <input
            readOnly
            value={url}
            onFocus={(e) => e.currentTarget.select()}
            className="min-w-0 flex-1 h-9 rounded-md border border-input bg-muted/30 px-3 text-xs font-mono outline-none"
          />
          <Button type="button" onClick={onCopy} disabled={expired}>
            {copied ? (
              <>
                <Check className="h-3.5 w-3.5" />
                Copied
              </>
            ) : (
              <>
                <Copy className="h-3.5 w-3.5" />
                Copy
              </>
            )}
          </Button>
        </div>
      </div>

      {expired && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs [color:hsl(var(--destructive))]">
          This link has expired. Close this dialog and generate a new one.
        </div>
      )}

      <div className="flex items-center justify-between border-t pt-3 text-xs text-muted-foreground">
        <span>
          {expired
            ? "Expired"
            : `Expires in ${mins}:${String(secs).padStart(2, "0")}`}
        </span>
        <Button variant="ghost" size="sm" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}

// MagicLinkTrigger — small helper the row-level UIs can render inline.
// Owns the mint call, the loading state, and the error surface; on
// success it hoists the URL up so the caller can show MagicLinkModal.
//
// Kept as a component (not a hook) so the callers can dispatch it as a
// single <MagicLinkTrigger /> in a table row without threading state
// through a bunch of props.
interface MagicLinkTriggerProps {
  disabled?: boolean;
  onMint: () => Promise<{ url: string; expires_at: string }>;
  onResult: (result: { url: string; expiresAt: string }) => void;
}

export function MagicLinkTrigger({ disabled, onMint, onResult }: MagicLinkTriggerProps) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    setErr(null);
    setBusy(true);
    try {
      const r = await onMint();
      onResult({ url: r.url, expiresAt: r.expires_at });
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        aria-label="Generate sign-in link"
        title="Generate one-time sign-in link (vendor break-glass)"
        onClick={run}
        disabled={disabled || busy}
      >
        {busy ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
        ) : (
          <Zap className="h-3.5 w-3.5" />
        )}
      </Button>
      {err && (
        <span className="ml-1 max-w-[200px] truncate text-[11px] [color:hsl(var(--destructive))]" title={err}>
          {err}
        </span>
      )}
    </>
  );
}
