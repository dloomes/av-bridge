"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { AlertTriangle, Download, Loader2, ShieldCheck, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { BackLink } from "@/components/back-link";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import type { EraseResult, HelpdeskCustomer } from "@/lib/api";
import { hasPermission } from "@/lib/session";

// Data requests: export everything held about one person as a ZIP, or erase
// them (their name, email and sign-in details are replaced; the activity
// record keeps a stand-in). Erase is two steps: Preview shows what would
// change, then you type the email again to confirm.
//
// With ?helpdesk=1 (Involve staff only) the page also asks which customer,
// or Involve staff, the person belongs to.

const inputCls =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

// Matches the cloud's own check: confirm equals the email, ignoring case.
const sameEmail = (a: string, b: string) =>
  a.trim().length > 0 && a.trim().toLowerCase() === b.trim().toLowerCase();

// ApiError messages look like `404 Not Found: {"error":"..."}`. Show just the
// sentence when there is one.
function plainError(e: unknown): string {
  const msg = (e as Error)?.message ?? "Something went wrong";
  const i = msg.indexOf("{");
  if (i >= 0) {
    try {
      const j = JSON.parse(msg.slice(i));
      if (typeof j?.error === "string") return j.error;
    } catch {}
  }
  return msg;
}

const humanise = (key: string) => {
  const s = key.replace(/[_.]+/g, " ").trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
};

type Target = { customerId?: string; vendor?: boolean };

export default function DataRequestsPage() {
  return (
    <Suspense fallback={null}>
      <DataRequests />
    </Suspense>
  );
}

