"use client";

// Client landing page for the Entra vendor SSO flow.
//
// The cloud's callback handler mints an `av_` session token, then 302s the
// browser to `/sign-in/callback?token=<token>`. This component picks the
// token out of the URL, stores it (same shape as password login), fetches
// /whoami to hydrate the session, and lands the user on `/`.
//
// Errors surface by redirecting back to /sign-in with `?entra_error=...`,
// which the sign-in form renders as an inline banner. No error UI lives
// here — this is a transient landing pad, not a destination.
//
// Wrapped in a Suspense boundary by ./page.tsx so Next.js's static
// prerender doesn't bail out on the useSearchParams call.

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { signIn } from "@/lib/session";

export function SignInCallbackClient() {
  const router = useRouter();
  const searchParams = useSearchParams();
  // Keep a bit of visible chrome so a slow whoami doesn't look like a
  // broken page. The spinner is the only frame the user should see.
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setVisible(true), 120);
    return () => window.clearTimeout(timer);
  }, []);

  useEffect(() => {
    const token = searchParams.get("token");
    if (!token) {
      router.replace("/sign-in?entra_error=missing_token");
      return;
    }
    // Seed the session with just the token so the API client will attach
    // it to /whoami. The user object is fully populated on the next line
    // once whoami returns.
    signIn(token, { role: "" });
    (async () => {
      try {
        const who = await api.whoami();
        signIn(token, {
          user_id: who.user_id,
          email: who.email,
          name: who.name,
          customer_id: who.customer_id,
          role: who.role,
          is_vendor: who.is_vendor,
          permissions: who.permissions ?? [],
        });
        router.replace("/");
      } catch {
        // whoami failure at this point means either the freshly-minted
        // token was rejected (server bug) or the network flapped. Either
        // way, back to sign-in with a diagnostic code — the user will
        // retry and it'll either succeed or the ops team will notice
        // the same code showing up in Sentry / cloud logs.
        router.replace("/sign-in?entra_error=session_hydrate_failed");
      }
    })();
  }, [router, searchParams]);

  return (
    <div className="flex min-h-screen items-center justify-center bg-background">
      {visible && (
        <div className="flex items-center gap-3 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="font-mono text-[11px] uppercase tracking-wider">
            Completing sign-in…
          </span>
        </div>
      )}
    </div>
  );
}
