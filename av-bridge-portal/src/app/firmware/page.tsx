"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  AlertTriangle,
  CheckCircle2,
  Cpu,
  ExternalLink,
  Loader2,
  Pencil,
  RefreshCcw,
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
import type { FirmwareRow, FirmwareTargetBody } from "@/lib/types";

// FirmwarePage — read-only summary that stops lying about "outdated".
//
// Instead of comparing every device to whatever's newest in the fleet (a
// broken heuristic — a beta on one device would mark everything else as
// "outdated"), we show the actual fleet breakdown per (make, model):
//   3 devices on 3.14.1 · 1 on 4.0.0 · 1 unknown
//
// Admins can optionally set a target_version + docs_url per model via the
// edit modal. When a target is set, devices trailing it get badged
// explicitly — customer opts in to strictness. When no target, the UI just
// surfaces the versions present and lets the operator decide.

interface ModelGroup {
  make: string;
  model: string;
  devices: FirmwareRow[];
  versions: Map<string, number>; // version → count
  unknownFirmware: number;
  targetVersion: string;
  docsURL: string;
}

export default function FirmwarePage() {
  const session = useSession();
  const admin = hasPermission(session.user, "firmware_target.crud");
  const [rows, setRows] = useState<FirmwareRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ make: string; model: string } | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.firmware(signal);
      if (signal?.aborted) return;
      setRows(data);
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const groups = useMemo<ModelGroup[]>(() => {
    if (!rows) return [];
    const bucket = new Map<string, ModelGroup>();
    for (const r of rows) {
      const key = `${r.make ?? ""}||${r.model ?? ""}`;
      const g: ModelGroup = bucket.get(key) ?? {
        make: r.make ?? "",
        model: r.model ?? "",
        devices: [],
        versions: new Map(),
        unknownFirmware: 0,
        targetVersion: r.target_version ?? "",
        docsURL: r.docs_url ?? "",
      };
      g.devices.push(r);
      // Rows share the same target/docs per (make, model), but be robust
      // if the JOIN returned a value on one row and not another.
      if (r.target_version) g.targetVersion = r.target_version;
      if (r.docs_url) g.docsURL = r.docs_url;
      if (!r.firmware_version) {
        g.unknownFirmware += 1;
      } else {
        g.versions.set(r.firmware_version, (g.versions.get(r.firmware_version) ?? 0) + 1);
      }
      bucket.set(key, g);
    }
    return Array.from(bucket.values()).sort((a, b) => {
      // Groups with a target set + drift first, then groups with no target
      // but multiple versions (interesting), then everything else.
      const aDrift = a.targetVersion ? aBehindTarget(a) : a.versions.size > 1 ? 1 : 0;
      const bDrift = b.targetVersion ? aBehindTarget(b) : b.versions.size > 1 ? 1 : 0;
      if (aDrift !== bDrift) return bDrift - aDrift;
      return `${a.make} ${a.model}`.localeCompare(`${b.make} ${b.model}`);
    });
  }, [rows]);

  const totals = useMemo(() => {
    if (!rows) return { devices: 0, unknown: 0, distinctVersions: 0 };
    const unique = new Set<string>();
    let unknown = 0;
    for (const r of rows) {
      if (!r.firmware_version) unknown++;
      else unique.add(r.firmware_version);
    }
    return {
      devices: rows.length,
      unknown,
      distinctVersions: unique.size,
    };
  }, [rows]);

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div>
          <h1 className="text-xl font-semibold">Firmware</h1>
          <p className="text-sm text-muted-foreground">
            Actual versions in the fleet per make and model — set an approved
            target per model to enable drift badges.
          </p>
        </div>
        <UserMenu />
      </header>

      {editing && (
        <Modal
          open
          onClose={() => setEditing(null)}
          title={`Firmware policy for ${editing.make} ${editing.model}`}
          wide={false}
        >
          <FirmwareTargetForm
            make={editing.make}
            model={editing.model}
            initial={rows?.find(
              (r) => r.make === editing.make && r.model === editing.model
            )}
            onCancel={() => setEditing(null)}
            onSuccess={() => {
              setEditing(null);
              void load();
            }}
          />
        </Modal>
      )}

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        {error && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
              {error}
              <Button size="sm" variant="ghost" onClick={() => load()}>
                <RefreshCcw className="h-3 w-3" />
                Retry
              </Button>
            </CardContent>
          </Card>
        )}

        <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
          <SummaryCard label="Total devices" value={totals.devices} />
          <SummaryCard label="Distinct versions" value={totals.distinctVersions} />
          <SummaryCard
            label="No firmware reported"
            value={totals.unknown}
            tone={totals.unknown > 0 ? "warn" : undefined}
          />
        </div>

        <section className="space-y-2">
          <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wide">
            By make / model
          </h2>
          {rows === null ? (
            <div className="grid gap-2 md:grid-cols-2">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-32" />
              ))}
            </div>
          ) : groups.length === 0 ? (
            <Card>
              <CardContent className="p-10 text-center text-sm text-muted-foreground">
                No devices yet.
              </CardContent>
            </Card>
          ) : (
            <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
              {groups.map((g) => (
                <ModelCard
                  key={`${g.make}||${g.model}`}
                  group={g}
                  admin={admin}
                  onEdit={() => setEditing({ make: g.make, model: g.model })}
                />
              ))}
            </div>
          )}
        </section>

        <p className="text-[11px] text-muted-foreground flex items-center gap-1">
          <Cpu className="h-3 w-3" />
          No automatic "outdated" flag — versions shown as-is. Set an approved
          target per model to enable drift badging and link to vendor release
          notes.
        </p>
      </div>
    </div>
  );
}

