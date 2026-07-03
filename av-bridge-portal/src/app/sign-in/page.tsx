"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { LogIn, Shield, ShieldCheck, ShieldOff, User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import { buildMockToken, signIn, type SessionUser } from "@/lib/session";

// Two sign-in paths on this page:
//
//   1. Real email+password against POST /api/v1/auth/login. This is the
//      non-Entra production path — customers who don't federate use it, and
//      the vendor helpdesk uses it by default until Entra is wired.
//
//   2. Mock-JWT presets below the fold. Kept for dev — they mint a
//      `mock.<base64>` token with fixed identities that resolve through the
//      mock resolver on the cloud side. Real deployments hide these behind
//      NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS=true.
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

// Opt-in — hidden by default so demos and screen-shares never see the mock
// personas. Set NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS=true in .env.local
// during development to restore the "Alice/Bob/Casey/Sam" preset chooser.
const DEV_SHORTCUTS_ENABLED =
  process.env.NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS === "true";

export default function SignInPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [presetBusy, setPresetBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

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
      // Fire /whoami to populate name/customer_id/is_vendor before the app
      // shell renders. Best-effort: any failure just leaves the seeded user.
      try {
        const who = await api.whoami();
        signIn(token, {
          user_id: who.user_id,
          email: who.email,
          name: who.name,
          customer_id: who.customer_id,
          role: who.role,
          is_vendor: who.is_vendor,
        });
      } catch {}
      router.replace("/");
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

  return (
    <div className="min-h-screen flex items-center justify-center p-6 bg-muted/30">
      <div className="w-full max-w-md space-y-4">
        <div className="text-center space-y-1">
          <div className="inline-flex items-center justify-center h-10 w-10 rounded-lg bg-foreground text-background mb-2">
            <LogIn className="h-5 w-5" />
          </div>
          <h1 className="text-xl font-semibold">AV Bridge</h1>
          <p className="text-sm text-muted-foreground">
            Sign in to continue.
          </p>
        </div>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}

        <Card>
          <CardContent className="p-4">
            <form onSubmit={handleLogin} className="space-y-3">
              <div className="space-y-1">
                <label htmlFor="email" className="text-xs font-medium text-muted-foreground">
                  Email
                </label>
                <input
                  id="email"
                  type="email"
                  autoComplete="username"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  disabled={submitting}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="password" className="text-xs font-medium text-muted-foreground">
                  Password
                </label>
                <input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  disabled={submitting}
                />
              </div>
              <Button type="submit" disabled={submitting} className="w-full">
                {submitting ? "Signing in…" : "Sign in"}
              </Button>
            </form>
          </CardContent>
        </Card>

        {DEV_SHORTCUTS_ENABLED && (
          <details className="rounded-md border bg-card">
            <summary className="cursor-pointer px-3 py-2 text-xs text-muted-foreground select-none">
              Dev shortcuts (mock JWT)
            </summary>
            <div className="border-t p-2 space-y-1">
              {PRESETS.map((p) => {
                const Icon = p.icon;
                return (
                  <button
                    key={p.key}
                    type="button"
                    onClick={() => handlePreset(p)}
                    disabled={presetBusy !== null}
                    className="flex w-full items-center gap-3 rounded-md p-2 text-left hover:bg-accent/40 disabled:opacity-50"
                  >
                    <div className="h-8 w-8 rounded-md bg-muted flex items-center justify-center shrink-0">
                      <Icon className="h-4 w-4" />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-medium">{p.label}</div>
                      <div className="text-xs text-muted-foreground truncate">
                        {p.description}
                      </div>
                    </div>
                    <span className="text-xs text-muted-foreground">
                      {presetBusy === p.key ? "..." : "→"}
                    </span>
                  </button>
                );
              })}
            </div>
          </details>
        )}
      </div>
    </div>
  );
}
