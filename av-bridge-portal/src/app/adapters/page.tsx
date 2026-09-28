"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Boxes,
  Check,
  ChevronRight,
  Copy,
  ExternalLink,
  Plug,
  Power,
  Puzzle,
  Search,
  Signal,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { AdapterInfo, AdapterKind } from "@/lib/types";

// A compact, searchable list: one row per adapter, grouped by kind, with the
// full reference (commands, metrics, config fields, example) in a modal.
// Vendor integrations sit first — that's what people look for ("do you
// support my Poly?"). The device-count badge doubles as ops insight: what's
// actually in play on this tenant. #<adapter-id> deep-links to the details.

const KIND_ORDER: AdapterKind[] = ["vendor", "transport", "probe"];

const KIND_META: Record<
  AdapterKind,
  { label: string; short: string; blurb: string; Icon: React.ComponentType<{ className?: string }> }
> = {
  vendor: {
    label: "Vendor integrations",
    short: "Vendor",
    blurb: "Native adapters that speak each vendor's protocol — full command sets and rich metrics.",
    Icon: Puzzle,
  },
  transport: {
    label: "Generic transports",
    short: "Generic",
    blurb: "Protocol building blocks for gear without a dedicated adapter. Bring your own commands.",
    Icon: Plug,
  },
  probe: {
    label: "Probes",
    short: "Probes",
    blurb: "Reachability checks for devices that don't expose a control API.",
    Icon: Signal,
  },
};

type KindFilter = AdapterKind | "all";