// aBehindTarget returns 1 if any device in the group is not on the target
// version. Used for sort weighting so problematic groups float to the top.
function aBehindTarget(g: ModelGroup): number {
  if (!g.targetVersion) return 0;
  for (const v of g.versions.keys()) {
    if (v !== g.targetVersion) return 1;
  }
  return 0;
}

function ModelCard({
  group,
  admin,
  onEdit,
}: {
  group: ModelGroup;
  admin: boolean;
  onEdit: () => void;
}) {
  const label =
    group.make || group.model
      ? `${group.make} ${group.model}`.trim()
      : "(unknown make/model)";

  // Sort versions descending by count (busiest first), then by string.
  const versionEntries = useMemo(
    () =>
      Array.from(group.versions.entries()).sort((a, b) => {
        if (b[1] !== a[1]) return b[1] - a[1];
        return a[0].localeCompare(b[0]);
      }),
    [group.versions]
  );

  const total = group.devices.length;
  const behind = group.targetVersion
    ? group.devices.filter(
        (d) => d.firmware_version && d.firmware_version !== group.targetVersion
      ).length
    : 0;
  const onTarget = group.targetVersion
    ? group.devices.filter((d) => d.firmware_version === group.targetVersion).length
    : 0;

  const tone = group.targetVersion
    ? behind > 0
      ? "border-amber-500/40 bg-amber-500/5"
      : "border-emerald-500/40 bg-emerald-500/5"
    : "";

  return (
    <Card className={`border ${tone}`}>
      <CardContent className="p-3 space-y-2">
        <div className="flex items-start gap-2">
          <Cpu className="h-4 w-4 text-muted-foreground mt-0.5 shrink-0" />
          <div className="flex-1 min-w-0">
            <div className="font-medium truncate">{label}</div>
            <div className="text-[11px] text-muted-foreground">
              {total} device{total === 1 ? "" : "s"}
            </div>
          </div>
          {group.targetVersion && behind === 0 && (
            <CheckCircle2 className="h-4 w-4 text-emerald-500 shrink-0" />
          )}
          {admin && (
            <Button
              size="sm"
              variant="ghost"
              aria-label="Edit firmware policy"
              className="h-7 w-7 p-0"
              onClick={onEdit}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>

        {group.targetVersion && (
          <div className="flex items-center gap-2 text-xs">
            <span className="text-muted-foreground">Approved:</span>
            <span className="font-mono">{group.targetVersion}</span>
            {behind > 0 && (
              <span className="ml-auto rounded bg-amber-500/15 text-amber-700 dark:text-amber-300 px-1.5 py-0.5 text-[11px] font-medium">
                {behind} behind
              </span>
            )}
            {behind === 0 && onTarget > 0 && (
              <span className="ml-auto text-[11px] text-emerald-600">
                All on target
              </span>
            )}
          </div>
        )}

        <ul className="space-y-0.5">
          {versionEntries.map(([version, count]) => {
            const isTarget = version === group.targetVersion;
            const isBehind = group.targetVersion && !isTarget;
            return (
              <li
                key={version}
                className="flex items-center gap-2 text-xs"
              >
                <span
                  className={`h-1.5 w-1.5 rounded-full shrink-0 ${
                    isTarget
                      ? "bg-emerald-500"
                      : isBehind
                      ? "bg-amber-500"
                      : "bg-muted-foreground/50"
                  }`}
                />
                <span className="font-mono flex-1 truncate">{version}</span>
                <span className="text-muted-foreground tabular-nums">
                  {count} device{count === 1 ? "" : "s"}
                </span>
              </li>
            );
          })}
          {group.unknownFirmware > 0 && (
            <li className="flex items-center gap-2 text-xs">
              <span className="h-1.5 w-1.5 rounded-full shrink-0 bg-muted-foreground/30" />
              <span className="text-muted-foreground italic flex-1">
                No firmware reported
              </span>
              <span className="text-muted-foreground tabular-nums">
                {group.unknownFirmware} device
                {group.unknownFirmware === 1 ? "" : "s"}
              </span>
            </li>
          )}
        </ul>

        {group.docsURL && (
          <a
            href={group.docsURL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground pt-1"
          >
            <ExternalLink className="h-3 w-3" />
            Vendor release notes
          </a>
        )}

        <details className="pt-1">
          <summary className="cursor-pointer text-[11px] text-muted-foreground hover:text-foreground">
            {total} device{total === 1 ? "" : "s"} in this group
          </summary>
          <ul className="mt-1 space-y-0.5 border-t pt-1">
            {group.devices.map((d) => {
              const isTarget = d.firmware_version === group.targetVersion;
              const isBehind =
                group.targetVersion &&
                d.firmware_version &&
                d.firmware_version !== group.targetVersion;
              return (
                <li key={d.device_id} className="flex items-center gap-2 text-xs">
                  <Link
                    href={`/devices/${encodeURIComponent(d.device_id)}`}
                    className="flex-1 truncate hover:underline"
                  >
                    {d.name}
                  </Link>
                  <span className="font-mono text-[10px]">
                    {d.firmware_version || (
                      <span className="text-muted-foreground italic">no fw</span>
                    )}
                  </span>
                  {isTarget && (
                    <CheckCircle2 className="h-3 w-3 text-emerald-500 shrink-0" />
                  )}
                  {isBehind && (
                    <AlertTriangle className="h-3 w-3 text-amber-500 shrink-0" />
                  )}
                </li>
              );
            })}
          </ul>
        </details>
      </CardContent>
    </Card>
  );
}

function SummaryCard({
  label,
  value,
  tone,
}: {
  label: string;
  value: string | number;
  tone?: "warn";
}) {
  return (
    <Card>
      <CardContent className="p-3">
        <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
        <div
          className={`mt-0.5 text-xl font-semibold ${
            tone === "warn" ? "text-amber-600" : ""
          }`}
        >
          {value}
        </div>
      </CardContent>
    </Card>
  );
}

interface FirmwareTargetFormProps {
  make: string;
  model: string;
  initial?: FirmwareRow;
  onCancel: () => void;
  onSuccess: () => void;
}

function FirmwareTargetForm({
  make,
  model,
  initial,
  onCancel,
  onSuccess,
}: FirmwareTargetFormProps) {
  const [targetVersion, setTargetVersion] = useState(initial?.target_version ?? "");
  const [docsURL, setDocsURL] = useState(initial?.docs_url ?? "");
  const [notes, setNotes] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // If the group has neither make nor model (unknown vendor), we can't key
  // a target row against it — the (make, model) unique constraint would fail
  // with empty strings across multiple such groups.
  if (!make || !model) {
    return (
      <div className="space-y-3 text-sm">
        <p>
          Can't set a firmware policy for devices without a make and model. Set
          those tags on the devices first, then policy can be applied per
          model.
        </p>
        <div className="flex justify-end pt-2 border-t">
          <Button onClick={onCancel}>Close</Button>
        </div>
      </div>
    );
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!targetVersion.trim() && !docsURL.trim() && !notes.trim()) {
      setError("Set at least one field (target version, docs URL, or notes).");
      return;
    }
    setSubmitting(true);
    try {
      const body: FirmwareTargetBody = {
        make,
        model,
        target_version: targetVersion.trim() || undefined,
        docs_url: docsURL.trim() || undefined,
        notes: notes.trim() || undefined,
      };
      await api.upsertFirmwareTarget(body);
      onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs">
        <span className="text-muted-foreground">Applies to:</span>{" "}
        <span className="font-medium">
          {make} {model}
        </span>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Approved firmware version
        </label>
        <input
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm font-mono"
          value={targetVersion}
          onChange={(e) => setTargetVersion(e.target.value)}
          placeholder="3.14.1"
        />
        <p className="mt-1 text-[11px] text-muted-foreground">
          When set, devices not on this version are flagged as behind.
          Optional — leave blank to just publish the docs link.
        </p>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Vendor release notes URL
        </label>
        <input
          type="url"
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          value={docsURL}
          onChange={(e) => setDocsURL(e.target.value)}
          placeholder="https://vendor.example.com/release-notes"
        />
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Notes (optional)
        </label>
        <textarea
          rows={2}
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder='e.g. "Wait for LTS release"'
        />
      </div>

      <div className="flex justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Save policy
        </Button>
      </div>
    </form>
  );
}
