"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loader2 } from "lucide-react";
import { Sidebar } from "@/components/sidebar";
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
    return <main className="flex-1 min-w-0">{children}</main>;
  }

  if (!session.hydrated || !session.token) {
    return (
      <main className="flex-1 min-w-0 flex h-screen items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </main>
    );
  }

  return (
    <>
      <Sidebar />
      <main className="flex-1 min-w-0">{children}</main>
    </>
  );
}
