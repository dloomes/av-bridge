"use client";

// A Collector's own log, as it sends it to the cloud with each push (Info
// and above, kept 30 days). Newest first; follows new lines while you're
// at the top, and pages back with "Load older".

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { Download, Loader2, RefreshCw, ScrollText, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { BackLink } from "@/components/back-link";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { UserMenu } from "@/components/user-menu";
import { useToast } from "@/components/toast";
import { api, type CollectorLogFilter, type CollectorLogLine } from "@/lib/api";
import { cn } from "@/lib/utils";

const FOLLOW_MS = 15_000;
const PAGE = 200;

const LEVELS: { value: NonNullable<CollectorLogFilter["level"]>; label: string }[] = [
  { value: "info", label: "Everything" },
  { value: "warn", label: "Warnings and errors" },
  { value: "error", label: "Errors only" },
];

const LEVEL_BADGE: Record<CollectorLogLine["level"], { variant: "secondary" | "warning" | "offline" | "outline"; label: string }> = {
  DEBUG: { variant: "outline", label: "Debug" },
  INFO: { variant: "secondary", label: "Info" },
  WARN: { variant: "warning", label: "Warning" },
  ERROR: { variant: "offline", label: "Error" },
};

const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export default function CollectorLogsPage() {
  const { id } = useParams<{ id: string }>();
  const { toast } = useToast();
  const [name, setName] = useState("");
  const [level, setLevel] = useState<NonNullable<CollectorLogFilter["level"]>>("info");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState(""); // debounced query
  const [lines, setLines] = useState<CollectorLogLine[]>([]);
  const [nextBefore, setNextBefore] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [follow, setFollow] = useState(true);
  const paged = useRef(false); // older pages loaded: stop replacing the list

  useEffect(() => {
    const t = setTimeout(() => setSearch(query), 300);
    return () => clearTimeout(t);
  }, [query]);

  const loadLatest = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const page = await api.collectorLogs(id, { level, q: search, limit: PAGE }, signal);
        setName(page.collector.name);
        setLines(page.logs);
        setNextBefore(page.next_before);
        setError(null);
        paged.current = false;
      } catch (e) {
        if ((e as Error).name !== "AbortError") setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    },
    [id, level, search]
  );

  useEffect(() => {
    const ctl = new AbortController();
    setLoading(true);
    void loadLatest(ctl.signal);
    return () => ctl.abort();
  }, [loadLatest]);

  useEffect(() => {
    if (!follow) return;
    const t = setInterval(() => {
      if (!paged.current && document.visibilityState === "visible") void loadLatest();
    }, FOLLOW_MS);
    return () => clearInterval(t);
  }, [follow, loadLatest]);

  const loadOlder = async () => {
    if (!nextBefore) return;
    setLoadingOlder(true);
    try {
      const page = await api.collectorLogs(id, { level, q: search, limit: PAGE, before: nextBefore });
      paged.current = true;
      setLines((prev) => [...prev, ...page.logs]);
      setNextBefore(page.next_before);
    } catch (e) {
      toast({ variant: "destructive", title: "Couldn't load older lines", description: (e as Error).message });
    } finally {
      setLoadingOlder(false);
    }
  };

  const download = async () => {
    setDownloading(true);
    try {
      const text = await api.collectorLogsText(id, { level, q: search });
      const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
      const a = document.createElement("a");
      a.href = url;
      const safe = (name || "collector").replace(/[^A-Za-z0-9._-]+/g, "-");
      a.download = `${safe}-logs-${new Date().toISOString().slice(0, 10)}.txt`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      toast({ variant: "destructive", title: "Download failed", description: (e as Error).message });
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className="flex min-h-app flex-col">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 items-center gap-3">
            <Button asChild variant="ghost" size="sm" className="shrink-0">
              <BackLink href="/collectors" label="Collectors" iconClassName="h-3.5 w-3.5" />
            </Button>
            <div className="h-9 w-9 shrink-0 rounded-md bg-primary/10 flex items-center justify-center">
              <ScrollText aria-hidden="true" className="h-4 w-4 text-primary" />
            </div>
            <div className="min-w-0">
              <h1 className="text-xl font-semibold leading-tight truncate">{name ? `${name} logs` : "Collector logs"}</h1>
              <p className="text-sm text-muted-foreground leading-tight">
                What the Collector logged, newest first. Kept for 30 days.
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={download} disabled={downloading || lines.length === 0}>
              {downloading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
              Download
            </Button>
            <UserMenu />
          </div>
        </div>
      </header>

      <div className="flex-1 px-6 py-6">
        <div className="mx-auto max-w-6xl space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative min-w-[12rem] flex-1">
              <Search aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <input
                aria-label="Search logs"
                className={`${inputClass} pl-9`}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search messages and details, e.g. a device name or IP"
                spellCheck={false}
              />
            </div>
            <select
              aria-label="Levels shown"
              className={cn(inputClass, "w-auto")}
              value={level}
              onChange={(e) => setLevel(e.target.value as typeof level)}
            >
              {LEVELS.map((l) => (
                <option key={l.value} value={l.value}>{l.label}</option>
              ))}
            </select>
            <label className="inline-flex items-center gap-2 text-sm text-muted-foreground">
              <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
              Follow new lines
            </label>
            <Button variant="ghost" size="icon" aria-label="Refresh" onClick={() => { setLoading(true); void loadLatest(); }}>
              <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
            </Button>
          </div>

          {error && (
            <Card className="border-destructive/30 bg-destructive/5">
              <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
                Couldn&apos;t load the logs: {error}
              </CardContent>
            </Card>
          )}

          <Card>
            <CardContent className="p-0">
              {loading && lines.length === 0 ? (
                <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" /> Loading…
                </div>
              ) : lines.length === 0 ? (
                <div className="p-6 text-sm text-muted-foreground space-y-1">
                  <p className="font-medium text-foreground">No log lines{search || level !== "info" ? " match" : " yet"}.</p>
                  {!search && level === "info" && (
                    <p>
                      Collectors on version 1.0.55 or later send their log to the cloud with every
                      update, so lines appear here within a minute. Older Collectors need updating first.
                    </p>
                  )}
                </div>
              ) : (
                <ol className="divide-y font-mono text-xs">
                  {lines.map((l) => (
                    <LogRow key={l.id} line={l} />
                  ))}
                </ol>
              )}
            </CardContent>
          </Card>

          {nextBefore && (
            <div className="flex justify-center">
              <Button variant="outline" size="sm" onClick={loadOlder} disabled={loadingOlder}>
                {loadingOlder && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                Load older
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function LogRow({ line }: { line: CollectorLogLine }) {
  const badge = LEVEL_BADGE[line.level] ?? LEVEL_BADGE.INFO;
  const when = new Date(line.ts);
  const attrs = Object.entries(line.attrs ?? {});
  return (
    <li
      className={cn(
        "grid grid-cols-1 gap-x-3 gap-y-1 px-4 py-2 sm:grid-cols-[10.5rem_5.5rem_minmax(0,1fr)]",
        line.level === "ERROR" && "bg-destructive/5",
        line.level === "WARN" && "bg-warning/5"
      )}
    >
      {/* Phones: time and level share a line; wider screens: columns. */}
      <div className="flex items-center gap-2 sm:contents">
        <time dateTime={line.ts} title={when.toISOString()} className="text-muted-foreground tabular-nums">
          {when.toLocaleString(undefined, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" })}
        </time>
        <div>
          <Badge variant={badge.variant} className="font-sans text-[11px]">{badge.label}</Badge>
        </div>
      </div>
      <div className="min-w-0 [overflow-wrap:anywhere]">
        <span className="text-foreground">{line.msg}</span>
        {attrs.map(([k, v]) => (
          <span key={k} className="ml-2 text-muted-foreground">
            {k}=<span className="text-foreground/80">{typeof v === "string" ? v : JSON.stringify(v)}</span>
          </span>
        ))}
      </div>
    </li>
  );
}
