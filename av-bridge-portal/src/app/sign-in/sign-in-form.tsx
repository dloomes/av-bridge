"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Shield, ShieldCheck, ShieldOff, User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, type Branding } from "@/lib/api";
import { hexToHslTriple } from "@/lib/hex-to-hsl";
import { buildMockToken, landingPath, signIn, type SessionUser } from "@/lib/session";

// Human-readable labels for the ?entra_error= codes the callback sets on
// its bounce-back. The codes are ours (not Microsoft's — we normalise
// their AADSTS-* into these buckets) so a UI copy change never breaks the
// callback contract.
const ENTRA_ERROR_LABELS: Record<string, string> = {
  state_mismatch: "The sign-in link expired. Please try signing in again.",
  missing_verifier: "The sign-in link expired. Please try signing in again.",
  missing_code_or_state:
    "Microsoft didn't return the expected sign-in details. Please try again.",
  code_exchange_failed:
    "Microsoft rejected the sign-in exchange. Check the app registration or try again.",
  token_verify_failed:
    "The Microsoft token failed verification. Contact ops if this persists.",
  user_provision_failed:
    "We couldn't set up your account. Contact ops.",
  session_mint_failed:
    "Sign-in succeeded but we couldn't start your session. Try again.",
  // Customer-SSO-specific codes emitted by /api/v1/auth/entra/customer/*.
  missing_slug:
    "This sign-in link is missing a customer identifier. Please try again from your organisation's sign-in URL.",
  unknown_customer:
    "We couldn't find your organisation. Please check the sign-in URL or contact ops.",
  sso_not_configured:
    "Single sign-on isn't set up for your organisation yet. Please sign in with a password or contact ops.",
  missing_customer:
    "The sign-in link expired. Please try signing in again.",
  tid_mismatch:
    "The Microsoft account you used isn't in your organisation's Entra tenant. Sign in with a work account from that tenant.",
};

// Two sign-in paths on this page:
//
//   1. Real email+password against POST /api/v1/auth/login. This is the
//      non-Entra production path — customers who don't federate use it, and
//      the vendor helpdesk uses it by default until Entra is wired.
//
//   2. Mock-JWT presets, gated behind NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS.
//      They mint a `mock.<base64>` token with fixed identities that resolve
//      through the mock resolver on the cloud side.
const PRESETS: Array<{
  key: string;
  label: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  claims: {
    sub: string;
    email: string;
    name: string;
    tid: string;
    groups: string[];
  };
  user: SessionUser;
}> = [
  {
    key: "alice",
    label: "Alice Admin",
    description: "POC Customer · admin (full read/write)",
    icon: ShieldCheck,
    claims: {
      sub: "alice@acme.com",
      email: "alice@acme.com",
      name: "Alice Admin",
      tid: "tenant-acme-poc",
      groups: ["poc-admins"],
    },
    user: {
      user_id: "alice@acme.com",
      email: "alice@acme.com",
      name: "Alice Admin",
      role: "admin",
    },
  },
  {
    key: "bob",
    label: "Bob Operator",
    description: "POC Customer · operator (commands + reads)",
    icon: Shield,
    claims: {
      sub: "bob@acme.com",
      email: "bob@acme.com",
      name: "Bob Operator",
      tid: "tenant-acme-poc",
      groups: ["poc-operators"],
    },
    user: {
      user_id: "bob@acme.com",
      email: "bob@acme.com",
      name: "Bob Operator",
      role: "operator",
    },
  },
  {
    key: "casey",
    label: "Casey Viewer",
    description: "POC Customer · viewer (read-only)",
    icon: User,
    claims: {
      sub: "casey@acme.com",
      email: "casey@acme.com",
      name: "Casey Viewer",
      tid: "tenant-acme-poc",
      groups: ["poc-viewers"],
    },
    user: {
      user_id: "casey@acme.com",
      email: "casey@acme.com",
      name: "Casey Viewer",
      role: "viewer",
    },
  },
  {
    key: "sam",
    label: "Sam Helpdesk",
    description: "Involve (vendor) · cross-customer access",
    icon: ShieldOff,
    claims: {
      sub: "sam@involve.vc",
      email: "sam@involve.vc",
      name: "Sam Helpdesk",
      tid: "tenant-involve-vendor",
      groups: ["helpdesk-admins"],
    },
    user: {
      user_id: "sam@involve.vc",
      email: "sam@involve.vc",
      name: "Sam Helpdesk",
      role: "admin",
      is_vendor: true,
    },
  },
];

