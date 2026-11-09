"use client";

import { useState } from "react";
import { Copy, Check, Plus, Trash2, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { api } from "@/lib/api";
import type { RoadmapLink } from "@/lib/types";
import { formatRelative, formatTimestamp } from "@/lib/utils";
import { ConfirmModal, ErrorBox, Field, LIMITS, inputCls, useSave } from "./shared";

const EXPIRY_DAYS = [30, 90, 180, 365];

function linkStatus(l: RoadmapLink): "Revoked" | "Expired" | "Active" {
  if (l.revoked_at) return "Revoked";
  if (new Date(l.expires_at).getTime() <= Date.now()) return "Expired";
  return "Active";
}

export function LinksSection({
  links,
  loadError,
  onChange,
}: {
  links: RoadmapLink[] | null;
  loadError: string | null;
  onChange: (l: RoadmapLink[]) => void;
}) {
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<RoadmapLink | null>(null);
  const [deleting, setDeleting] = useState<RoadmapLink | null>(null);

  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold">Share links</h2>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" />
            New link
          </Button>
        </div>
        <p className="mb-3 text-xs text-muted-foreground">
          A link lets someone outside the portal view the roadmap, read-only, until it expires or you
          revoke it.
        </p>
        <ErrorBox message={loadError} />
        {links === null && !loadError ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : links && links.length === 0 ? (
          <p className="text-sm text-muted-foreground">No share links yet.</p>
        ) : links ? (
          <div className="overflow-x-auto rounded-md border">
            <table className="w-full min-w-[720px] text-sm">
              <thead className="bg-muted/40 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Name</th>
                  <th className="px-3 py-2 font-medium">Created</th>
                  <th className="px-3 py-2 font-medium">Expires</th>
                  <th className="px-3 py-2 font-medium">Last opened</th>
                  <th className="px-3 py-2 font-medium">Opens</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {links.map((l) => {
                  const st = linkStatus(l);
                  return (
                    <tr key={l.id} className="border-t">
                      <td className="px-3 py-2 font-medium">{l.name}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{formatTimestamp(l.created_at)}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{formatTimestamp(l.expires_at)}</td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        {l.last_used_at ? formatRelative(l.last_used_at) : "Never"}
                      </td>
                      <td className="px-3 py-2">{l.use_count}</td>
                      <td className="px-3 py-2">{st}</td>
                      <td className="px-3 py-2">
                        <div className="flex items-center justify-end gap-1 whitespace-nowrap">
                          {st === "Active" && (
                            <Button size="sm" variant="ghost" onClick={() => setRevoking(l)}>
                              Revoke
                            </Button>
                          )}
                          <Button size="icon" variant="ghost" aria-label={`Delete link ${l.name}`}
                            onClick={() => setDeleting(l)}>
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}
      </CardContent>

      <NewLinkModal
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(list) => onChange(list)}
      />

      <ConfirmModal
        open={revoking !== null}
        title="Revoke share link"
        body={
          <p>
            Revoke the link for <span className="font-medium">{revoking?.name}</span>? Anyone using it
            loses access straight away. You can&apos;t undo this.
          </p>
        }
        confirmLabel="Revoke"
        onCancel={() => setRevoking(null)}
        onConfirm={async () => {
          if (!revoking) return;
          onChange((await api.revokeRoadmapLink(revoking.id)).links);
          setRevoking(null);
        }}
      />

      <ConfirmModal
        open={deleting !== null}
        title="Delete share link"
        body={
          <p>
            Delete the link for <span className="font-medium">{deleting?.name}</span> from this list?
            If it is still active it stops working.
          </p>
        }
        confirmLabel="Delete"
        onCancel={() => setDeleting(null)}
        onConfirm={async () => {
          if (!deleting) return;
          onChange((await api.deleteRoadmapLink(deleting.id)).links);
          setDeleting(null);
        }}
      />
    </Card>
  );
}

// NewLinkModal collects a name and expiry, then shows the link once. The
// server keeps only a hash of the token, so the URL can't be shown again.
function NewLinkModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (links: RoadmapLink[]) => void;
}) {
  // Once the URL is on screen, an accidental close (Esc, click outside) asks first.
  const [shown, setShown] = useState(false);
  const close = () => {
    setShown(false);
    onClose();
  };
  return (
    <Modal
      open={open}
      onClose={close}
      title="New share link"
      wide={false}
      dirty={shown}
      dirtyPrompt="You won't see this link again. Close without copying it?"
    >
      {open && <NewLinkBody onClose={close} onCreated={onCreated} onShown={() => setShown(true)} />}
    </Modal>
  );
}

function NewLinkBody({
  onClose,
  onCreated,
  onShown,
}: {
  onClose: () => void;
  onCreated: (links: RoadmapLink[]) => void;
  onShown: () => void;
}) {
  const [name, setName] = useState("");
  const [days, setDays] = useState(90);
  const [url, setUrl] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);
  const { busy, error, run } = useSave();

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    const made = await run(async () => {
      const res = await api.createRoadmapLink({ name: name.trim(), expires_in_days: days });
      // The list is refreshed separately so a failure there can't hide the URL.
      return res;
    });
    if (!made) return;
    setUrl(made.url);
    onShown();
    try {
      onCreated((await api.listRoadmapLinks()).links);
    } catch {
      // The list catches up on the next load; the link itself is created.
    }
  };

  const copy = async () => {
    setCopyError(null);
    try {
      await navigator.clipboard.writeText(url as string);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopyError("Couldn't copy. Select the link and copy it by hand.");
    }
  };

  if (url) {
    return (
      <div className="flex flex-col gap-4">
        <div className="flex items-start gap-3 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-3 text-sm">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 [color:hsl(var(--warning))]" />
          <p className="font-medium [color:hsl(var(--warning))]">You won&apos;t see this link again.</p>
        </div>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="rml-url" className="text-xs font-medium text-muted-foreground">
            Share link
          </label>
          <div className="flex gap-2">
            <input id="rml-url" readOnly className={`${inputCls} font-mono text-xs`} value={url}
              onFocus={(e) => e.currentTarget.select()} />
            <Button type="button" variant="outline" onClick={copy}>
              {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          {copyError && <p className="text-xs [color:hsl(var(--destructive))]">{copyError}</p>}
        </div>
        <div className="flex justify-end pt-2">
          <Button type="button" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }

  return (
    <form onSubmit={create} className="flex flex-col gap-4">
      <Field label="Company or team" htmlFor="rml-name" hint="Who you're sharing with, for example Acme Ltd">
        <input id="rml-name" className={inputCls} maxLength={LIMITS.linkName} required value={name}
          disabled={busy} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Expires after" htmlFor="rml-days">
        <select id="rml-days" className={inputCls} value={days} disabled={busy}
          onChange={(e) => setDays(Number(e.target.value))}>
          {EXPIRY_DAYS.map((d) => (
            <option key={d} value={d}>{d} days</option>
          ))}
        </select>
      </Field>
      <ErrorBox message={error} />
      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy || name.trim() === ""}>
          {busy ? "Creating…" : "Create link"}
        </Button>
      </div>
    </form>
  );
}