export default function AdaptersPage() {
  const fetcher = useCallback(
    (signal: AbortSignal) => api.listAdapters(signal),
    []
  );
  const { data, loading, error, refresh } = usePolling<AdapterInfo[]>(
    fetcher,
    30_000
  );

  const [search, setSearch] = useState("");
  const [kind, setKind] = useState<KindFilter>("all");
  const [deviceType, setDeviceType] = useState("");
  const [inUseOnly, setInUseOnly] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);

  // Deep link: #cisco_roomos opens that adapter's details.
  useEffect(() => {
    const fromHash = () => {
      const id = decodeURIComponent(window.location.hash.slice(1));
      setOpenId(id || null);
    };
    fromHash();
    window.addEventListener("hashchange", fromHash);
    return () => window.removeEventListener("hashchange", fromHash);
  }, []);

  const openAdapter = (id: string | null) => {
    setOpenId(id);
    const url = id ? `#${encodeURIComponent(id)}` : window.location.pathname + window.location.search;
    window.history.replaceState(null, "", url);
  };

  const all = useMemo(() => data ?? [], [data]);
  const deviceTypes = useMemo(
    () => Array.from(new Set(all.flatMap((a) => a.device_types))).sort(),
    [all]
  );
  const kindCounts = useMemo(() => {
    const c: Record<KindFilter, number> = { all: all.length, vendor: 0, transport: 0, probe: 0 };
    for (const a of all) c[a.kind] = (c[a.kind] ?? 0) + 1;
    return c;
  }, [all]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return all
      .filter((a) => kind === "all" || a.kind === kind)
      .filter((a) => !deviceType || a.device_types.includes(deviceType))
      .filter((a) => !inUseOnly || a.device_count > 0)
      .filter(
        (a) =>
          !q ||
          [a.name, a.vendor ?? "", a.id, a.description, ...a.device_types]
            .join(" ")
            .toLowerCase()
            .includes(q)
      )
      .sort((x, y) => x.name.localeCompare(y.name));
  }, [all, search, kind, deviceType, inUseOnly]);

  const grouped = useMemo(() => {
    const g: Record<AdapterKind, AdapterInfo[]> = { vendor: [], transport: [], probe: [] };
    for (const a of filtered) g[a.kind]?.push(a);
    return g;
  }, [filtered]);

  const inUseCount = all.filter((a) => a.device_count > 0).length;
  const anyFilter = search !== "" || kind !== "all" || deviceType !== "" || inUseOnly;
  const clearFilters = () => {
    setSearch("");
    setKind("all");
    setDeviceType("");
    setInUseOnly(false);
  };
  const open = all.find((a) => a.id === openId) ?? null;

  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
              <Puzzle aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div>
              <h1 className="text-xl font-semibold leading-tight">Adapters</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                What each adapter does, how to configure it, and how many devices are using it.
              </p>
            </div>
          </div>
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 px-6 py-6">
        <div className="mx-auto max-w-6xl space-y-6">
          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))] flex items-center justify-between">
              <span>Failed to load adapters: {error.message}</span>
              <button
                onClick={() => refresh()}
                className="text-xs underline underline-offset-2 hover:opacity-80"
              >
                Retry
              </button>
            </div>
          )}

          <Card>
            <CardContent className="p-4 space-y-3">
              <div className="flex flex-wrap items-center gap-3">
                <div className="relative flex-1 min-w-[220px]">
                  <Search
                    aria-hidden="true"
                    className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
                  />
                  <input
                    type="search"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder="Search vendor, model, protocol…"
                    aria-label="Search adapters"
                    className="w-full rounded-md border bg-background pl-9 pr-3 py-2 text-sm"
                  />
                </div>
                <select
                  value={deviceType}
                  onChange={(e) => setDeviceType(e.target.value)}
                  aria-label="Filter by device type"
                  className="rounded-md border bg-background px-3 py-2 text-sm"
                >
                  <option value="">All device types</option>
                  {deviceTypes.map((t) => (
                    <option key={t} value={t}>
                      {t.charAt(0).toUpperCase() + t.slice(1)}
                    </option>
                  ))}
                </select>
                <label className="flex items-center gap-2 text-sm text-muted-foreground cursor-pointer select-none">
                  <input
                    type="checkbox"
                    checked={inUseOnly}
                    onChange={(e) => setInUseOnly(e.target.checked)}
                    className="h-4 w-4 rounded border"
                  />
                  In use
                  {data && <span className="text-xs">({inUseCount})</span>}
                </label>
              </div>

              <div className="flex flex-wrap items-center justify-between gap-2">
                <div role="tablist" aria-label="Adapter kind" className="inline-flex rounded-md border bg-muted/40 p-0.5">
                  {(["all", ...KIND_ORDER] as KindFilter[]).map((k) => (
                    <button
                      key={k}
                      type="button"
                      role="tab"
                      aria-selected={kind === k}
                      onClick={() => setKind(k)}
                      className={cn(
                        "rounded px-3 py-1 text-xs font-medium transition-colors",
                        kind === k
                          ? "bg-background text-foreground shadow-sm"
                          : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      {k === "all" ? "All" : KIND_META[k].short}
                      <span className="ml-1.5 tabular-nums text-muted-foreground">{kindCounts[k]}</span>
                    </button>
                  ))}
                </div>
                {data && (
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    <span>
                      <span className="font-medium text-foreground tabular-nums">{filtered.length}</span> of{" "}
                      {all.length} adapters
                    </span>
                    {anyFilter && (
                      <button
                        type="button"
                        onClick={clearFilters}
                        className="inline-flex items-center gap-1 underline-offset-2 hover:underline hover:text-foreground"
                      >
                        <X aria-hidden="true" className="h-3 w-3" />
                        Clear filters
                      </button>
                    )}
                  </div>
                )}
              </div>
            </CardContent>
          </Card>

          {loading && !data && (
            <Card>
              <CardContent className="p-0 divide-y">
                {[0, 1, 2, 3, 4].map((i) => (
                  <div key={i} className="flex items-center gap-4 px-4 py-3">
                    <Skeleton className="h-4 w-48" />
                    <Skeleton className="h-4 flex-1" />
                    <Skeleton className="h-5 w-20" />
                  </div>
                ))}
              </CardContent>
            </Card>
          )}

          {data && filtered.length === 0 && (
            <div className="rounded-md border border-dashed px-4 py-10 text-center text-sm text-muted-foreground">
              No adapters match your filters.{" "}
              <button type="button" onClick={clearFilters} className="text-primary hover:underline">
                Clear filters
              </button>
            </div>
          )}

          {data &&
            KIND_ORDER.map((k) => {
              const items = grouped[k];
              if (items.length === 0) return null;
              const meta = KIND_META[k];
              const KindIcon = meta.Icon;
              return (
                <section key={k} className="space-y-2" aria-labelledby={`kind-${k}`}>
                  <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-1">
                    <KindIcon aria-hidden="true" className="h-4 w-4 self-center text-muted-foreground" />
                    <h2 id={`kind-${k}`} className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
                      {meta.label}
                    </h2>
                    <span className="text-xs text-muted-foreground tabular-nums">{items.length}</span>
                    <span className="hidden md:inline text-xs text-muted-foreground">· {meta.blurb}</span>
                  </div>
                  <Card>
                    <CardContent className="p-0">
                      <ul className="divide-y">
                        {items.map((a) => (
                          <li key={a.id}>
                            <AdapterRow adapter={a} onOpen={() => openAdapter(a.id)} />
                          </li>
                        ))}
                      </ul>
                    </CardContent>
                  </Card>
                </section>
              );
            })}
        </div>
      </div>

      <Modal open={open !== null} onClose={() => openAdapter(null)} title={open?.name ?? "Adapter"}>
        {open && <AdapterDetails adapter={open} />}
      </Modal>
    </div>
  );
}

