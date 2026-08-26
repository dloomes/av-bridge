"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  BookOpen,
  Check,
  Copy,
  Key,
  Loader2,
  Plus,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type {
  APITokenRow,
  CreateAPITokenBody,
  CreateAPITokenResponse,
} from "@/lib/types";

// Scope catalogue for the create form. Must stay in sync with the
// backend allowlist in portalapi/api_tokens.go — v1 accepts view.* keys
// only. Labels use human wording rather than the raw permission key so
// an operator picking scopes doesn't have to memorize the backend
// vocabulary.
const SCOPE_OPTIONS: { key: string; label: string; description: string }[] = [
  { key: "view.dashboard", label: "Read fleet + devices", description: "Devices, telemetry, rooms, buildings, alerts, events." },
  { key: "view.assets", label: "Read assets (CMDB)", description: "Physical inventory records for the tenant." },
  { key: "view.firmware", label: "Read firmware", description: "Firmware summary and per-device version reports." },
  { key: "view.notifications", label: "Read notification channels", description: "Configured email / Teams / webhook channels (never their secrets)." },
  { key: "view.audit", label: "Read audit log", description: "Activity trail for the tenant." },
  { key: "view.reports", label: "Read reports", description: "Uptime, room-activity, and related derived reports." },
  { key: "nightly.view", label: "Read Room Readiness", description: "Nightly schedules, routines, and run history." },
];

const EXPIRY_PRESETS = [
  { key: "30", label: "30 days" },
  { key: "90", label: "90 days" },
  { key: "365", label: "1 year" },
  { key: "", label: "Never (not recommended)" },
] as const;

export default function APITokensPage() {
  const session = useSession();
  const canManage = hasPermission(session.user, "api_token.manage");
  const [tokens, setTokens] = useState<APITokenRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [revealed, setRevealed] = useState<CreateAPITokenResponse | null>(null);
  const [revoking, setRevoking] = useState<APITokenRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listAPITokens(signal);
      if (signal?.aborted) return;
      setTokens(list);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const active = useMemo(
    () => (tokens ?? []).filter((t) => !t.revoked_at),
    [tokens]
  );
  const revoked = useMemo(
    () => (tokens ?? []).filter((t) => !!t.revoked_at),
    [tokens]
  );

  return (
    <div className="min-h-screen">
      <div className="max-w-5xl mx-auto p-6 space-y-4">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">API tokens</h1>
            <p className="text-sm text-muted-foreground">
              Programmatic access to <code className="text-xs bg-muted px-1 rounded">/pub/v1</code>. Read-only in this release.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              asChild
            >
              <a href="/pub/v1/docs" target="_blank" rel="noreferrer">
                <BookOpen className="h-4 w-4" />
                API docs
              </a>
            </Button>
            {canManage && (
              <Button onClick={() => setCreating(true)}>
                <Plus className="h-4 w-4" />
                New token
              </Button>
            )}
            <UserMenu />
          </div>
        </header>

        {loadError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {loadError}
          </div>
        )}

        {tokens === null ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 w-full" />
            ))}
          </div>
        ) : active.length === 0 && revoked.length === 0 ? (
          <EmptyState canCreate={canManage} onCreate={() => setCreating(true)} />
        ) : (
          <>
            <TokenTable
              title="Active"
              tokens={active}
              canManage={canManage}
              onRevoke={setRevoking}
            />
            {revoked.length > 0 && (
              <TokenTable
                title="Revoked"
                tokens={revoked}
                canManage={false}
                onRevoke={() => {}}
                mutedHeader
              />
            )}
          </>
        )}

        <Modal
          open={creating}
          onClose={() => setCreating(false)}
          title="New API token"
        >
          {creating && (
            <CreateTokenForm
              onCancel={() => setCreating(false)}
              onCreated={async (t) => {
                setCreating(false);
                setRevealed(t);
                await load();
              }}
            />
          )}
        </Modal>

        <Modal
          open={revealed !== null}
          onClose={() => setRevealed(null)}
          title="Save this token now"
          wide={false}
        >
          {revealed && (
            <RevealedToken token={revealed} onClose={() => setRevealed(null)} />
          )}
        </Modal>

        <Modal
          open={revoking !== null}
          onClose={() => setRevoking(null)}
          title={`Revoke ${revoking?.name ?? ""}`}
          wide={false}
        >
          {revoking && (
            <RevokeConfirm
              token={revoking}
              onCancel={() => setRevoking(null)}
              onDone={async () => {
                setRevoking(null);
                await load();
              }}
            />
          )}
        </Modal>
      </div>
    </div>
  );
}

