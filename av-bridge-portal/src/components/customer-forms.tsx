"use client";

import { useState } from "react";
import { Copy, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, type HelpdeskOverviewItem } from "@/lib/api";
import { validateSlug } from "@/lib/branding-slug";
import type { CreateCustomerBody, UpdateCustomerBody } from "@/lib/types";

// customer-forms.tsx — shared vendor-only forms for creating and editing a
// customer. Extracted from the helpdesk page so both /helpdesk and
// /customers can mount the same modals; changing the customer schema once
// keeps every entry point aligned.
//
// Slug validation lives in @/lib/branding-slug so the sign-in host resolver
// and both forms enforce identical rules.

// NewCustomerForm creates a customer + optional first admin in a single
// backend call. On success, if an admin was seeded, the credentials are
// surfaced inline so the vendor can copy/paste them to the new admin —
// this is the only time the password is shown (the hash is one-way).
export function NewCustomerForm({
  onCancel,
  onCreated,
}: {
  onCancel: () => void;
  onCreated: () => Promise<void> | void;
}) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [entra, setEntra] = useState("");
  const [seedAdmin, setSeedAdmin] = useState(true);
  const [adminEmail, setAdminEmail] = useState("");
  const [adminName, setAdminName] = useState("");
  const [adminPassword, setAdminPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ email: string; password: string } | null>(null);

  const slugError = validateSlug(slug);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (slugError) {
      setError(slugError);
      return;
    }
    setBusy(true);
    try {
      const body: CreateCustomerBody = {
        name: name.trim(),
        entra_tenant_id: entra.trim() || undefined,
        slug: slug.trim() || undefined,
      };
      if (seedAdmin) {
        body.initial_admin = {
          email: adminEmail.trim(),
          password: adminPassword,
          full_name: adminName.trim() || undefined,
        };
      }
      await api.createCustomer(body);
      if (seedAdmin) {
        setResult({ email: adminEmail.trim(), password: adminPassword });
      } else {
        await onCreated();
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (result) {
    return (
      <div className="space-y-3">
        <div className="rounded-md border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-sm">
          Customer created. Share the credentials below with the new admin — this is the only time the password is shown.
        </div>
        <CredentialRow label="Email" value={result.email} />
        <CredentialRow label="Password" value={result.password} secret />
        <div className="flex items-center justify-end gap-2 pt-2 border-t">
          <Button onClick={() => void onCreated()}>Done</Button>
        </div>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="space-y-1">
        <label htmlFor="nc-name" className="text-xs font-medium text-muted-foreground">
          Customer name
        </label>
        <input
          id="nc-name"
          type="text"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
        />
      </div>

      <div className="space-y-1">
        <label htmlFor="nc-slug" className="text-xs font-medium text-muted-foreground">
          URL slug <span className="text-muted-foreground/70">(optional)</span>
        </label>
        <div className="flex items-center gap-1">
          <input
            id="nc-slug"
            type="text"
            placeholder="acme"
            value={slug}
            onChange={(e) => setSlug(e.target.value.toLowerCase())}
            disabled={busy}
            className={`h-9 flex-1 rounded-md border bg-background px-3 text-sm font-mono ${
              slugError ? "border-destructive" : "border-input"
            }`}
          />
          <span className="text-xs text-muted-foreground shrink-0">
            .uat.involvecloud.com
          </span>
        </div>
        {slugError ? (
          <p className="text-[11px] [color:hsl(var(--destructive))]">{slugError}</p>
        ) : (
          <p className="text-[11px] text-muted-foreground">
            Sets the customer&rsquo;s branded sign-in URL. 3-50 chars, lowercase letters/digits/hyphens. Leave blank to skip.
          </p>
        )}
      </div>

      <div className="space-y-1">
        <label htmlFor="nc-entra" className="text-xs font-medium text-muted-foreground">
          Entra tenant ID <span className="text-muted-foreground/70">(optional)</span>
        </label>
        <input
          id="nc-entra"
          type="text"
          placeholder="tenant-<name>-<random>"
          value={entra}
          onChange={(e) => setEntra(e.target.value)}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
        />
        <p className="text-[11px] text-muted-foreground">
          Set when the customer federates with Entra later. Leave blank for local-auth-only tenants.
        </p>
      </div>

      <div className="rounded-md border p-3 space-y-3">
        <label className="flex items-start gap-2 text-sm cursor-pointer">
          <input
            type="checkbox"
            checked={seedAdmin}
            onChange={(e) => setSeedAdmin(e.target.checked)}
            disabled={busy}
            className="mt-0.5"
          />
          <span>
            Seed an initial admin user{" "}
            <span className="text-xs text-muted-foreground">
              (recommended — without this the tenant has nobody who can log in)
            </span>
          </span>
        </label>

        {seedAdmin && (
          <div className="space-y-2">
            <div className="space-y-1">
              <label htmlFor="nc-ae" className="text-xs font-medium text-muted-foreground">
                Admin email
              </label>
              <input
                id="nc-ae"
                type="email"
                required
                value={adminEmail}
                onChange={(e) => setAdminEmail(e.target.value)}
                disabled={busy}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="nc-an" className="text-xs font-medium text-muted-foreground">
                Admin full name
              </label>
              <input
                id="nc-an"
                type="text"
                value={adminName}
                onChange={(e) => setAdminName(e.target.value)}
                disabled={busy}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="nc-ap" className="text-xs font-medium text-muted-foreground">
                Initial password
              </label>
              <input
                id="nc-ap"
                type="password"
                required
                minLength={12}
                value={adminPassword}
                onChange={(e) => setAdminPassword(e.target.value)}
                disabled={busy}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              />
              <p className="text-[11px] text-muted-foreground">
                Minimum 12 characters. Share it out of band — the admin can change it after first sign-in.
              </p>
            </div>
          </div>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Create customer
        </Button>
      </div>
    </form>
  );
}

// EditCustomerForm edits mutable fields on an existing customer row: name
// + URL slug. Sends PATCH with only the fields that actually changed so
// the audit log stays clean. Clearing the slug field and saving sends ""
// which the server interprets as "set to NULL".
export function EditCustomerForm({
  customer,
  onCancel,
  onSaved,
}: {
  customer: HelpdeskOverviewItem;
  onCancel: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const [name, setName] = useState(customer.name);
  const [slug, setSlug] = useState(customer.slug ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const slugError = validateSlug(slug);
  const nameChanged = name.trim() !== customer.name;
  const slugChanged = slug.trim().toLowerCase() !== (customer.slug ?? "");
  const canSave = !busy && !slugError && name.trim() !== "" && (nameChanged || slugChanged);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!canSave) return;
    setBusy(true);
    try {
      const body: UpdateCustomerBody = {};
      if (nameChanged) body.name = name.trim();
      if (slugChanged) body.slug = slug.trim();
      await api.updateCustomer(customer.id, body);
      await onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="space-y-1">
        <label htmlFor="ec-name" className="text-xs font-medium text-muted-foreground">
          Customer name
        </label>
        <input
          id="ec-name"
          type="text"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
        />
      </div>

      <div className="space-y-1">
        <label htmlFor="ec-slug" className="text-xs font-medium text-muted-foreground">
          URL slug{" "}
          <span className="text-muted-foreground/70">
            (empty = no branded URL)
          </span>
        </label>
        <div className="flex items-center gap-1">
          <input
            id="ec-slug"
            type="text"
            placeholder="acme"
            value={slug}
            onChange={(e) => setSlug(e.target.value.toLowerCase())}
            disabled={busy}
            className={`h-9 flex-1 rounded-md border bg-background px-3 text-sm font-mono ${
              slugError ? "border-destructive" : "border-input"
            }`}
          />
          <span className="text-xs text-muted-foreground shrink-0">
            .uat.involvecloud.com
          </span>
        </div>
        {slugError ? (
          <p className="text-[11px] [color:hsl(var(--destructive))]">
            {slugError}
          </p>
        ) : slug && (
          <p className="text-[11px] text-muted-foreground">
            Sign-in page will render this customer&rsquo;s branding when reached via{" "}
            <span className="font-mono">
              {slug}.uat.involvecloud.com
            </span>
            .
          </p>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={!canSave}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Save changes
        </Button>
      </div>
    </form>
  );
}

// CredentialRow displays a label + value with an optional show/hide toggle
// (for secrets) and a copy-to-clipboard button. Used for the initial admin
// email + password after a fresh customer is provisioned.
export function CredentialRow({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  const [reveal, setReveal] = useState(!secret);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard may be unavailable over insecure origins — noop
    }
  };
  return (
    <div className="flex items-center gap-2 rounded-md border bg-muted/40 px-3 py-2 text-sm">
      <span className="text-xs uppercase tracking-wide text-muted-foreground w-20 shrink-0">{label}</span>
      <span className="font-mono flex-1 truncate">
        {reveal ? value : "•".repeat(value.length)}
      </span>
      {secret && (
        <Button variant="ghost" size="sm" onClick={() => setReveal((v) => !v)}>
          {reveal ? "Hide" : "Show"}
        </Button>
      )}
      <Button variant="ghost" size="icon" aria-label="Copy" onClick={copy}>
        <Copy className="h-3.5 w-3.5" />
      </Button>
      {copied && <span className="text-[11px] text-emerald-600">copied</span>}
    </div>
  );
}
