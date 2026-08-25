"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  CircleAlert,
  Copy,
  HelpCircle,
  Loader2,
  Plus,
  RefreshCw,
  Server,
  Signal,
  Trash2,
  Zap,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import { cn, formatRelative } from "@/lib/utils";
import type {
  BuildingRow,
  CollectorSummary,
  CreateCollectorResponse,
} from "@/lib/types";

// One-glance answer to "how are my collectors doing?" Table-first, no
// per-collector detail yet — clicking through takes you to the existing
// devices page filtered to that collector. Sorting is server-side (ops-first:
// offline before degraded before online) so the row that needs attention is
// always in the top pixels of the page.

type Tone = {
  label: string;
  variant: "success" | "warning" | "destructive" | "secondary";
  icon: React.ComponentType<{ className?: string }>;
};

const STATUS_TONE: Record<string, Tone> = {
  online:   { label: "Online",  variant: "success",     icon: Signal },
  degraded: { label: "Warning", variant: "warning",     icon: CircleAlert },
  offline:  { label: "Offline", variant: "destructive", icon: CircleAlert },
  unknown:  { label: "Unknown", variant: "secondary",   icon: HelpCircle },
};

const SYNC_TONE: Record<string, Tone> = {
  current: { label: "Current", variant: "success",   icon: Signal },
  stale:   { label: "Stale",   variant: "warning",   icon: CircleAlert },
  unknown: { label: "—",       variant: "secondary", icon: HelpCircle },
};