function DataRequests() {
  const session = useSession();
  const params = useSearchParams();
  const helpdesk = params.get("helpdesk") === "1";
  const isVendor = !!session.user?.is_vendor;
  const allowed = helpdesk
    ? isVendor
    : isVendor || hasPermission(session.user, "privacy.manage");

  const router = useRouter();
  const pathname = usePathname();
  // Read ?email= once, then take it out of the URL so the address isn't
  // left in history or the address bar.
  const [initialEmail, setInitialEmail] = useState(() => params.get("email") ?? "");
  const action = params.get("action");
  useEffect(() => {
    if (!params.has("email")) return;
    const next = new URLSearchParams(params.toString());
    next.delete("email");
    const qs = next.toString();
    router.replace(qs ? `${pathname}?${qs}` : pathname);
  }, [params, pathname, router]);

  // Helpdesk target: a customer id, or "vendor" for Involve staff.
  const [customers, setCustomers] = useState<HelpdeskCustomer[]>([]);
  const [target, setTarget] = useState("");
  useEffect(() => {
    if (!helpdesk || !isVendor) return;
    const ctrl = new AbortController();
    api
      .helpdeskCustomers(ctrl.signal)
      .then((c) => setCustomers(c))
      .catch(() => {});
    return () => ctrl.abort();
  }, [helpdesk, isVendor]);

  const targetReady = !helpdesk || target !== "";
  const targetArg: Target = target === "vendor" ? { vendor: true } : { customerId: target };

  const eraseRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (session.hydrated && action === "erase") {
      eraseRef.current?.scrollIntoView({ block: "start" });
    }
  }, [action, session.hydrated]);

  if (!session.hydrated) return null;

  if (!allowed) {
    return (
      <div className="min-h-app">
        <div className="max-w-3xl mx-auto p-6">
          <Card>
            <CardContent className="p-8 text-center text-sm text-muted-foreground">
              You don&apos;t have permission to manage data requests.
            </CardContent>
          </Card>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-app">
      <div className="max-w-3xl mx-auto p-4 sm:p-6 space-y-4">
        <header className="flex items-center gap-3">
          {helpdesk && (
            <Button asChild variant="ghost" size="sm" className="shrink-0">
              <BackLink href="/helpdesk" label="Helpdesk" iconClassName="h-3.5 w-3.5" />
            </Button>
          )}
          <div className="h-9 w-9 shrink-0 rounded-md bg-primary/10 flex items-center justify-center">
            <ShieldCheck aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold leading-tight">Data requests</h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Export or erase the personal data held about one person.
            </p>
          </div>
        </header>

        {helpdesk && (
          <Card>
            <CardContent className="p-4 space-y-1">
              <label htmlFor="dr-target" className="text-xs font-medium text-muted-foreground">
                Whose data is it?
              </label>
              <select
                id="dr-target"
                value={target}
                onChange={(e) => {
                  setTarget(e.target.value);
                  // The prefill belongs to the first view only.
                  setInitialEmail("");
                }}
                className={inputCls}
              >
                <option value="">Choose a customer…</option>
                {customers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
                <option value="vendor">Involve staff</option>
              </select>
            </CardContent>
          </Card>
        )}

        <ExportCard
          key={`export-${target}`}
          initialEmail={action === "export" ? initialEmail : ""}
          disabled={!targetReady}
          run={(email) =>
            helpdesk
              ? api.helpdeskPrivacyAccessExport(email, targetArg)
              : api.privacyAccessExport(email)
          }
        />

        <div ref={eraseRef} className="scroll-mt-4">
          <EraseCard
            key={`erase-${target}`}
            initialEmail={action === "erase" ? initialEmail : ""}
            disabled={!targetReady}
            run={(req) =>
              helpdesk ? api.helpdeskPrivacyErase(req, targetArg) : api.privacyErase(req)
            }
          />
        </div>
      </div>
    </div>
  );
}

function ExportCard({
  initialEmail,
  disabled,
  run,
}: {
  initialEmail: string;
  disabled: boolean;
  run: (email: string) => Promise<{ blob: Blob; filename: string }>;
}) {
  const { toast } = useToast();
  const [email, setEmail] = useState(initialEmail);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const { blob, filename } = await run(email.trim());
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      toast({ title: `Downloaded ${filename}`, variant: "success" });
    } catch (err) {
      setError(plainError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardContent className="p-4 sm:p-5">
        <form onSubmit={go} className="space-y-3">
          <div>
            <h2 className="text-base font-semibold">Export a person&apos;s data</h2>
            <p className="text-sm text-muted-foreground">
              Downloads a ZIP of everything held about the person with this email address.
            </p>
          </div>
          <div className="space-y-1">
            <label htmlFor="export-email" className="text-xs font-medium text-muted-foreground">
              Email address
            </label>
            <input
              id="export-email"
              type="email"
              autoComplete="off"
              disabled={busy}
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                setError(null);
              }}
              placeholder="name@example.com"
              className={inputCls}
            />
          </div>
          {error && <ErrorLine text={error} />}
          <Button type="submit" disabled={busy || disabled || !email.trim()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
            Download ZIP
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function EraseCard({
  initialEmail,
  disabled,
  run,
}: {
  initialEmail: string;
  disabled: boolean;
  run: (req: { email: string; confirm: string; dry_run: boolean }) => Promise<EraseResult>;
}) {
  const { toast } = useToast();
  const [email, setEmail] = useState(initialEmail);
  const [confirm, setConfirm] = useState("");
  const [preview, setPreview] = useState<EraseResult | null>(null);
  // The address the preview was run for; Erase is only offered for it.
  const [previewedEmail, setPreviewedEmail] = useState("");
  // Bumped whenever the flow is reset, so a request that was still running
  // can't put its result back.
  const reqId = useRef(0);
  const confirmRef = useRef<HTMLInputElement>(null);
  const [done, setDone] = useState<EraseResult | null>(null);
  const [busy, setBusy] = useState<"preview" | "erase" | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (preview) confirmRef.current?.focus();
  }, [preview]);

  const reset = () => {
    reqId.current++;
    setPreview(null);
    setPreviewedEmail("");
    setDone(null);
    setConfirm("");
    setError(null);
  };

  // The cloud wants the confirm text on a preview too; a preview changes
  // nothing, so the email itself is sent for it.
  const doPreview = async (e: React.FormEvent) => {
    e.preventDefault();
    const em = email.trim();
    if (!em) return;
    setBusy("preview");
    reset();
    const id = reqId.current;
    try {
      const r = await run({ email: em, confirm: em, dry_run: true });
      if (id !== reqId.current) return;
      setPreview(r);
      setPreviewedEmail(em);
    } catch (err) {
      if (id === reqId.current) setError(plainError(err));
    } finally {
      if (id === reqId.current) setBusy(null);
    }
  };

  const canErase =
    preview !== null &&
    sameEmail(previewedEmail, email) &&
    sameEmail(confirm, previewedEmail);

  const doErase = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!canErase) return;
    setBusy("erase");
    setError(null);
    const id = reqId.current;
    try {
      const r = await run({ email: previewedEmail, confirm: confirm.trim(), dry_run: false });
      if (id !== reqId.current) return;
      // Clear the form but keep the result showing.
      reset();
      setBusy(null);
      setEmail("");
      setDone(r);
      toast({ title: "Person erased", variant: "success" });
    } catch (err) {
      if (id === reqId.current) setError(plainError(err));
    } finally {
      if (id === reqId.current) setBusy(null);
    }
  };

  return (
    <Card>
      <CardContent className="p-4 sm:p-5 space-y-4">
        <div>
          <h2 className="text-base font-semibold">Erase a person</h2>
          <p className="text-sm text-muted-foreground">
            Replaces their name, email and sign-in details with a stand-in. This can&apos;t be undone.
            Preview first to see what will change.
          </p>
        </div>

        <form onSubmit={doPreview} className="space-y-3">
          <div className="space-y-1">
            <label htmlFor="erase-email" className="text-xs font-medium text-muted-foreground">
              Email address
            </label>
            <input
              id="erase-email"
              type="email"
              autoComplete="off"
              disabled={busy !== null}
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                reset();
              }}
              placeholder="name@example.com"
              className={inputCls}
            />
          </div>
          <Button
            type="submit"
            variant="outline"
            disabled={busy !== null || disabled || !email.trim()}
          >
            {busy === "preview" && <Loader2 className="h-4 w-4 animate-spin" />}
            Preview
          </Button>
        </form>

        {error && <ErrorLine text={error} />}

        {preview && (
          <form onSubmit={doErase} className="space-y-3">
            <p className="text-sm font-medium">What will change</p>
            <Result r={preview} />
            <div className="space-y-1">
              <label htmlFor="erase-confirm" className="text-xs font-medium text-muted-foreground">
                Type the email address to confirm
              </label>
              <input
                id="erase-confirm"
                ref={confirmRef}
                type="email"
                autoComplete="off"
                disabled={busy !== null}
                value={confirm}
                onChange={(e) => {
                  setConfirm(e.target.value);
                  setError(null);
                }}
                placeholder={email.trim()}
                className={inputCls}
              />
            </div>
            <Button
              type="submit"
              variant="destructive"
              disabled={busy !== null || !canErase}
            >
              {busy === "erase" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
              Erase
            </Button>
          </form>
        )}

        {done && (
          <div className="space-y-3">
            <p className="text-sm font-medium">Erased</p>
            <Result r={done} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Result({ r }: { r: EraseResult }) {
  const rows = Object.entries(r.counts).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        Stand-in name: <span className="font-mono text-foreground break-all">{r.stand_in}</span>
      </p>
      {rows.length > 0 && (
        <div className="rounded-md border divide-y">
          {rows.map(([k, n]) => (
            <div key={k} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
              <span className="min-w-0 break-words">{humanise(k)}</span>
              <span className="tabular-nums font-medium">{n}</span>
            </div>
          ))}
        </div>
      )}
      {r.actions.length > 0 && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm text-amber-800 dark:text-amber-200">
          <p className="flex items-center gap-1.5 font-medium">
            <AlertTriangle aria-hidden="true" className="h-4 w-4 shrink-0" />
            Needs your attention
          </p>
          <ul className="mt-1.5 list-disc space-y-1 pl-5">
            {r.actions.map((a, i) => (
              <li key={i}>{a}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function ErrorLine({ text }: { text: string }) {
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]"
    >
      {text}
    </div>
  );
}
