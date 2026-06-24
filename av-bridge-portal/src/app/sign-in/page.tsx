"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { LogIn, Shield, ShieldCheck, ShieldOff, User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { buildMockToken, signIn, type SessionUser } from "@/lib/session";

// Preset mock users line up with the role mappings seeded in the cloud's
// BootstrapPoC. The Entra tenant ids and group names are the same constants
// the DB knows about, so signing in as Alice writes through to a real
// customer-scoped Principal on the cloud side.
//
// When real Entra lands, this page either disappears (replaced by a Microsoft
// sign-in redirect) or stays as a dev shortcut behind an env flag.
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

export default function SignInPage() {
  const router = useRouter();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const handleSelect = (preset: (typeof PRESETS)[number]) => {
    setBusy(preset.key);
    setError(null);
    try {
      const token = buildMockToken(preset.claims);
      signIn(token, preset.user);
      router.replace("/");
    } catch (e) {
      setError((e as Error).message);
      setBusy(null);
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
            Pick a development user to continue.
          </p>
        </div>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}

        <div className="space-y-2">
          {PRESETS.map((p) => {
            const Icon = p.icon;
            return (
              <Card key={p.key} className="cursor-pointer hover:bg-accent/40 transition-colors">
                <CardContent className="p-3">
                  <button
                    type="button"
                    onClick={() => handleSelect(p)}
                    disabled={busy !== null}
                    className="flex w-full items-center gap-3 text-left disabled:opacity-50"
                  >
                    <div className="h-9 w-9 rounded-md bg-muted flex items-center justify-center shrink-0">
                      <Icon className="h-4 w-4" />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-medium">{p.label}</div>
                      <div className="text-xs text-muted-foreground truncate">
                        {p.description}
                      </div>
                    </div>
                    <span className="text-xs text-muted-foreground">
                      {busy === p.key ? "..." : "Sign in"}
                    </span>
                  </button>
                </CardContent>
              </Card>
            );
          })}
        </div>

        <p className="text-center text-[11px] text-muted-foreground">
          Mock-JWT auth — dev scaffold. Real Microsoft Entra will plug in here
          when production-bound.
        </p>
      </div>
    </div>
  );
}