function EmptyState({
  canCreate,
  onCreate,
}: {
  canCreate: boolean;
  onCreate: () => void;
}) {
  return (
    <Card>
      <CardContent className="py-12 flex flex-col items-center gap-3 text-center">
        <div className="h-10 w-10 rounded-md bg-primary/10 ring-1 ring-primary/30 flex items-center justify-center">
          <Key className="h-5 w-5 text-primary" />
        </div>
        <div className="max-w-md space-y-1">
          <div className="text-sm font-medium">No API tokens yet</div>
          <div className="text-xs text-muted-foreground">
            Mint a token to let an external system read from this tenant.
            Tokens carry only the scopes you pick, and the secret is
            shown once at creation.
          </div>
        </div>
        {canCreate && (
          <Button onClick={onCreate} className="mt-2">
            <Plus className="h-4 w-4" />
            New token
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

function TokenTable({
  title,
  tokens,
  canManage,
  onRevoke,
  mutedHeader,
}: {
  title: string;
  tokens: APITokenRow[];
  canManage: boolean;
  onRevoke: (t: APITokenRow) => void;
  mutedHeader?: boolean;
}) {
  return (
    <Card>
      <CardContent className="p-0">
        <div
          className={`px-3 py-2 text-xs uppercase tracking-wide border-b ${
            mutedHeader ? "text-muted-foreground" : "font-medium"
          }`}
        >
          {title}
        </div>
        <div className="divide-y">
          {tokens.map((t) => (
            <TokenRow
              key={t.id}
              token={t}
              canManage={canManage}
              onRevoke={() => onRevoke(t)}
            />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function TokenRow({
  token,
  canManage,
  onRevoke,
}: {
  token: APITokenRow;
  canManage: boolean;
  onRevoke: () => void;
}) {
  const expired =
    !!token.expires_at && new Date(token.expires_at).getTime() < Date.now();
  return (
    <div className="flex items-center gap-3 p-3">
      <div className="h-9 w-9 rounded-md flex items-center justify-center shrink-0 bg-primary/10 ring-1 ring-primary/30">
        <Key className="h-4 w-4 text-primary" />
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium flex items-center gap-2">
          <span className="truncate">{token.name}</span>
          {token.revoked_at && (
            <span className="text-[10px] uppercase tracking-wide bg-muted text-muted-foreground px-1.5 py-0.5 rounded">
              revoked
            </span>
          )}
          {!token.revoked_at && expired && (
            <span className="text-[10px] uppercase tracking-wide bg-amber-500/10 text-amber-700 dark:text-amber-400 px-1.5 py-0.5 rounded">
              expired
            </span>
          )}
        </div>
        <div className="text-xs text-muted-foreground truncate">
          <code className="bg-muted px-1 rounded">avb_{token.token_prefix}…</code>
          {" · "}
          {token.scopes.length} scope{token.scopes.length === 1 ? "" : "s"}
          {token.created_by ? ` · created by ${token.created_by}` : ""}
        </div>
      </div>
      <div className="hidden md:block w-40 text-right text-xs text-muted-foreground">
        {token.last_used_at ? (
          <>
            Last used {formatRelative(token.last_used_at)}
            {token.last_used_ip ? (
              <div className="text-[10px] opacity-70">{token.last_used_ip}</div>
            ) : null}
          </>
        ) : (
          <span className="opacity-60">Never used</span>
        )}
      </div>
      <div className="hidden lg:block w-32 text-right text-xs text-muted-foreground">
        {token.revoked_at
          ? `revoked ${formatRelative(token.revoked_at)}`
          : token.expires_at
          ? `expires ${formatRelative(token.expires_at)}`
          : "no expiry"}
      </div>
      <div className="flex items-center gap-1">
        {canManage && !token.revoked_at && (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Revoke token"
            onClick={onRevoke}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
    </div>
  );
}

function CreateTokenForm({
  onCancel,
  onCreated,
}: {
  onCancel: () => void;
  onCreated: (t: CreateAPITokenResponse) => void;
}) {
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Record<string, boolean>>({
    "view.dashboard": true,
  });
  const [expiry, setExpiry] = useState<string>("90");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const selectedScopes = Object.entries(scopes)
    .filter(([, v]) => v)
    .map(([k]) => k);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (name.trim() === "") {
      setError("Give this token a descriptive name");
      return;
    }
    if (selectedScopes.length === 0) {
      setError("Select at least one scope");
      return;
    }
    setSaving(true);
    try {
      const body: CreateAPITokenBody = {
        name: name.trim(),
        scopes: selectedScopes,
      };
      if (expiry !== "") {
        const days = parseInt(expiry, 10);
        const iso = new Date(Date.now() + days * 24 * 3600 * 1000).toISOString();
        body.expires_at = iso;
      }
      const created = await api.createAPIToken(body);
      onCreated(created);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <label className="text-xs font-medium text-muted-foreground">Name</label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. Facilities BI export"
          className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
          maxLength={200}
          autoFocus
        />
        <div className="text-xs text-muted-foreground mt-1">
          Choose a name that makes it obvious later which system uses this key.
        </div>
      </div>

      <div>
        <div className="text-xs font-medium text-muted-foreground mb-2">Scopes</div>
        <div className="space-y-2">
          {SCOPE_OPTIONS.map((opt) => (
            <label
              key={opt.key}
              className="flex items-start gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/30"
            >
              <input
                type="checkbox"
                className="mt-0.5"
                checked={!!scopes[opt.key]}
                onChange={(e) =>
                  setScopes((s) => ({ ...s, [opt.key]: e.target.checked }))
                }
              />
              <div className="min-w-0">
                <div className="text-sm font-medium">{opt.label}</div>
                <div className="text-xs text-muted-foreground">
                  {opt.description}
                </div>
              </div>
            </label>
          ))}
        </div>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground">Expires</label>
        <select
          value={expiry}
          onChange={(e) => setExpiry(e.target.value)}
          className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
        >
          {EXPIRY_PRESETS.map((p) => (
            <option key={p.key} value={p.key}>
              {p.label}
            </option>
          ))}
        </select>
      </div>

      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs [color:hsl(var(--destructive))] flex items-start gap-2">
          <AlertTriangle className="h-3.5 w-3.5 mt-0.5" />
          <span>{error}</span>
        </div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
        <Button type="submit" disabled={saving}>
          {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Create token
        </Button>
      </div>
    </form>
  );
}

function RevealedToken({
  token,
  onClose,
}: {
  token: CreateAPITokenResponse;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token.token);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard unavailable — the user can still select the text.
    }
  };
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs">
        <div className="flex items-start gap-2">
          <AlertTriangle className="h-3.5 w-3.5 mt-0.5 text-amber-700 dark:text-amber-400 shrink-0" />
          <div>
            This is the only time this token will be shown. Copy it into
            your integration&apos;s secret store now — we can&apos;t retrieve
            it again.
          </div>
        </div>
      </div>
      <div>
        <div className="text-xs font-medium text-muted-foreground mb-1">
          Token
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 min-w-0 truncate rounded-md border bg-muted/40 px-3 py-2 text-xs font-mono">
            {token.token}
          </code>
          <Button type="button" variant="ghost" size="icon" onClick={copy} aria-label="Copy token">
            {copied ? (
              <Check className="h-4 w-4 text-emerald-600" />
            ) : (
              <Copy className="h-4 w-4" />
            )}
          </Button>
        </div>
      </div>
      <div className="text-xs text-muted-foreground">
        Send it as an <code className="bg-muted px-1 rounded">Authorization: Bearer …</code> header when calling <code className="bg-muted px-1 rounded">/pub/v1</code>.
      </div>
      <div className="flex justify-end pt-2">
        <Button onClick={onClose}>I&apos;ve saved it</Button>
      </div>
    </div>
  );
}

function RevokeConfirm({
  token,
  onCancel,
  onDone,
}: {
  token: APITokenRow;
  onCancel: () => void;
  onDone: () => Promise<void> | void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.revokeAPIToken(token.id);
      await onDone();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <div className="text-sm">
        Revoking <span className="font-medium">{token.name}</span> stops the
        integration using this key immediately. Existing calls in flight
        continue; new calls with this token return 401.
      </div>
      <div className="text-xs text-muted-foreground">
        The token stays in the audit history so you can trace past activity.
      </div>
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Keep active
        </Button>
        <Button type="button" variant="destructive" onClick={confirm} disabled={busy}>
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
          Revoke token
        </Button>
      </div>
    </div>
  );
}
