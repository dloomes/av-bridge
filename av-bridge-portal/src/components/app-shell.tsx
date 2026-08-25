"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loader2 } from "lucide-react";
import { Sidebar } from "@/components/sidebar";
import { BrandingProvider } from "@/components/branding-provider";
import { ToastProvider } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { signIn } from "@/lib/session";

// AppShell owns the top-level chrome decision: sign-in screen gets the full
// viewport with no sidebar; every other route gets the sidebar + content
// only if the user is signed in. Keeps layout.tsx a server component while
// the session-aware bits stay client-side.
export function AppShell({ children }: { children: React.ReactNode }) {
  const session = useSession();
  const router = useRouter();
  const pathname = usePathname();
  // Auth-optional routes: shown without the sidebar chrome and reachable
  // without a session. /sign-in itself, /sign-in/callback (Entra lands the
  // token here, so requiring a token would deadlock the flow), plus the
  // self-serve password reset pair — /forgot-password (start of flow) and
  // /reset-password?token=... (email link landing). Signed-in users hit
  // these too (e.g. someone clicks their own reset link mid-session) and
  // still get the full-screen unauthed layout, which matches the intent.
  const onSignIn =
    pathname === "/sign-in" ||
    pathname.startsWith("/sign-in/") ||
    pathname === "/forgot-password" ||
    pathname === "/reset-password";

  useEffect(() => {
    if (!session.hydrated) return;
    if (!session.token && !onSignIn) {
      router.replace("/sign-in");
    }
  }, [session.hydrated, session.token, onSignIn, router]);

  // Session self-heal: an older stored user has no permissions[]. Fetch
  // /whoami once so hasPermission() has data to work with, without making
  // the user sign out and back in whenever the session shape evolves.
  // Fires only when there IS a token — no auth attempt on the sign-in
  // page. If whoami fails (expired token etc.) we leave the session alone
  // and let the next authed request return 401 → sign-in redirect.
  useEffect(() => {
    if (!session.hydrated || !session.token || onSignIn) return;
    // Re-fetch whoami whenever the stored session is missing any field
    // that has been added since the user last signed in. permissions
    // was the original trigger; landing_page was added later and
    // sessions minted before it stay undefined without this. Any new
    // whoami field should be added to this condition so existing
    // sessions catch up without a sign-out.
    if (
      session.user?.permissions !== undefined &&
      session.user?.landing_page !== undefined
    ) {
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const who = await api.whoami();
        if (cancelled) return;
        signIn(session.token as string, {
          user_id: who.user_id,
          email: who.email,
          name: who.name,
          customer_id: who.customer_id,
          role: who.role,
          is_vendor: who.is_vendor,
          permissions: who.permissions ?? [],
          landing_page: who.landing_page,
        });
      } catch {}
    })();
    return () => {
      cancelled = true;
    };
  }, [session.hydrated, session.token, session.user, onSignIn]);

  if (onSignIn) {
    // Sign-in still gets ToastProvider so login errors can surface as
    // toasts instead of alert()s. Branding stays out — pre-auth lookup
    // wouldn't know which tenant.
    return (
      <ToastProvider>
        <main className="flex-1 min-w-0">{children}</main>
      </ToastProvider>
    );
  }

  if (!session.hydrated || !session.token) {
    return (
      <main className="flex-1 min-w-0 flex h-screen items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </main>
    );
  }

  // BrandingProvider sits inside the authed shell — it fires GET /branding
  // once a token is available (the endpoint is authed) and re-fires when
  // the vendor scope changes. Sign-in stays outside so the login page
  // renders in defaults without a pre-auth branding lookup. ToastProvider
  // wraps everything so any page can call useToast().
  return (
    <ToastProvider>
      <BrandingProvider>
        <Sidebar />
        <main className="flex-1 min-w-0">{children}</main>
      </BrandingProvider>
    </ToastProvider>
  );
}
