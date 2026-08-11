"use client";

import { useCallback, useMemo, useState } from "react";
import {
  Boxes,
  Check,
  ChevronDown,
  Copy,
  ExternalLink,
  Plug,
  Puzzle,
  Signal,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { AdapterInfo, AdapterKind } from "@/lib/types";

// One card per adapter. Vendor-specific integrations sit first — those are
// what people care about ("do you support my Poly?"), transports and the
// ping probe follow. The device_count pill turns the page into ops insight
// on top of docs: "aurora_vpx · 2 devices" says at a glance what's in play.

const KIND_ORDER: AdapterKind[] = ["vendor", "transport", "probe"];

const KIND_META: Record<AdapterKind, { label: string; blurb: string; Icon: React.ComponentType<{ className?: string }> }> = {
  vendor: {
    label: "Vendor integrations",
    blurb: "Native adapters that speak each vendor's protocol — full command sets and rich metrics.",
    Icon: Puzzle,
  },
  transport: {
    label: "Generic transports",
    blurb: "Protocol building blocks for gear without a dedicated adapter. Bring your own commands.",
    Icon: Plug,
  },
  probe: {
    label: "Probes",
    blurb: "Reachability checks for devices that don't expose a control API — a switch, an IP camera, a legacy panel.",
    Icon: Signal,
  },
};

export default function AdaptersPage() {
  const fetcher = useCallback(
    (signal: AbortSignal) => api.listAdapters(signal),
    []
  );
  const { data, loading, error, refresh } = usePolling<AdapterInfo[]>(
    fetcher,
    30_000
  );

  const grouped = useMemo(() => {
    const g: Record<AdapterKind, AdapterInfo[]> = {
      vendor: [],
      transport: [],
      probe: [],
    };
    for (const a of data ?? []) g[a.kind]?.push(a);
    return g;
  }, [data]);

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
        <div className="mx-auto max-w-6xl space-y-8">
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

          {loading && !data && (
            <div className="grid gap-4 sm:grid-cols-2">
              {[0, 1, 2, 3].map((i) => (
                <Card key={i}>
                  <CardContent className="p-5 space-y-3">
                    <Skeleton className="h-5 w-40" />
                    <Skeleton className="h-4 w-full" />
                    <Skeleton className="h-4 w-3/4" />
                  </CardContent>
                </Card>
              ))}
            </div>
          )}

          {data &&
            KIND_ORDER.map((kind) => {
              const items = grouped[kind];
              if (items.length === 0) return null;
              const meta = KIND_META[kind];
              const KindIcon = meta.Icon;
              return (
                <section key={kind} className="space-y-3">
                  <div className="flex items-baseline gap-2">
                    <KindIcon
                      aria-hidden="true"
                      className="h-4 w-4 text-muted-foreground"
                    />
                    <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
                      {meta.label}
                    </h2>
                    <span className="text-xs text-muted-foreground">
                      · {meta.blurb}
                    </span>
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    {items.map((a) => (
                      <AdapterCard key={a.id} adapter={a} />
                    ))}
                  </div>
                </section>
              );
            })}
        </div>
      </div>
    </div>
  );
}

function AdapterCard({ adapter }: { adapter: AdapterInfo }) {
  const [showDetails, setShowDetails] = useState(false);
  const inUse = adapter.device_count > 0;
  const commandCount = adapter.commands?.length ?? 0;
  const metricCount = adapter.metrics?.length ?? 0;

  return (
    <Card className="flex flex-col">
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <h3 className="font-semibold leading-tight">{adapter.name}</h3>
              <code className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground">
                {adapter.id}
              </code>
            </div>
            {adapter.vendor && (
              <div className="text-xs text-muted-foreground mt-1">
                {adapter.vendor}
              </div>
            )}
          </div>
          <Badge
            variant={inUse ? "success" : "secondary"}
            className="flex-shrink-0"
          >
            <Boxes className="h-3 w-3 mr-1" />
            {inUse
              ? `${adapter.device_count} device${adapter.device_count === 1 ? "" : "s"}`
              : "Not in use"}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="flex-1 pt-0 space-y-4">
        <p className="text-sm text-muted-foreground leading-relaxed">
          {adapter.description}
        </p>

        <div className="flex flex-wrap gap-1.5">
          {adapter.device_types.map((t) => (
            <span
              key={t}
              className="rounded-full border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground capitalize"
            >
              {t}
            </span>
          ))}
          {(adapter.power.on || adapter.power.off) && (
            <span className="rounded-full border border-primary/40 bg-primary/5 px-2 py-0.5 text-[11px] text-primary">
              {adapter.power.on && adapter.power.off
                ? "Power on/off"
                : adapter.power.on
                ? "Power on"
                : "Power off"}
            </span>
          )}
        </div>

        <button
          type="button"
          onClick={() => setShowDetails((v) => !v)}
          className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
          aria-expanded={showDetails}
        >
          <ChevronDown
            className={cn(
              "h-3.5 w-3.5 transition-transform",
              showDetails && "rotate-180"
            )}
          />
          {adapter.dynamic_commands
            ? `${metricCount} metrics · commands user-defined`
            : `${commandCount} command${commandCount === 1 ? "" : "s"} · ${metricCount} metric${metricCount === 1 ? "" : "s"}`}
        </button>

        {showDetails && (
          <div className="space-y-4 text-xs">
            {adapter.dynamic_commands ? (
              <DetailSection title="Commands">
                <p className="text-muted-foreground leading-relaxed">
                  This adapter has no built-in named commands. Add a{" "}
                  <code className="rounded bg-muted px-1 py-0.5">commands:</code>{" "}
                  map on each device and each entry becomes a portal button.
                </p>
              </DetailSection>
            ) : (
              commandCount > 0 && (
                <DetailSection title="Commands">
                  <ChipList items={adapter.commands ?? []} />
                </DetailSection>
              )
            )}
            {metricCount > 0 && (
              <DetailSection title="Metrics">
                <ChipList items={adapter.metrics ?? []} muted />
              </DetailSection>
            )}
            {adapter.config_schema.length > 0 && (
              <DetailSection title="Config fields">
                <table className="w-full text-[11px]">
                  <tbody>
                    {adapter.config_schema.map((f) => (
                      <tr key={f.name} className="align-top">
                        <td className="pr-3 py-1 font-mono whitespace-nowrap">
                          {f.name}
                          {f.required && (
                            <span className="text-destructive ml-0.5">*</span>
                          )}
                        </td>
                        <td className="py-1 text-muted-foreground leading-snug">
                          {f.description}
                          {f.example && (
                            <span className="block mt-0.5 font-mono text-[10px] text-foreground/60">
                              e.g. {f.example}
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </DetailSection>
            )}
            <ExampleConfig yaml={adapter.example_config} />
            {adapter.docs_url && (
              <a
                href={adapter.docs_url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
              >
                Vendor docs
                <ExternalLink className="h-3 w-3" />
              </a>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function DetailSection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
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
            muted
              ? "bg-muted/60 text-muted-foreground"
              : "bg-primary/10 text-primary"
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
    <DetailSection title="Example config (poc-config.yaml)">
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
