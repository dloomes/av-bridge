"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ChevronDown, LayoutGrid, LogOut, ShieldCheck, User } from "lucide-react";
import { useSession } from "@/hooks/useSession";
import { signOut, setScope } from "@/lib/session";
import { api, type HelpdeskCustomer } from "@/lib/api";

// UserMenu — top-right dropdown showing the current user, their role, and
// (for vendor users) a customer-scope picker. Sign-out clears the session
// and pushes back to /sign-in.
export function UserMenu() {
  const session = useSession();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [customers, setCustomers] = useState<HelpdeskCustomer[]>([]);
  const ref = useRef<HTMLDivElement>(null);

  // Vendor users get the customer list so they can pick which tenant to act
  // as. Customer (non-vendor) users skip this entirely.
  useEffect(() => {
    if (!session.user?.is_vendor) return;
    const ctrl = new AbortController();
    api
      .helpdeskCustomers(ctrl.signal)
      .then(setCustomers)
      .catch(() => void 0); // silent — menu still works without the list
    return () => ctrl.abort();
  }, [session.user?.is_vendor]);

  // Click-outside to close.
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  if (!session.user) return null;

  const handleSignOut = () => {
    signOut();
    router.replace("/sign-in");
  };

  const handleScopeChange = (id: string | null) => {
    setScope(id);
    setOpen(false);
    // Force a soft refresh so polling/queries pick up the new scope.
    router.refresh();
  };

  const initials = (session.user.name ?? session.user.email ?? "?")
    .split(/\s+/)
    .map((p) => p[0])
    .slice(0, 2)
    .join("")
    .toUpperCase();

  const currentCustomer = customers.find((c) => c.id === session.scope);

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 rounded-md border bg-card px-2 py-1.5 text-sm hover:bg-accent/40"
      >
        <div className="h-6 w-6 rounded-full bg-foreground text-background text-[10px] flex items-center justify-center font-semibold">
          {initials}
        </div>
        <span className="hidden sm:inline">{session.user.name ?? session.user.email}</span>
        <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
      </button>

      {open && (
        <div className="absolute right-0 top-full mt-1 w-72 rounded-md border bg-card shadow-lg z-50">
          <div className="p-3 border-b">
            <div className="flex items-center gap-2 text-sm font-medium">
              {session.user.is_vendor ? (
                <ShieldCheck className="h-4 w-4 text-amber-500" />
              ) : (
                <User className="h-4 w-4 text-muted-foreground" />
              )}
              {session.user.name}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground truncate">
              {session.user.email}
            </div>
            <div className="mt-1.5 inline-flex items-center gap-2 text-[11px]">
              <span className="rounded bg-muted px-1.5 py-0.5 font-medium">
                {session.user.role}
              </span>
              {session.user.is_vendor && (
                <span className="rounded bg-amber-500/10 text-amber-700 dark:text-amber-300 px-1.5 py-0.5 font-medium">
                  vendor
                </span>
              )}
            </div>
          </div>

          {session.user.is_vendor && (
            <>
              <Link
                href="/helpdesk"
                onClick={() => setOpen(false)}
                className="flex items-center gap-2 px-3 py-2 text-sm border-b hover:bg-accent/40"
              >
                <LayoutGrid className="h-3.5 w-3.5" />
                Helpdesk overview
              </Link>
              <div className="p-3 border-b space-y-1">
                <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                  Acting as customer
                </div>
                <select
                  value={session.scope ?? ""}
                  onChange={(e) => handleScopeChange(e.target.value || null)}
                  className="h-8 w-full rounded-md border border-input bg-background px-2 text-sm"
                >
                  <option value="">— Unscoped (helpdesk view) —</option>
                  {customers.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
                {currentCustomer && (
                  <div className="text-[11px] text-muted-foreground">
                    Tenant: {currentCustomer.entra_tenant_id || "—"}
                  </div>
                )}
              </div>
            </>
          )}

          <button
            type="button"
            onClick={handleSignOut}
            className="flex w-full items-center gap-2 px-3 py-2 text-sm text-left hover:bg-accent/40 rounded-b-md"
          >
            <LogOut className="h-3.5 w-3.5" />
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}