const DEV_SHORTCUTS_ENABLED =
  process.env.NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS === "true";

// Signal-bars brand mark. Three ascending vertical bars — reads as "signal
// strength / live monitoring". Used when the customer hasn't uploaded a
// logo. The tallest bar uses `fill-primary`, so a customer accent flows
// through it automatically.
function SignalBarsMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 20 20"
      fill="none"
      aria-hidden="true"
      className={className}
    >
      <rect x="3" y="12" width="3" height="5" rx="0.8" className="fill-sidebar-foreground/40" />
      <rect x="8.5" y="8" width="3" height="9" rx="0.8" className="fill-sidebar-foreground/85" />
      <rect x="14" y="4" width="3" height="13" rx="0.8" className="fill-primary" />
    </svg>
  );
}

// Compact two-letter derivation for the mark fallback when a customer has
// a display_name but no uploaded logo. "Acme Corp" → "AC", "Initech" → "I".
function initialsFor(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean).slice(0, 2);
  const joined = words.map((w) => w[0]?.toUpperCase() ?? "").join("");
  return joined || "·";
}

// SupportLink renders the customer-configured support contact as a real
// link when it parses as one (email or URL) and as plain text otherwise.
// Very forgiving — we don't want to block the vendor from typing "call
// the ops desk on ext 4321" as their support line.
function SupportLink({ value }: { value: string }) {
  const trimmed = value.trim();
  if (/^https?:\/\//i.test(trimmed)) {
    return (
      <a href={trimmed} className="text-foreground underline decoration-border underline-offset-2 hover:decoration-primary">
        {trimmed}
      </a>
    );
  }
  // Loose email test — good enough to trigger mailto: without pretending to
  // be a full RFC 5322 validator.
  if (/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(trimmed)) {
    return (
      <a href={`mailto:${trimmed}`} className="text-foreground underline decoration-border underline-offset-2 hover:decoration-primary">
        {trimmed}
      </a>
    );
  }
  return <span className="text-foreground">{trimmed}</span>;
}

interface SignInFormProps {
  branding: Branding;
  // True on the unbranded portal origin (app.<env>.involvecloud.com) —
  // shows the vendor SSO tile. False on branded customer subdomains,
  // where the customer-SSO tile takes over when branding.sso_available.
  showVendorSSO?: boolean;
  // Customer slug when this page is rendered under a branded subdomain
  // (e.g. "acme"). Combined with appOrigin to build the customer SSO
  // authorize URL. Absent on the unbranded app.<env> origin.
  slug?: string;
  // Fully-qualified origin of the unbranded portal (e.g.
  // "https://app.uat.involvecloud.com") — where the customer SSO tile
  // needs to point so authorize + callback land same-origin (single
  // Entra redirect_uri, cookie domain sanity). Empty string falls back
  // to a relative link.
  appOrigin?: string;
}

