"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import { api, type Branding } from "@/lib/api";
import { hexToHslTriple } from "@/lib/hex-to-hsl";
import { useSession } from "@/hooks/useSession";

// BrandingContext hands the current tenant's logo + accent + display name
// down to consumers (page header, tab title, sign-in fallback). A `refresh`
// closure is included so the settings page can force a re-fetch after a
// successful PATCH without waiting for a page reload.
interface BrandingContextValue {
  branding: Branding;
  refresh: () => void;
}

const BrandingContext = createContext<BrandingContextValue>({
  branding: {},
  refresh: () => {},
});

// Track the previous accent so scope switches / logout restore the default.
// The bare defaults live in globals.css; we only touch the vars when a
// customer has supplied their own accent.
function applyAccent(hex: string | undefined | null) {
  const root = document.documentElement;
  if (!hex) {
    root.style.removeProperty("--primary");
    root.style.removeProperty("--ring");
    return;
  }
  const hsl = hexToHslTriple(hex);
  if (!hsl) {
    root.style.removeProperty("--primary");
    root.style.removeProperty("--ring");
    return;
  }
  root.style.setProperty("--primary", hsl);
  root.style.setProperty("--ring", hsl);
}

export function BrandingProvider({ children }: { children: React.ReactNode }) {
  const session = useSession();
  const [branding, setBranding] = useState<Branding>({});
  const [tick, setTick] = useState(0);

  const refresh = useCallback(() => setTick((n) => n + 1), []);

  // Re-fetch on token change (login / logout / re-auth) and on scope change
  // (vendor switching between customers pulls that customer's branding).
  useEffect(() => {
    if (!session.hydrated || !session.token) {
      setBranding({});
      return;
    }
    // A vendor with no scope has no tenant to pull branding for; leave
    // defaults in place so the helpdesk landing stays unbranded.
    if (session.user?.is_vendor && !session.scope) {
      setBranding({});
      return;
    }
    const ctrl = new AbortController();
    api
      .getBranding(ctrl.signal)
      .then(setBranding)
      .catch(() => {
        /* branding is best-effort; fall back to defaults on any failure */
        setBranding({});
      });
    return () => ctrl.abort();
  }, [session.hydrated, session.token, session.scope, session.user?.is_vendor, tick]);

  // Apply as side-effects. Doing this in a separate effect (rather than in
  // the fetch callback) means the accent + title also reset cleanly when
  // branding becomes {} on logout.
  useEffect(() => {
    applyAccent(branding.accent_color);
    document.title = branding.display_name || "AV Bridge";
  }, [branding]);

  return (
    <BrandingContext.Provider value={{ branding, refresh }}>
      {children}
    </BrandingContext.Provider>
  );
}

export function useBranding(): BrandingContextValue {
  return useContext(BrandingContext);
}
