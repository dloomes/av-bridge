"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import { api, type Branding } from "@/lib/api";
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

// Convert an incoming hex string (#RGB or #RRGGBB) to the "H S% L%" triple
// the Tailwind theme expects for its CSS variables. Returns null on any
// parse failure so the caller can fall back to the default palette.
function hexToHslTriple(hex: string): string | null {
  if (!hex || hex[0] !== "#") return null;
  let r = 0,
    g = 0,
    b = 0;
  if (hex.length === 4) {
    r = parseInt(hex[1] + hex[1], 16);
    g = parseInt(hex[2] + hex[2], 16);
    b = parseInt(hex[3] + hex[3], 16);
  } else if (hex.length === 7) {
    r = parseInt(hex.slice(1, 3), 16);
    g = parseInt(hex.slice(3, 5), 16);
    b = parseInt(hex.slice(5, 7), 16);
  } else {
    return null;
  }
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) return null;
  const rn = r / 255,
    gn = g / 255,
    bn = b / 255;
  const max = Math.max(rn, gn, bn);
  const min = Math.min(rn, gn, bn);
  const l = (max + min) / 2;
  let h = 0,
    s = 0;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case rn:
        h = (gn - bn) / d + (gn < bn ? 6 : 0);
        break;
      case gn:
        h = (bn - rn) / d + 2;
        break;
      case bn:
        h = (rn - gn) / d + 4;
        break;
    }
    h /= 6;
  }
  return `${Math.round(h * 360)} ${Math.round(s * 100)}% ${Math.round(l * 100)}%`;
}

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
