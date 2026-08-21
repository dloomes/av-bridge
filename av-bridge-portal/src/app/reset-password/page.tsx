"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { CheckCircle2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";

// /reset-password?token=<hex> — landing page for the reset email link.
//
// The token is validated server-side on submit (a plausible-looking hex
// string could still be expired / used / non-existent — the server returns
// a generic "reset failed" for all three so probes can't distinguish). The
// portal only pre-checks that a token IS present so we can show a helpful
// "your link looks broken" message before the user types anything.
const MIN_PASSWORD_LEN = 12;

export default function ResetPasswordPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const token = searchParams.get("token")?.trim() ?? "";
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  // After a successful reset, bounce to /sign-in after a short beat so the
  // "you're set" moment is visible. useEffect keeps the redirect off the
  // render cycle so hitting the browser back button after landing on the
  // success screen doesn't re-trigger the timer.
  useEffect(() => {
    if (!done) return;
    const t = setTimeout(() => router.replace("/sign-in"), 2500);
    return () => clearTimeout(t);
  }, [done, router]);

  const tokenMissing = token === "";

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (password.length < MIN_PASSWORD_LEN) {
      setError(
        `Choose a password of at least ${MIN_PASSWORD_LEN} characters.`
      );
      return;
    }
    if (password !== confirm) {
      setError("The two passwords don't match.");
      return;
    }
    setSubmitting(true);
    try {
      await api.completePasswordReset(token, password);
      setDone(true);
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "We couldn't complete the reset. Try requesting a new link."
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      <div className="flex-1 flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-md">
          <div className="rounded-xl border border-border bg-card p-8 shadow-sm">
            {done ? (
              <div className="flex flex-col items-center text-center gap-3">
                <CheckCircle2
                  className="h-10 w-10 text-primary"
                  aria-hidden="true"
                />
                <h1 className="text-2xl font-semibold tracking-tight">
                  Password reset
                </h1>
                <p className="text-sm text-muted-foreground leading-relaxed">
                  You&rsquo;ll be redirected to sign in with your new password
                  in a moment.
                </p>
                <Link
                  href="/sign-in"
                  className="mt-2 text-sm text-primary hover:underline underline-offset-4"
                >
                  Continue to sign in
                </Link>
              </div>
            ) : tokenMissing ? (
              <>
                <h1 className="text-2xl font-semibold tracking-tight">
                  This link isn&rsquo;t valid
                </h1>
                <p className="mt-2 text-sm text-muted-foreground leading-relaxed">
                  The reset link is missing a token. Request a new one to try
                  again.
                </p>
                <Link
                  href="/forgot-password"
                  className="mt-6 inline-flex h-11 w-full items-center justify-center rounded-md bg-primary px-4 text-[14.5px] font-medium text-primary-foreground no-underline hover:opacity-90 transition-opacity"
                >
                  Request a new link
                </Link>
              </>
            ) : (
              <>
                <h1 className="text-2xl font-semibold tracking-tight">
                  Choose a new password
                </h1>
                <p className="mt-2 text-sm text-muted-foreground leading-relaxed">
                  For your security, all active sessions will be signed out
                  once you set a new password.
                </p>

                <form onSubmit={onSubmit} className="mt-6 flex flex-col gap-4">
                  <div className="flex flex-col gap-1.5">
                    <label
                      htmlFor="password"
                      className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground"
                    >
                      New password
                    </label>
                    <input
                      id="password"
                      type="password"
                      autoComplete="new-password"
                      required
                      autoFocus
                      minLength={MIN_PASSWORD_LEN}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      disabled={submitting}
                      className="h-[46px] rounded-md border border-border bg-background px-3.5 text-[15px] text-foreground outline-none transition-all hover:border-input focus:border-primary focus:shadow-[0_0_0_4px_hsl(var(--primary)/0.12)]"
                    />
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      Minimum {MIN_PASSWORD_LEN} characters.
                    </p>
                  </div>

                  <div className="flex flex-col gap-1.5">
                    <label
                      htmlFor="confirm"
                      className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground"
                    >
                      Confirm password
                    </label>
                    <input
                      id="confirm"
                      type="password"
                      autoComplete="new-password"
                      required
                      minLength={MIN_PASSWORD_LEN}
                      value={confirm}
                      onChange={(e) => setConfirm(e.target.value)}
                      disabled={submitting}
                      className="h-[46px] rounded-md border border-border bg-background px-3.5 text-[15px] text-foreground outline-none transition-all hover:border-input focus:border-primary focus:shadow-[0_0_0_4px_hsl(var(--primary)/0.12)]"
                    />
                  </div>

                  {error && (
                    <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
                      {error}
                    </div>
                  )}

                  <Button
                    type="submit"
                    disabled={
                      submitting ||
                      password.length < MIN_PASSWORD_LEN ||
                      password !== confirm
                    }
                    className="mt-2 h-12 w-full text-[14.5px] font-medium"
                  >
                    {submitting ? "Saving…" : "Set new password"}
                  </Button>
                </form>

                <div className="mt-6 border-t border-border pt-4 text-center">
                  <Link
                    href="/sign-in"
                    className="text-sm text-muted-foreground hover:text-foreground transition-colors no-underline"
                  >
                    Return to sign in
                  </Link>
                </div>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
