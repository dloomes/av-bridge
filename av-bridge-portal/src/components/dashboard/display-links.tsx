"use client";

// Display links: read-only addresses that show shared dashboards on a
// wall screen with no one signed in, rotating between them. Managed by
// people with dashboard.share; the address is shown once.

import { useCallback, useEffect, useState } from "react";
import { Check, Copy, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, type DashboardSummary, type DisplayLink } from "@/lib/api";
import { formatRelative } from "@/lib/utils";

const labelClass = "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const ROTATE = [
  { value: 30, label: "30 seconds" },
  { value: 60, label: "1 minute" },
  { value: 120, label: "2 minutes" },
  { value: 300, label: "5 minutes" },
];

function errorText(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e);
  const i = msg.indexOf("{");
  if (i >= 0) {
    try {
      const body = JSON.parse(msg.slice(i));
      if (typeof body.error === "string") return body.error;
    } catch {}
  }
  return msg;
}

export function displayAddress(token: string) {
  return `${window.location.origin}/display#token=${encodeURIComponent(token)}`;
}

export function DisplayLinks({ shared }: { shared: DashboardSummary[] }) {
  const [links, setLinks] = useState<DisplayLink[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const [rotate, setRotate] = useState(60);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<{ name: string; url: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(() => {
    api.listDisplayLinks().then((r) => setLinks(r.links)).catch((e) => setError(errorText(e)));
  }, []);
  useEffect(load, [load]);

  const nameOf = (id: string) => shared.find((d) => d.id === id)?.name ?? "(no longer shared)";

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.createDisplayLink({ name: name.trim(), dashboard_ids: chosen, rotate_seconds: rotate });
      setCreated({ name: r.link.name, url: displayAddress(r.token) });
      setCopied(false);
      setName("");
      setChosen([]);
      load();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (id: string) => {
    try {
      await api.revokeDisplayLink(id);
      load();
    } catch (e) {
      setError(errorText(e));
    }
  };

  return (
    <div className="space-y-5 text-sm">
      <p className="text-muted-foreground">
        A display link opens shared dashboards full screen on a wall screen or TV, with no one signed in. It can
        only view, sees no more than you can, and rotates between its dashboards. Revoke it to stop the screen at
        once.
      </p>

      {created && (
        <div className="space-y-2 rounded-md border border-emerald-500/40 bg-emerald-500/5 p-3">
          <div className="font-medium">Display link &ldquo;{created.name}&rdquo; created</div>
          <p className="text-xs text-muted-foreground">
            Open this address in the screen&apos;s browser. It&apos;s shown only now, so copy it before closing this
            window. Anyone with it can view these dashboards, so treat it like a password.
          </p>
          <div className="flex gap-2">
            <input className={inputClass} readOnly value={created.url} onFocus={(e) => e.target.select()} />
            <Button
              size="sm"
              variant="outline"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(created.url);
                  setCopied(true);
                } catch {}
              }}
            >
              {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
        </div>
      )}

      {error && (
        <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="space-y-3 rounded-md border p-3">
        <div className="font-medium">New display link</div>
        {shared.length === 0 ? (
          <p className="text-xs text-muted-foreground">Share a dashboard first; display links can only show shared dashboards.</p>
        ) : (
          <>
            <div className="space-y-1.5">
              <label className={labelClass} htmlFor="dl-name">Name</label>
              <input id="dl-name" className={inputClass} value={name} onChange={(e) => setName(e.target.value)} maxLength={80}
                placeholder="Service desk wall" />
            </div>
            <fieldset className="space-y-1.5">
              <legend className={labelClass}>Dashboards, in the order shown</legend>
              {shared.map((d) => (
                <label key={d.id} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={chosen.includes(d.id)}
                    onChange={(e) =>
                      setChosen((c) => (e.target.checked ? [...c, d.id] : c.filter((x) => x !== d.id)))
                    }
                  />
                  {d.name}
                  {chosen.includes(d.id) && chosen.length > 1 && (
                    <span className="text-xs text-muted-foreground">#{chosen.indexOf(d.id) + 1}</span>
                  )}
                </label>
              ))}
            </fieldset>
            {chosen.length > 1 && (
              <div className="space-y-1.5">
                <label className={labelClass} htmlFor="dl-rotate">Show each for</label>
                <select id="dl-rotate" className={inputClass} value={rotate} onChange={(e) => setRotate(Number(e.target.value))}>
                  {ROTATE.map((r) => (
                    <option key={r.value} value={r.value}>{r.label}</option>
                  ))}
                </select>
              </div>
            )}
            <div className="flex justify-end">
              <Button size="sm" onClick={create} disabled={busy || !name.trim() || chosen.length === 0}>
                {busy ? "Creating…" : "Create display link"}
              </Button>
            </div>
          </>
        )}
      </div>

      <div className="space-y-2">
        <div className={labelClass}>Display links</div>
        {links === null ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : links.length === 0 ? (
          <p className="text-xs text-muted-foreground">None yet.</p>
        ) : (
          <ul className="divide-y rounded-md border">
            {links.map((l) => (
              <li key={l.id} className="flex items-center justify-between gap-3 px-3 py-2">
                <div className="min-w-0">
                  <div className="truncate font-medium">{l.name}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {l.dashboard_ids.map(nameOf).join(", ")}
                    {l.dashboard_ids.length > 1 && ` · every ${l.rotate_seconds}s`}
                    {" · "}
                    {l.last_used_at ? `last seen ${formatRelative(l.last_used_at)}` : "not opened yet"}
                  </div>
                </div>
                <Button variant="outline" size="sm" onClick={() => revoke(l.id)}>
                  <Trash2 className="h-3.5 w-3.5" /> Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
