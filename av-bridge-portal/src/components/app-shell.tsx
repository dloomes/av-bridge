"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loader2 } from "lucide-react";
import { Sidebar } from "@/components/sidebar";
import { BrandingProvider } from "@/components/branding-provider";
import { ToastProvider } from "@/components/toast";
import { useSession } from "@/hooks/useSession";

// AppShell owns the top-level chrome decision: sign-in screen gets the full
// viewport with no sidebar; every other route gets the sidebar + content
// only if the user is signed in. Keeps layout.tsx a server component while
// the session-aware bits stay client-side.
export function AppShell({ children }: { children: React.ReactNode }) {
  const session = useSession();
  const router = useRouter();
  const pathname = usePathname();
  const onSignIn = pathname === "/sign-in";

  useEffect(() => {
    if (!session.hydrated) return;
    if (!session.token && !onSignIn) {
      router.replace("/sign-in");
    }
  }, [session.hydrated, session.token, onSignIn, router]);

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