// "9 commands · 17 metrics". Adapters with no built-in commands take the
// ones defined on each device; a zero metric count is left out.
function capabilitySummary(a: AdapterInfo): string {
  const commands = a.commands?.length ?? 0;
  const metrics = a.metrics?.length ?? 0;
  const parts = [
    a.dynamic_commands || commands === 0
      ? "Your commands"
      : `${commands} command${commands === 1 ? "" : "s"}`,
  ];
  if (metrics > 0) parts.push(`${metrics} metric${metrics === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

// Generic adapters work with every device type; one "Any device" chip
// reads better than five.
const ALL_TYPES_THRESHOLD = 4;

function DeviceTypeChips({ types }: { types: string[] }) {
  const shown =
    types.length >= ALL_TYPES_THRESHOLD
      ? ["Any device"]
      : types.map((t) => t.charAt(0).toUpperCase() + t.slice(1));
  return (
    <>
      {shown.map((t) => (
        <span
          key={t}
          title={types.length >= ALL_TYPES_THRESHOLD ? types.join(", ") : undefined}
          className="rounded-full border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground"
        >
          {t}
        </span>
      ))}
    </>
  );
}

function powerLabel(a: AdapterInfo): string | null {
  if (a.power.on && a.power.off) return "Power on/off";
  if (a.power.on) return "Power on";
  if (a.power.off) return "Power off";
  return null;
}

function UsageBadge({ count }: { count: number }) {
  const inUse = count > 0;
  return (
    <Badge variant={inUse ? "success" : "secondary"} className="flex-shrink-0 tabular-nums">
      <Boxes aria-hidden="true" className="h-3 w-3 mr-1" />
      {inUse ? `${count} device${count === 1 ? "" : "s"}` : "Not in use"}
    </Badge>
  );
}

function AdapterRow({ adapter: a, onOpen }: { adapter: AdapterInfo; onOpen: () => void }) {
  const power = powerLabel(a);
  return (
    <button
      type="button"
      onClick={onOpen}
      className="group grid w-full grid-cols-[1fr_auto] items-center gap-x-4 gap-y-1.5 px-4 py-3 text-left transition-colors hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:outline-none md:grid-cols-[minmax(0,1fr)_11rem_11rem_7.5rem_1rem]"
    >
      <div className="min-w-0">
        <div className="flex items-baseline gap-2 min-w-0">
          <span className="font-medium leading-tight truncate">{a.name}</span>
          {a.vendor && <span className="text-xs text-muted-foreground truncate">{a.vendor}</span>}
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground line-clamp-1">{a.description}</p>
      </div>

      <div className="col-start-1 row-start-2 flex flex-wrap items-center gap-1 md:col-start-auto md:row-start-auto">
        <DeviceTypeChips types={a.device_types} />
        {power && (
          <span className="inline-flex items-center gap-1 rounded-full border border-primary/40 bg-primary/5 px-2 py-0.5 text-[11px] text-primary">
            <Power aria-hidden="true" className="h-3 w-3" />
            {power}
          </span>
        )}
      </div>

      <div className="hidden md:block text-xs text-muted-foreground tabular-nums">{capabilitySummary(a)}</div>

      <div className="col-start-2 row-span-2 row-start-1 flex items-center justify-end md:col-start-auto md:row-span-1 md:row-start-auto">
        <UsageBadge count={a.device_count} />
      </div>

      <ChevronRight
        aria-hidden="true"
        className="hidden md:block h-4 w-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5 group-hover:text-foreground"
      />
    </button>
  );
}

function AdapterDetails({ adapter: a }: { adapter: AdapterInfo }) {
  const power = powerLabel(a);
  const commandCount = a.commands?.length ?? 0;
  const metricCount = a.metrics?.length ?? 0;
  return (
    <div className="space-y-5 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <code className="rounded bg-muted px-1.5 py-0.5 text-[11px] font-mono text-muted-foreground">{a.id}</code>
        {a.vendor && <span className="text-xs text-muted-foreground">{a.vendor}</span>}
        <span className="text-xs text-muted-foreground">· {KIND_META[a.kind].short}</span>
        <span className="ml-auto">
          <UsageBadge count={a.device_count} />
        </span>
      </div>

      <p className="text-muted-foreground leading-relaxed">{a.description}</p>

      <div className="flex flex-wrap gap-1.5">
        {a.device_types.map((t) => (
          <span key={t} className="rounded-full border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground capitalize">
            {t}
          </span>
        ))}
        {power && (
          <span className="inline-flex items-center gap-1 rounded-full border border-primary/40 bg-primary/5 px-2 py-0.5 text-[11px] text-primary">
            <Power aria-hidden="true" className="h-3 w-3" />
            {power}
          </span>
        )}
      </div>

      {a.dynamic_commands ? (
        <DetailSection title="Commands">
          <p className="text-xs text-muted-foreground leading-relaxed">
            No built-in commands. Add commands in the device form and each one becomes a button on the device page.
          </p>
        </DetailSection>
      ) : (
        commandCount > 0 && (
          <DetailSection title="Commands" count={commandCount}>
            <ChipList items={a.commands ?? []} />
          </DetailSection>
        )
      )}

      {metricCount > 0 && (
        <DetailSection title="Metrics" count={metricCount}>
          <ChipList items={a.metrics ?? []} muted />
        </DetailSection>
      )}

      {a.config_schema.length > 0 && (
        <DetailSection title="Settings">
          <div className="rounded-md border divide-y">
            {a.config_schema.map((f) => (
              <div key={f.name} className="grid gap-1 px-3 py-2 text-xs sm:grid-cols-[10rem_1fr] sm:gap-3">
                <div className="font-mono break-all">
                  {f.name}
                  {f.required && (
                    <span className="text-destructive ml-0.5" aria-label="required">
                      *
                    </span>
                  )}
                </div>
                <div className="text-muted-foreground leading-snug">
                  {f.description}
                  {f.example && (
                    <span className="block mt-0.5 font-mono text-[11px] text-foreground/60">e.g. {f.example}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </DetailSection>
      )}

      {a.example_config && <ExampleConfig yaml={a.example_config} />}

      {a.docs_url && (
        <a
          href={a.docs_url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
        >
          Vendor docs
          <ExternalLink aria-hidden="true" className="h-3 w-3" />
        </a>
      )}
    </div>
  );
}

function DetailSection({
  title,
  count,
  children,
}: {
  title: string;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
        {count !== undefined && <span className="ml-1.5 font-normal tabular-nums">{count}</span>}
      </div>
      {children}
    </div>
  );
}

function ChipList({ items, muted = false }: { items: string[]; muted?: boolean }) {
  return (
    <div className="flex flex-wrap gap-1">
      {items.map((it) => (
        <code
          key={it}
          className={cn(
            "rounded px-1.5 py-0.5 text-[11px] font-mono",
            muted ? "bg-muted/60 text-muted-foreground" : "bg-primary/10 text-primary"
          )}
        >
          {it}
        </code>
      ))}
    </div>
  );
}

function ExampleConfig({ yaml }: { yaml: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(yaml);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API blocked — user can still select the text manually
    }
  };
  return (
    <DetailSection title="Example (Collector YAML)">
      <div className="relative">
        <pre className="rounded-md border bg-muted/40 p-3 pr-10 text-[11px] font-mono leading-relaxed overflow-x-auto">
          {yaml}
        </pre>
        <Button
          variant="ghost"
          size="sm"
          onClick={copy}
          className="absolute top-1.5 right-1.5 h-7 w-7 p-0"
          aria-label="Copy example config"
        >
          {copied ? (
            <Check className="h-3.5 w-3.5 [color:hsl(var(--success))]" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </Button>
      </div>
    </DetailSection>
  );
}