export default function CollectorsPage() {
  const session = useSession();
  const canManage = hasPermission(session.user, "collector.crud");

  const fetcher = useCallback(
    (signal: AbortSignal) => api.listCollectors(signal),
    []
  );
  const { data, loading, error, refresh } = usePolling<CollectorSummary[]>(
    fetcher,
    15_000
  );

  const [adding, setAdding] = useState(false);
  const [enrollment, setEnrollment] = useState<
    | {
        name: string;
        bridgeCollectorID: string;
        token: string;
        expiresAt: string;
      }
    | null
  >(null);
  const [deleting, setDeleting] = useState<CollectorSummary | null>(null);

  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
              <Server aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div>
              <h1 className="text-xl font-semibold leading-tight">Collectors</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                One row per on-site bridge. Broken collectors sort to the top.
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {canManage && (
              <Button size="sm" onClick={() => setAdding(true)}>
                <Plus className="h-3.5 w-3.5" />
                New collector
              </Button>
            )}
            <UserMenu />
          </div>
        </div>
      </header>

      <div className="flex-1 px-6 py-6">
        <div className="mx-auto max-w-6xl space-y-4">
          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))] flex items-center justify-between">
              <span>Failed to load collectors: {error.message}</span>
              <button
                onClick={() => refresh()}
                className="text-xs underline underline-offset-2 hover:opacity-80"
              >
                Retry
              </button>
            </div>
          )}

          <Card>
            <CardContent className="p-0">
              <div className="overflow-x-auto">
                <table className="w-full min-w-[880px] text-sm">
                  <thead>
                    <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                      <th scope="col" className="px-4 py-3 font-medium">Name</th>
                      <th scope="col" className="px-4 py-3 font-medium">Building</th>
                      <th scope="col" className="px-4 py-3 font-medium">Version</th>
                      <th scope="col" className="px-4 py-3 font-medium text-right">Devices</th>
                      <th scope="col" className="px-4 py-3 font-medium">Status</th>
                      <th scope="col" className="px-4 py-3 font-medium">Config sync</th>
                      <th scope="col" className="px-4 py-3 font-medium">Last seen</th>
                      {canManage && <th scope="col" className="px-4 py-3 font-medium text-right"> </th>}
                    </tr>
                  </thead>
                  <tbody>
                    {loading && !data && (
                      <>
                        {[0, 1, 2].map((i) => (
                          <tr key={i} className="border-b last:border-0">
                            {Array.from({ length: canManage ? 8 : 7 }).map((_, j) => (
                              <td key={j} className="px-4 py-3.5">
                                <Skeleton className="h-4 w-24" />
                              </td>
                            ))}
                          </tr>
                        ))}
                      </>
                    )}
                    {data && data.length === 0 && (
                      <tr>
                        <td colSpan={8} className="px-4 py-16 text-center">
                          <div className="mx-auto max-w-md space-y-3">
                            <div className="mx-auto h-10 w-10 rounded-md bg-muted flex items-center justify-center">
                              <Server aria-hidden="true" className="h-5 w-5 text-muted-foreground" />
                            </div>
                            <div className="font-medium">No collectors registered yet</div>
                            <div className="text-sm text-muted-foreground">
                              {canManage
                                ? "Click New collector to pre-provision one and get a one-time install token for an engineer to run on site."
                                : "Ask an admin to add a collector — you'll see it here once it's phoned home."}
                            </div>
                            {canManage && (
                              <div>
                                <Button size="sm" onClick={() => setAdding(true)}>
                                  <Plus className="h-3.5 w-3.5" />
                                  New collector
                                </Button>
                              </div>
                            )}
                          </div>
                        </td>
                      </tr>
                    )}
                    {data?.map((c) => {
                      const tone = STATUS_TONE[c.status] ?? STATUS_TONE.unknown;
                      const StatusIcon = tone.icon;
                      const sync = SYNC_TONE[c.config_sync_status] ?? SYNC_TONE.unknown;
                      const SyncIcon = sync.icon;
                      return (
                        <tr
                          key={c.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3.5 align-top">
                            <div className="font-medium">{c.name}</div>
                            {c.bridge_collector_id && (
                              <div className="text-xs text-muted-foreground mt-0.5 font-mono">
                                {c.bridge_collector_id}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {c.building_name || (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            {c.bridge_version ? (
                              <div>
                                <div className="font-mono text-xs">{c.bridge_version}</div>
                                {c.bridge_build_time && (
                                  <div
                                    className="text-[10px] text-muted-foreground mt-0.5"
                                    title={c.bridge_build_time}
                                  >
                                    built {formatRelative(c.bridge_build_time)}
                                  </div>
                                )}
                              </div>
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top text-right tabular-nums">
                            {c.device_count > 0 ? (
                              <Link
                                href={`/devices?collector_id=${c.id}`}
                                className="text-primary hover:underline"
                              >
                                {c.device_count}
                              </Link>
                            ) : (
                              <span className="text-muted-foreground">0</span>
                            )}
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            <Badge
                              variant={tone.variant}
                              className="gap-1 uppercase text-[10px]"
                            >
                              <StatusIcon aria-hidden="true" className="h-3 w-3" />
                              {tone.label}
                            </Badge>
                          </td>
                          <td className="px-4 py-3.5 align-top">
                            <Badge
                              variant={sync.variant}
                              className="gap-1 uppercase text-[10px]"
                              title={
                                c.last_config_pull_at
                                  ? `Last pull ${formatRelative(c.last_config_pull_at)}`
                                  : "Bridge has not pulled config yet"
                              }
                            >
                              <SyncIcon aria-hidden="true" className="h-3 w-3" />
                              {sync.label}
                            </Badge>
                          </td>
                          <td className="px-4 py-3.5 align-top text-xs text-muted-foreground">
                            {c.last_seen_at ? (
                              formatRelative(c.last_seen_at)
                            ) : (
                              <span>never</span>
                            )}
                          </td>
                          {canManage && (
                            <td className="px-4 py-3.5 align-top text-right">
                              <div className="inline-flex items-center gap-1">
                                <ReissueTokenButton
                                  collector={c}
                                  onIssued={(token, expiresAt) =>
                                    setEnrollment({
                                      name: c.name,
                                      bridgeCollectorID: c.bridge_collector_id,
                                      token,
                                      expiresAt,
                                    })
                                  }
                                />
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="Delete collector"
                                  title={
                                    c.device_count > 0
                                      ? `Refuses while ${c.device_count} device(s) attached — delete them first`
                                      : "Delete collector"
                                  }
                                  onClick={() => setDeleting(c)}
                                >
                                  <Trash2 className="h-3.5 w-3.5" />
                                </Button>
                              </div>
                            </td>
                          )}
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </CardContent>
          </Card>

          {loading && data && (
            <div className="flex items-center justify-center gap-2 text-xs text-muted-foreground">
              <Loader2 aria-hidden="true" className="h-3 w-3 animate-spin" />
              Refreshing…
            </div>
          )}
        </div>
      </div>

      <Modal
        open={adding}
        onClose={() => setAdding(false)}
        title="New collector"
        wide={false}
      >
        <NewCollectorForm
          onCancel={() => setAdding(false)}
          onCreated={(res) => {
            setAdding(false);
            refresh();
            setEnrollment({
              name: res.name,
              bridgeCollectorID: res.bridge_collector_id,
              token: res.enrollment_token,
              expiresAt: res.expires_at,
            });
          }}
        />
      </Modal>

      <Modal
        open={enrollment !== null}
        onClose={() => setEnrollment(null)}
        title="Collector enrollment"
        wide={false}
      >
        {enrollment && (
          <EnrollmentInstructions
            name={enrollment.name}
            bridgeCollectorID={enrollment.bridgeCollectorID}
            token={enrollment.token}
            expiresAt={enrollment.expiresAt}
            onClose={() => setEnrollment(null)}
          />
        )}
      </Modal>

      <Modal
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title="Delete collector"
        wide={false}
      >
        {deleting && (
          <DeleteCollectorConfirm
            collector={deleting}
            onCancel={() => setDeleting(null)}
            onDeleted={async () => {
              setDeleting(null);
              await refresh();
            }}
          />
        )}
      </Modal>
    </div>
  );
}

function DeleteCollectorConfirm({
  collector,
  onCancel,
  onDeleted,
}: {
  collector: CollectorSummary;
  onCancel: () => void;
  onDeleted: () => Promise<void>;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setError(null);
    setSubmitting(true);
    try {
      await api.deleteCollector(collector.id);
      await onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p>
        Permanently remove{" "}
        <span className="font-medium">{collector.name}</span>
        {collector.bridge_collector_id && (
          <>
            {" "}
            (<span className="font-mono text-xs">{collector.bridge_collector_id}</span>)
          </>
        )}
        ?
      </p>
      <p className="text-xs text-muted-foreground">
        This unregisters the bridge. Its HMAC secret stops working immediately —
        the bridge will fail every heartbeat and stop appearing in this list.
        {collector.device_count > 0 && (
          <>
            {" "}
            The server refuses if any devices are still attached; delete or
            move the {collector.device_count} device(s) first.
          </>
        )}
      </p>
      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="button" onClick={run} disabled={submitting}>
          {submitting ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Deleting…
            </>
          ) : (
            "Delete"
          )}
        </Button>
      </div>
    </div>
  );
}

function ReissueTokenButton({
  collector,
  onIssued,
}: {
  collector: CollectorSummary;
  onIssued: (token: string, expiresAt: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const label =
    collector.last_seen_at
      ? "Re-issue enrollment token"
      : "Show enrollment token";
  const Icon = collector.last_seen_at ? RefreshCw : Zap;
  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        aria-label={label}
        title={label}
        disabled={busy}
        onClick={async () => {
          setErr(null);
          setBusy(true);
          try {
            const r = await api.reissueCollectorEnrollmentToken(collector.id);
            onIssued(r.enrollment_token, r.expires_at);
          } catch (e) {
            setErr((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
        ) : (
          <Icon className="h-3.5 w-3.5" />
        )}
      </Button>
      {err && (
        <div className="text-[10px] [color:hsl(var(--destructive))] mt-1 max-w-[160px] truncate" title={err}>
          {err}
        </div>
      )}
    </>
  );
}

function NewCollectorForm({
  onCancel,
  onCreated,
}: {
  onCancel: () => void;
  onCreated: (res: CreateCollectorResponse & { name: string }) => void;
}) {
  const [name, setName] = useState("");
  const [buildings, setBuildings] = useState<BuildingRow[] | null>(null);
  const [buildingID, setBuildingID] = useState<string>("");
  const [bridgeCollectorID, setBridgeCollectorID] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    api
      .listBuildings(ctrl.signal)
      .then(setBuildings)
      .catch(() => {}); // buildings are optional — the form still saves without one
    return () => ctrl.abort();
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setError(null);
    setSubmitting(true);
    try {
      const res = await api.createCollector({
        name: name.trim(),
        building_id: buildingID || null,
        bridge_collector_id: bridgeCollectorID.trim() || undefined,
      });
      onCreated({ ...res, name: name.trim() });
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4 text-sm">
      <div className="flex flex-col gap-1.5">
        <label htmlFor="collector-name" className="text-xs font-medium text-muted-foreground">
          Display name
        </label>
        <input
          id="collector-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={submitting}
          required
          autoFocus
          placeholder="e.g. Head office bridge"
          className="h-9 rounded-md border border-input bg-background px-3 text-sm outline-none focus:border-primary"
        />
        <p className="text-[11px] text-muted-foreground">
          Shown in the portal + audit logs. Rename later at any time.
        </p>
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="collector-building" className="text-xs font-medium text-muted-foreground">
          Building (optional)
        </label>
        <select
          id="collector-building"
          value={buildingID}
          onChange={(e) => setBuildingID(e.target.value)}
          disabled={submitting || !buildings}
          className="h-9 rounded-md border border-input bg-background px-3 text-sm outline-none focus:border-primary"
        >
          <option value="">— Unassigned —</option>
          {(buildings ?? []).map((b) => (
            <option key={b.id} value={b.id}>
              {b.name}
            </option>
          ))}
        </select>
        <p className="text-[11px] text-muted-foreground">
          Devices auto-inherit this building unless you place them in specific rooms.
        </p>
      </div>

      <details className="rounded-md border border-input bg-muted/20 px-3 py-2 text-xs">
        <summary className="cursor-pointer select-none text-muted-foreground">
          Advanced: override on-wire id
        </summary>
        <div className="mt-2 flex flex-col gap-1.5">
          <input
            value={bridgeCollectorID}
            onChange={(e) => setBridgeCollectorID(e.target.value)}
            disabled={submitting}
            placeholder="derived from name if empty"
            className="h-9 rounded-md border border-input bg-background px-3 font-mono text-xs outline-none focus:border-primary"
          />
          <p className="text-[11px] text-muted-foreground">
            The stable identifier the bridge sends on every heartbeat.
            Lowercase alphanumerics, dashes, underscores. Leave blank to
            auto-generate — the default is fine unless you have a naming
            convention.
          </p>
        </div>
      </details>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting || !name.trim()}>
          {submitting ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Creating…
            </>
          ) : (
            "Create + mint token"
          )}
        </Button>
      </div>
    </form>
  );
}

function EnrollmentInstructions({
  name,
  bridgeCollectorID,
  token,
  expiresAt,
  onClose,
}: {
  name: string;
  bridgeCollectorID: string;
  token: string;
  expiresAt: string;
  onClose: () => void;
}) {
  const [copiedField, setCopiedField] = useState<"token" | "curl" | null>(null);
  const [remainingSec, setRemainingSec] = useState(() =>
    Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000))
  );

  useEffect(() => {
    const t = setInterval(() => {
      setRemainingSec(
        Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000))
      );
    }, 1000);
    return () => clearInterval(t);
  }, [expiresAt]);

  const hours = Math.floor(remainingSec / 3600);
  const mins = Math.floor((remainingSec % 3600) / 60);
  const expired = remainingSec === 0;

  // One-liners derive their URLs from the browser origin so this works
  // cleanly in both UAT and prod without a hard-coded portal hostname.
  // Two flavours: Linux (curl | bash) and Windows (iwr | iex under an
  // elevated PowerShell). Same token wire, same enrollment endpoint —
  // only the client-side script differs.
  const origin =
    typeof window !== "undefined"
      ? window.location.origin
      : "https://<portal>";
  const installShURL = `${origin}/public/collectors/install.sh`;
  const installPsURL = `${origin}/public/collectors/install.ps1`;
  const oneLinerLinux = `curl -fsSL ${installShURL} | sudo AV_ENROLL_TOKEN=${token} bash`;
  // PowerShell has to set the env var in the same statement scope as
  // iex, so we build a small compound: set token, invoke web request,
  // pipe body to invoke-expression.
  const oneLinerWindows = `$env:AV_ENROLL_TOKEN='${token}'; iwr ${installPsURL} -UseBasicParsing | iex`;
  const [platform, setPlatform] = useState<"linux" | "windows">("linux");
  const oneLiner = platform === "linux" ? oneLinerLinux : oneLinerWindows;

  const copy = async (v: string, which: "token" | "curl") => {
    try {
      await navigator.clipboard.writeText(v);
      setCopiedField(which);
      setTimeout(() => setCopiedField(null), 2000);
    } catch {
      /* clipboard blocked; input is still selectable */
    }
  };

  return (
    <div className="flex flex-col gap-4 text-sm">
      <div className="rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-3">
        <p className="[color:hsl(var(--warning))] font-medium">
          One-shot install token for{" "}
          <span className="font-mono">{bridgeCollectorID}</span>.
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          Anyone who runs this can enrol{" "}
          <span className="font-mono">{name}</span> once. Send it through a
          trusted channel; regenerate if it leaks.
        </p>
      </div>

      <div>
        <div className="mb-1 flex items-center justify-between">
          <label className="text-xs font-medium text-muted-foreground">
            Copy-paste one-liner
          </label>
          <span className="text-[10px] text-muted-foreground">
            {expired
              ? "Expired"
              : hours >= 1
              ? `Expires in ${hours}h ${mins}m`
              : `Expires in ${mins}m ${remainingSec % 60}s`}
          </span>
        </div>
        {/* Platform toggle — the token is portable; only the client-side
            bootstrap script differs. Keeping the two side-by-side means
            a copy-paste never has to be transcribed between OSes. */}
        <div className="mb-2 inline-flex rounded-md border border-input bg-muted/30 p-0.5 text-[11px]">
          {(["linux", "windows"] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPlatform(p)}
              className={cn(
                "px-2.5 py-1 rounded transition-colors",
                platform === p
                  ? "bg-background shadow-sm font-medium"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {p === "linux" ? "Linux (bash)" : "Windows (PowerShell)"}
            </button>
          ))}
        </div>
        <div className="flex gap-2">
          <input
            readOnly
            value={oneLiner}
            onFocus={(e) => e.currentTarget.select()}
            className="min-w-0 flex-1 h-9 rounded-md border border-input bg-muted/30 px-3 text-xs font-mono outline-none"
          />
          <Button
            type="button"
            onClick={() => copy(oneLiner, "curl")}
            disabled={expired}
          >
            <Copy className="h-3.5 w-3.5" />
            {copiedField === "curl" ? "Copied" : "Copy"}
          </Button>
        </div>
        <p className="mt-1.5 text-[11px] text-muted-foreground">
          {platform === "linux" ? (
            <>
              Run on a fresh Ubuntu / Debian box at the site. The script
              installs the bridge as a systemd service, redeems this
              token for the HMAC secret, and starts phoning home.
            </>
          ) : (
            <>
              Run in an <strong>elevated</strong> PowerShell on the target
              Windows host. Drop <span className="font-mono">av-bridge.exe</span>{" "}
              into <span className="font-mono">C:\Program Files\av-bridge\</span>{" "}
              first, or set <span className="font-mono">AV_BRIDGE_BINARY_URL</span>{" "}
              to a download URL. The script registers the Windows Service
              and starts it.
            </>
          )}
        </p>
      </div>

      <details className="rounded-md border border-input bg-muted/20 px-3 py-2 text-xs">
        <summary className="cursor-pointer select-none text-muted-foreground">
          Just the token
        </summary>
        <div className="mt-2 flex gap-2">
          <input
            readOnly
            value={token}
            onFocus={(e) => e.currentTarget.select()}
            className="min-w-0 flex-1 h-9 rounded-md border border-input bg-background px-3 font-mono text-xs outline-none"
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => copy(token, "token")}
            disabled={expired}
          >
            {copiedField === "token" ? "Copied" : "Copy"}
          </Button>
        </div>
      </details>

      <div className="flex items-center justify-end pt-2">
        <Button variant="ghost" size="sm" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}
