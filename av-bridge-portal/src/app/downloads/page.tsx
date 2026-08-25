"use client";

// Downloads — where operators grab the on-prem collector binaries.
// Fetches the cloud's /public/downloads catalogue so adding a new
// artefact only needs a Dockerfile COPY + a downloadCatalogue entry
// on the cloud side; this page reflects it automatically.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  AlertTriangle,
  Cpu,
  Download as DownloadIcon,
  FileArchive,
  Loader2,
  RefreshCcw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { api } from "@/lib/api";
import type { DownloadItem } from "@/lib/api";

// Presentation metadata — the cloud catalogue only cares about
// serving the file; friendly names + descriptions live here so a
// customer sees "AV Bridge Collector for Windows" instead of a raw
// filename. Keyed by the artefact key so a new server-side entry
// still renders (as a plain filename) even if we forget to add a
// mapping here.
const PRESENTATION: Record<
  string,
  { title: string; blurb: string; platform: string }
> = {
  "av-bridge-windows-amd64.exe": {
    title: "AV Bridge Collector",
    blurb:
      "On-prem collector binary. Windows 10 / 11 and Windows Server 2016+. Runs as a Windows Service.",
    platform: "Windows · amd64",
  },
  "av-bridge-linux-amd64": {
    title: "AV Bridge Collector",
    blurb:
      "On-prem collector binary. Ubuntu / Debian / RHEL x86_64 hosts. Runs as a systemd unit.",
    platform: "Linux · amd64",
  },
  "av-bridge-linux-arm64": {
    title: "AV Bridge Collector",
    blurb:
      "On-prem collector binary for ARM64 hosts — Raspberry Pi 4/5, AWS Graviton, Ampere. Runs as a systemd unit.",
    platform: "Linux · arm64",
  },
};

function formatBytes(n: number): string {
  if (n <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

export default function DownloadsPage() {
  const [items, setItems] = useState<DownloadItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await api.listDownloads(signal);
      if (signal?.aborted) return;
      // Stable sort — key ascending — so the list layout doesn't
      // jump between polls if the server returns them in a
      // different order.
      setItems([...data].sort((a, b) => a.key.localeCompare(b.key)));
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

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div>
          <h1 className="text-xl font-semibold">Downloads</h1>
          <p className="text-sm text-muted-foreground">
            Collector binaries and installers. Same file that the
            enrolment one-liner would fetch.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void load()}>
            <RefreshCcw className="h-3.5 w-3.5" />
            Refresh
          </Button>
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-6">
        <div className="mx-auto max-w-4xl space-y-4">
          {error && (
            <Card className="border-destructive/30 bg-destructive/5">
              <CardContent className="flex items-start gap-2 p-4 text-sm">
                <AlertTriangle className="mt-0.5 h-4 w-4 [color:hsl(var(--destructive))]" />
                <div>
                  <div className="font-medium [color:hsl(var(--destructive))]">
                    Couldn&apos;t load downloads
                  </div>
                  <div className="mt-0.5 text-muted-foreground">{error}</div>
                </div>
              </CardContent>
            </Card>
          )}

          {items === null ? (
            <div className="space-y-2">
              <Skeleton className="h-24" />
              <Skeleton className="h-24" />
            </div>
          ) : items.length === 0 ? (
            <Card>
              <CardContent className="p-10 text-center text-sm text-muted-foreground">
                No downloads available yet.
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-3">
              {items.map((it) => (
                <DownloadCard key={it.key} item={it} />
              ))}
            </div>
          )}

          <div className="mt-2 rounded-md border border-input bg-muted/20 p-4 text-xs text-muted-foreground">
            <div className="mb-1 flex items-center gap-1.5 font-medium text-foreground">
              <Cpu className="h-3.5 w-3.5" />
              Fresh install
            </div>
            <p className="leading-relaxed">
              Prefer the one-liner from the{" "}
              <Link
                href="/collectors"
                className="text-primary underline-offset-4 hover:underline"
              >
                Collectors page
              </Link>{" "}
              — it downloads the binary, redeems the enrolment token,
              writes the config, and registers the service in a
              single copy-paste. Grab the raw file here only when
              you need to sideload it (air-gapped sites, mass
              deploys, etc).
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}

function DownloadCard({ item }: { item: DownloadItem }) {
  const [downloading, setDownloading] = useState(false);
  const meta = PRESENTATION[item.key] ?? {
    title: item.filename,
    blurb: "",
    platform: item.content_type,
  };

  const onDownload = () => {
    // Use a hidden anchor rather than window.location so the
    // Content-Disposition-driven filename lands correctly and the
    // browser doesn't try to navigate the SPA to a binary URL.
    setDownloading(true);
    const a = document.createElement("a");
    a.href = `/public/downloads/${encodeURIComponent(item.key)}`;
    a.download = item.filename;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    // Short delay is cosmetic — the click hands off to the browser
    // immediately; we just want the spinner to flash so the user
    // sees the click registered.
    window.setTimeout(() => setDownloading(false), 800);
  };

  return (
    <Card>
      <CardContent className="flex flex-wrap items-start gap-4 p-4">
        <div className="grid h-10 w-10 shrink-0 place-items-center rounded-md bg-muted">
          <FileArchive className="h-5 w-5 text-muted-foreground" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <div className="truncate text-sm font-semibold">{meta.title}</div>
            <span className="rounded border border-border bg-muted/40 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
              {meta.platform}
            </span>
          </div>
          {meta.blurb && (
            <p className="mt-1 text-xs text-muted-foreground">{meta.blurb}</p>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
            <span className="font-mono">{item.filename}</span>
            {item.available && <span>{formatBytes(item.size_bytes)}</span>}
            {!item.available && (
              <span className="[color:hsl(var(--warning))]">
                Not baked into this deploy yet
              </span>
            )}
          </div>
        </div>
        <div className="ml-auto">
          <Button
            type="button"
            onClick={onDownload}
            disabled={!item.available || downloading}
          >
            {downloading ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <DownloadIcon className="h-3.5 w-3.5" />
            )}
            Download
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