export function SignInForm({ branding, showVendorSSO = false, slug, appOrigin = "" }: SignInFormProps) {
  // SSO-only mode: the customer has flipped M4's toggle AND their tenant
  // has SSO available. The password form is hidden entirely — Microsoft
  // becomes the only route in. Only ever true on a branded customer page
  // (the vendor path uses showVendorSSO which is orthogonal). Absent
  // sso_available (older cloud, missing config) collapses to false and
  // the form stays, matching the pre-M4 default.
  const ssoOnlyMode =
    !showVendorSSO &&
    !!slug &&
    !!branding.sso_required &&
    !!branding.sso_available;
  const router = useRouter();
  const searchParams = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [presetBusy, setPresetBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Surface Entra callback errors as an inline banner on first mount. The
  // Entra callback route bounces here with `?entra_error=<code>` when the
  // OAuth handshake fails (bad state, verify failure, etc.), then the URL
  // param stays around — we translate it once, then wipe it so a browser
  // reload doesn't re-surface the banner without reason.
  useEffect(() => {
    const code = searchParams.get("entra_error");
    if (!code) return;
    setError(
      ENTRA_ERROR_LABELS[code] ??
        `Sign-in via Microsoft failed (${code}). Please try again.`
    );
    // Strip the param without triggering navigation.
    const url = new URL(window.location.href);
    url.searchParams.delete("entra_error");
    window.history.replaceState({}, "", url.toString());
  }, [searchParams]);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const { token, role } = await api.login(email.trim(), password);
      // The token is enough — /whoami will fill in the rest on the next tick,
      // but we seed a SessionUser now so the immediate redirect renders with
      // the right role gating instead of flashing viewer defaults.
      signIn(token, { email: email.trim(), role });
      let landing = "/";
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
          landing_page: who.landing_page,
        });
        landing = landingPath(who.landing_page);
      } catch {}
      router.replace(landing);
    } catch (err) {
      setError((err as Error).message || "Sign-in failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handlePreset = (preset: (typeof PRESETS)[number]) => {
    setPresetBusy(preset.key);
    setError(null);
    try {
      const token = buildMockToken(preset.claims);
      signIn(token, preset.user);
      router.replace("/");
    } catch (e) {
      setError((e as Error).message);
      setPresetBusy(null);
    }
  };

  // Customer accent → --primary override scoped to this page. Applied inline
  // on the root so the first paint already has the correct hue — no FOUC of
  // the default portal blue. hexToHslTriple returns null on bad input, in
  // which case we leave the default palette in place.
  const accentHsl = branding.accent_color
    ? hexToHslTriple(branding.accent_color)
    : null;
  const rootStyle = accentHsl
    ? ({ ["--primary"]: accentHsl, ["--ring"]: accentHsl } as React.CSSProperties)
    : undefined;

  const displayName = branding.display_name?.trim() || "";
  const hasLogo = Boolean(branding.logo_data_url);
  const heroTitle = displayName
    ? `Welcome back to ${displayName}.`
    : "Welcome back.";
  const buttonLabel = submitting
    ? "Signing in…"
    : displayName
    ? `Sign in to ${displayName}`
    : "Sign in to av-bridge";

  return (
    <div
      style={rootStyle}
      className="min-h-screen grid md:grid-cols-[1.1fr_1fr] bg-background text-foreground"
    >
      {/* ═══════════ LEFT: brand + hero (dark) ═══════════ */}
      <section
        className="relative overflow-hidden bg-sidebar text-sidebar-foreground px-8 py-10 md:px-16 md:py-14 flex flex-col justify-between min-h-[380px] md:min-h-0"
        style={
          branding.sign_in_hero_data_url
            ? {
                backgroundImage: `linear-gradient(180deg, hsl(var(--sidebar) / 0.72), hsl(var(--sidebar) / 0.88)), url("${branding.sign_in_hero_data_url}")`,
                backgroundSize: "cover",
                backgroundPosition: "center",
              }
            : undefined
        }
      >
        {/* Accent halo bleeding from bottom-right */}
        <div
          aria-hidden="true"
          className="absolute -right-64 -bottom-64 w-[640px] h-[640px] pointer-events-none opacity-60 blur-2xl"
          style={{
            background:
              "radial-gradient(closest-side, hsl(var(--primary) / 0.32), transparent 70%)",
          }}
        />

        {/* Faint engineered grid */}
        <div
          aria-hidden="true"
          className="absolute inset-0 pointer-events-none"
          style={{
            backgroundImage:
              "linear-gradient(to right, rgba(255,255,255,0.028) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,0.028) 1px, transparent 1px)",
            backgroundSize: "48px 48px",
          }}
        />

        {/* Giant ghosted signal-bars motif — fills space with brand, not chrome */}
        <div
          aria-hidden="true"
          className="absolute -right-20 -bottom-32 w-[560px] h-[560px] pointer-events-none opacity-[0.08] md:opacity-[0.09] hidden md:block"
        >
          <svg viewBox="0 0 240 240" fill="none" className="w-full h-full">
            <rect x="30" y="150" width="40" height="60" rx="6" className="fill-sidebar-foreground" />
            <rect x="100" y="90" width="40" height="120" rx="6" className="fill-sidebar-foreground" />
            <rect x="170" y="30" width="40" height="180" rx="6" className="fill-primary" />
          </svg>
        </div>

        {/* Brand row — logo tile + wordmark. Three states:
              1. No branding                  → signal-bars mark + "av/bridge"
              2. display_name, no logo        → initials tile + "<name> · on av-bridge"
              3. logo_data_url present         → uploaded logo + "<name> · on av-bridge"
            The tile has fixed footprint (36px) either way so the layout doesn't
            reflow when branding hydrates. */}
        <div className="relative flex items-center gap-3 animate-fade-in">
          {hasLogo ? (
            <div className="h-9 w-9 rounded-md bg-sidebar-foreground/5 border border-sidebar-foreground/10 overflow-hidden flex items-center justify-center">
              {/* Logos are stored as data: URIs by the cloud, so no CORS or
                  loader concern — safe to render as a plain <img>. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                src={branding.logo_data_url}
                alt={displayName ? `${displayName} logo` : "Customer logo"}
                className="h-full w-full object-contain"
              />
            </div>
          ) : displayName ? (
            <div className="h-9 w-9 rounded-md bg-primary text-primary-foreground border border-transparent flex items-center justify-center font-medium text-[13.5px] tracking-tight">
              {initialsFor(displayName)}
            </div>
          ) : (
            <div className="h-9 w-9 rounded-md bg-sidebar-foreground/5 border border-sidebar-foreground/10 flex items-center justify-center shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]">
              <SignalBarsMark className="h-5 w-5" />
            </div>
          )}

          {displayName ? (
            <div className="flex items-baseline gap-2">
              <span className="text-[17px] font-semibold tracking-tight">
                {displayName}
              </span>
              <span className="font-mono text-[11px] tracking-wider text-sidebar-foreground/50">
                on av-bridge
              </span>
            </div>
          ) : (
            <div className="font-mono text-sm tracking-tight">
              av<span className="mx-1 text-sidebar-foreground/40">/</span>bridge
            </div>
          )}
        </div>

        {/* Hero */}
        <div className="relative max-w-md animate-fade-in [animation-delay:120ms] [animation-fill-mode:both]">
          <div className="mb-5 flex items-center gap-2.5 font-mono text-[11px] tracking-[0.14em] uppercase text-sidebar-foreground/45">
            <span className="h-px w-6 bg-sidebar-foreground/40" />
            {displayName ? `${displayName} · AV Fleet` : "AV Fleet Monitoring"}
          </div>
          <h1 className="text-4xl md:text-5xl font-medium leading-[1.05] tracking-tight mb-5">
            One pane
            <br />
            for every{" "}
            <span className="italic font-serif text-primary">signal.</span>
          </h1>
          <p className="text-[15px] md:text-[15.5px] leading-relaxed text-sidebar-foreground/70 max-w-md">
            Live health across every site, room, and device — from the
            boardroom camera to the auditorium DSP.
          </p>
        </div>

        {/* Foot: quiet operational marker + brand line */}
        <div className="relative flex items-center gap-6 font-mono text-[10.5px] tracking-wider text-sidebar-foreground/45 animate-fade-in [animation-delay:280ms] [animation-fill-mode:both]">
          <span className="inline-flex items-center gap-2">
            <span
              className="h-1.5 w-1.5 rounded-full bg-success"
              style={{ boxShadow: "0 0 8px hsl(var(--success) / 0.55)" }}
              aria-hidden="true"
            />
            All systems operational
          </span>
          <span>© involve · av-bridge</span>
        </div>
      </section>

      {/* ═══════════ RIGHT: sign-in form (light) ═══════════ */}
      <section
        className="relative flex flex-col bg-background px-8 py-10 md:px-16 md:py-14"
        style={{
          backgroundImage:
            "radial-gradient(circle, hsl(var(--foreground) / 0.03) 1px, transparent 1px)",
          backgroundSize: "22px 22px",
        }}
      >
        {/* Top row: talk-to-sales — the only chrome */}
        <div className="flex justify-end items-center gap-2 font-mono text-[11px] text-muted-foreground animate-fade-in">
          <span>Need an account?</span>
          <a
            href="#"
            className="ml-1 rounded-md border border-border bg-background px-2.5 py-1.5 text-foreground/80 no-underline transition-colors hover:border-input hover:bg-muted hover:text-foreground"
          >
            Talk to sales →
          </a>
        </div>

        {/* Form column — vertically centred in remaining space */}
        <div className="flex-1 flex items-center justify-center py-10">
          <div className="w-full max-w-sm animate-fade-in [animation-delay:180ms] [animation-fill-mode:both]">
            <div className="mb-3 font-mono text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
              Sign in
            </div>
            <h2 className="mb-2 text-3xl font-medium tracking-tight text-foreground">
              {heroTitle}
            </h2>
            <p className="mb-8 max-w-[340px] text-sm text-muted-foreground">
              {branding.sign_in_message?.trim() ||
                "Enter your credentials to access your organisation’s dashboard."}
            </p>

            {error && (
              <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
                {error}
              </div>
            )}

            {ssoOnlyMode && (
              <div className="mb-6 rounded-md border border-border bg-muted/30 px-3 py-3 text-xs text-muted-foreground">
                Your organisation requires sign-in with Microsoft. Use the
                button below.
              </div>
            )}
            {!ssoOnlyMode && (
            <form onSubmit={handleLogin} className="flex flex-col gap-[18px]">
              <div className="flex flex-col gap-1.5 group">
                <label
                  htmlFor="email"
                  className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground transition-colors group-focus-within:text-primary"
                >
                  Work email
                </label>
                <input
                  id="email"
                  type="email"
                  autoComplete="username"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="alex@acme.com"
                  disabled={submitting}
                  className="h-[46px] rounded-md border border-border bg-background px-3.5 text-[15px] text-foreground placeholder:text-muted-foreground outline-none transition-all hover:border-input focus:border-primary focus:bg-card focus:shadow-[0_0_0_4px_hsl(var(--primary)/0.12)]"
                />
              </div>

              <div className="flex flex-col gap-1.5 group">
                <label
                  htmlFor="password"
                  className="flex items-baseline justify-between font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground transition-colors group-focus-within:text-primary"
                >
                  <span>Password</span>
                  <Link
                    href="/forgot-password"
                    className="font-sans text-xs normal-case tracking-normal text-foreground/70 no-underline border-b border-border hover:text-primary hover:border-primary transition-colors"
                  >
                    Forgot?
                  </Link>
                </label>
                <input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  disabled={submitting}
                  className="h-[46px] rounded-md border border-border bg-background px-3.5 text-[15px] text-foreground placeholder:text-muted-foreground outline-none transition-all hover:border-input focus:border-primary focus:bg-card focus:shadow-[0_0_0_4px_hsl(var(--primary)/0.12)]"
                />
              </div>

              <Button
                type="submit"
                disabled={submitting}
                className="group mt-1 h-12 w-full text-[14.5px] font-medium"
              >
                <span>{buttonLabel}</span>
                <svg
                  width="14"
                  height="14"
                  viewBox="0 0 14 14"
                  fill="none"
                  aria-hidden="true"
                  className="transition-transform group-hover:translate-x-1"
                >
                  <path
                    d="M2 7h10M8 3l4 4-4 4"
                    stroke="currentColor"
                    strokeWidth="1.6"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              </Button>
            </form>
            )}

            {/* Divider — hidden in SSO-only mode since the form above is gone */}
            {!ssoOnlyMode && (
              <div className="my-7 flex items-center gap-3 font-mono text-[10.5px] uppercase tracking-[0.14em] text-muted-foreground">
                <span className="h-px flex-1 bg-border" />
                or
                <span className="h-px flex-1 bg-border" />
              </div>
            )}

            {showVendorSSO ? (
              // Vendor SSO — live tile. Routes through the portal's own
              // /api/v1 proxy so the browser origin stays app.<env>,
              // which keeps the state/PKCE cookies readable on callback
              // (they'd be lost across a cross-origin bounce).
              <a
                href="/api/v1/auth/entra/vendor/authorize"
                className="flex items-center gap-3 rounded-md border border-border bg-background px-4 py-3.5 text-sm text-foreground no-underline transition-colors hover:border-input hover:bg-muted"
              >
                <div className="grid h-7 w-7 place-items-center rounded-md bg-muted text-muted-foreground shrink-0">
                  {/* Microsoft four-square mark, mono-tone so it inherits the tile colour */}
                  <svg width="14" height="14" viewBox="0 0 14 14" fill="currentColor" aria-hidden="true">
                    <rect x="1" y="1" width="5.5" height="5.5" />
                    <rect x="7.5" y="1" width="5.5" height="5.5" opacity="0.72" />
                    <rect x="1" y="7.5" width="5.5" height="5.5" opacity="0.72" />
                    <rect x="7.5" y="7.5" width="5.5" height="5.5" opacity="0.5" />
                  </svg>
                </div>
                <div className="flex-1 min-w-0">
                  <div className="text-[13.5px] font-medium text-foreground">
                    {branding.sso_button_label?.trim() || "Sign in with Microsoft"}
                  </div>
                  <div className="mt-0.5 font-mono text-[10.5px] uppercase tracking-wider text-muted-foreground">
                    Vendor · Entra ID
                  </div>
                </div>
                <span className="text-foreground/50">→</span>
              </a>
            ) : branding.sso_available && slug ? (
              // Customer SSO — live tile. Cross-origin link to app.<env>
              // so authorize + Microsoft's callback land on the same
              // origin (the multi-tenant app reg has one registered
              // redirect_uri; cookies scoped to that host). The customer
              // handler on the cloud reads the slug, looks up their
              // entra_tenant_id, and routes the OIDC flow to their
              // Entra tenant. The Microsoft mark matches the vendor
              // tile for visual consistency.
              <a
                href={`${appOrigin}/api/v1/auth/entra/customer/authorize?slug=${encodeURIComponent(slug)}`}
                className="flex items-center gap-3 rounded-md border border-border bg-background px-4 py-3.5 text-sm text-foreground no-underline transition-colors hover:border-input hover:bg-muted"
              >
                <div className="grid h-7 w-7 place-items-center rounded-md bg-muted text-muted-foreground shrink-0">
                  <svg width="14" height="14" viewBox="0 0 14 14" fill="currentColor" aria-hidden="true">
                    <rect x="1" y="1" width="5.5" height="5.5" />
                    <rect x="7.5" y="1" width="5.5" height="5.5" opacity="0.72" />
                    <rect x="1" y="7.5" width="5.5" height="5.5" opacity="0.72" />
                    <rect x="7.5" y="7.5" width="5.5" height="5.5" opacity="0.5" />
                  </svg>
                </div>
                <div className="flex-1 min-w-0">
                  <div className="text-[13.5px] font-medium text-foreground">
                    {branding.sso_button_label?.trim() || "Sign in with Microsoft"}
                  </div>
                  <div className="mt-0.5 font-mono text-[10.5px] uppercase tracking-wider text-muted-foreground">
                    {branding.display_name?.trim() || "Entra ID"}
                  </div>
                </div>
                <span className="text-foreground/50">→</span>
              </a>
            ) : (
              // Branded page, but the customer has no entra_tenant_id set
              // yet OR the cloud isn't configured for customer SSO. Keep
              // the inert affordance so people know SSO is a thing they
              // can ask ops to enrol.
              <div
                role="button"
                aria-disabled="true"
                className="flex items-center gap-3 rounded-md border border-dashed border-input bg-transparent px-4 py-3.5 text-sm text-foreground/70 cursor-not-allowed"
              >
                <div className="grid h-7 w-7 place-items-center rounded-md bg-muted text-muted-foreground shrink-0">
                  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                    <rect
                      x="2.5"
                      y="6"
                      width="9"
                      height="6.5"
                      rx="1.2"
                      stroke="currentColor"
                      strokeWidth="1.3"
                    />
                    <path
                      d="M4.5 6V4.5a2.5 2.5 0 0 1 5 0V6"
                      stroke="currentColor"
                      strokeWidth="1.3"
                    />
                  </svg>
                </div>
                <div className="flex-1 min-w-0">
                  <div className="text-[13.5px] font-medium text-foreground">
                    {branding.sso_button_label?.trim() || "Single sign-on via Entra ID"}
                  </div>
                  <div className="mt-0.5 font-mono text-[10.5px] uppercase tracking-wider text-muted-foreground">
                    Contact ops to enrol
                  </div>
                </div>
              </div>
            )}

            {branding.support_contact?.trim() && (
              <div className="mt-6 border-t border-border pt-4 text-center text-xs text-muted-foreground">
                Need help?{" "}
                <SupportLink value={branding.support_contact.trim()} />
              </div>
            )}

            {DEV_SHORTCUTS_ENABLED && (
              <details className="mt-6 rounded-md border border-border bg-card">
                <summary className="cursor-pointer select-none px-3 py-2 font-mono text-[11px] uppercase tracking-wider text-muted-foreground">
                  Dev shortcuts (mock JWT)
                </summary>
                <div className="border-t border-border p-2 space-y-1">
                  {PRESETS.map((p) => {
                    const Icon = p.icon;
                    return (
                      <button
                        key={p.key}
                        type="button"
                        onClick={() => handlePreset(p)}
                        disabled={presetBusy !== null}
                        className="flex w-full items-center gap-3 rounded-md p-2 text-left transition-colors hover:bg-accent/60 disabled:opacity-50"
                      >
                        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted">
                          <Icon className="h-4 w-4" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="text-sm font-medium">{p.label}</div>
                          <div className="truncate text-xs text-muted-foreground">
                            {p.description}
                          </div>
                        </div>
                        <span className="text-xs text-muted-foreground">
                          {presetBusy === p.key ? "…" : "→"}
                        </span>
                      </button>
                    );
                  })}
                </div>
              </details>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}
