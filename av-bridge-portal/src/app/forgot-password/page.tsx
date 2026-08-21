"use client";

import Link from "next/link";
import { useState } from "react";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";

// /forgot-password — start-of-flow page for the self-serve password reset.
//
// UX shape: single-field email form → success screen. The success screen is
// shown even when the email doesn't match a user; the server's 202-uniform
// response prevents enumeration and this page keeps the promise consistent.
// Users who mistyped their address can hit "back" and try again.
export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim()) return;
    setSubmitting(true);
    setError(null);
    try {
      await api.requestPasswordReset(email.trim());
      setSent(true);
    } catch (err) {
      // requestPasswordReset only rejects on transport errors — the server
      // always returns 202. If we're here, the network is off; tell the
      // user to try again rather than pretending it worked.
      setError(
        err instanceof Error
          ? err.message
          : "We couldn't reach the server. Try again in a moment."
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      <div className="flex-1 flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-md">
          <div className="mb-6 flex justify-start">
            <Link
              href="/sign-in"
              className="inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.14em] text-muted-foreground hover:text-foreground transition-colors no-underline"
            >
              <ArrowLeft className="h-3 w-3" />
              Back to sign in
            </Link>
          </div>

          <div className="rounded-xl border border-border bg-card p-8 shadow-sm">
            <h1 className="text-2xl font-semibold tracking-tight">
              {sent ? "Check your email" : "Reset your password"}
            </h1>
            <p className="mt-2 text-sm text-muted-foreground leading-relaxed">
              {sent ? (
                <>
                  If an account exists for{" "}
                  <span className="font-medium text-foreground">
                    {email.trim()}
                  </span>
                  , we&rsquo;ve sent a link to choose a new password. It will
                  expire in one hour.
                </>
              ) : (
                <>
                  Enter the email you sign in with and we&rsquo;ll send a link
                  to choose a new password.
                </>
              )}
            </p>

            {!sent && (
              <form onSubmit={onSubmit} className="mt-6 flex flex-col gap-4">
                <div className="flex flex-col gap-1.5">
                  <label
                    htmlFor="email"
                    className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground"
                  >
                    Work email
                  </label>
                  <input
                    id="email"
                    type="email"
                    autoComplete="username"
                    required
                    autoFocus
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    disabled={submitting}
                    placeholder="alex@acme.com"
                    className="h-[46px] rounded-md border border-border bg-background px-3.5 text-[15px] text-foreground placeholder:text-muted-foreground outline-none transition-all hover:border-input focus:border-primary focus:shadow-[0_0_0_4px_hsl(var(--primary)/0.12)]"
                  />
                </div>

                {error && (
                  <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
                    {error}
                  </div>
                )}

                <Button
                  type="submit"
                  disabled={submitting || !email.trim()}
                  className="mt-2 h-12 w-full text-[14.5px] font-medium"
                >
                  {submitting ? "Sending…" : "Send reset link"}
                </Button>
              </form>
            )}

            {sent && (
              <div className="mt-6 flex flex-col gap-3">
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setSent(false);
                    setEmail("");
                  }}
                  className="h-11 w-full text-sm"
                >
                  Use a different email
                </Button>
                <Link
                  href="/sign-in"
                  className="text-center text-sm text-muted-foreground hover:text-foreground transition-colors no-underline"
                >
                  Return to sign in
                </Link>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
