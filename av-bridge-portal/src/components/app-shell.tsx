"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loader2, LockKeyhole } from "lucide-react";
import { Sidebar } from "@/components/sidebar";
import { BrandingProvider } from "@/components/branding-provider";
import { ToastProvider } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { signIn, signOut } from "@/lib/session";

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
          is_scoped: who.is_scoped,
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

  // Signed in but holding no permissions — typically a Microsoft user in no
  // mapped Entra group. Every page would just fail its API calls, so say
  // what's going on instead. permissions === undefined means whoami hasn't
  // answered yet, so don't flash this while loading.
  const perms = session.user?.permissions;
  if (!session.user?.is_vendor && perms !== undefined && perms.length === 0) {
    return (
      <ToastProvider>
        <BrandingProvider>
          <NoAccess email={session.user?.email ?? ""} />
        </BrandingProvider>
      </ToastProvider>
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

function NoAccess({ email }: { email: string }) {
  const router = useRouter();
  const handleSignOut = async () => {
    await api.logout();
    signOut();
    router.replace("/sign-in");
  };
  return (
    <main className="flex-1 min-w-0 flex h-screen items-center justify-center p-6">
      <div className="max-w-md w-full rounded-lg border bg-card p-6 space-y-4 text-sm">
        <div className="flex items-center gap-3">
          <div className="h-9 w-9 rounded-md bg-muted flex items-center justify-center">
            <LockKeyhole className="h-4 w-4" />
          </div>
          <h1 className="text-lg font-semibold">You don&apos;t have access yet</h1>
        </div>
        <p>
          You&apos;re signed in{email ? <> as <span className="font-medium">{email}</span></> : null}, but your
          account doesn&apos;t have a role, so there&apos;s nothing to show.
        </p>
        <p className="text-muted-foreground">
          Ask your M.A.R.C.U.S. administrator to give you a role on the Users page. If you sign in with
          Microsoft, they can instead add the Entra group you belong to under Sign-in mappings; your access
          then appears the next time you sign in.
        </p>
        <button
          type="button"
          onClick={handleSignOut}
          className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-muted"
        >
          Sign out
        </button>
      </div>
    </main>
  );
}
