"use client";

import { useEffect } from "react";
import { useRouter, usePathname } from "next/navigation";
import { Loader2 } from "lucide-react";
import { useSession } from "@/hooks/useSession";

// Wrap any page that requires a signed-in user. Redirects to /sign-in if
// no token has been stored. We wait for `hydrated` to flip true before
// deciding — otherwise SSR/first paint would always think the user is
// signed out.
//
// Vendor users without a customer scope still pass through here; pages that
// require a tenant scope must check separately and prompt the user to pick
// a customer (helpdesk flow).
export function AuthGuard({ children }: { children: React.ReactNode }) {
  const session = useSession();
  const router = useRouter();
  const pathname = usePathname();

  useEffect(() => {
    if (!session.hydrated) return;
    if (!session.token && pathname !== "/sign-in") {
      router.replace("/sign-in");
    }
  }, [session.hydrated, session.token, pathname, router]);

  if (!session.hydrated || (!session.token && pathname !== "/sign-in")) {
    return (
      <div className="flex h-screen items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }
  return <>{children}</>;
}
