"use client";

import { useCallback, useMemo, useState } from "react";
import Link from "next/link";
import {
  AlertTriangle,
  ArrowLeft,
  Bell,
  Check,
  CheckCircle2,
  Info,
  Loader2,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { canOperate } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type { AlertItem, AlertSeverity, AlertStatus } from "@/lib/types";

type Tab = "open" | "acknowledged" | "resolved" | "all";

const TAB_QUERY: Record<Tab, string | undefined> = {
  open: "open",
  acknowledged: "acknowledged",
  resolved: "resolved",
  all: undefined,
};

const SEVERITY_TONE: Record<AlertSeverity, string> = {
  critical: "border-red-500/40 bg-red-500/5",
  warning: "border-amber-500/40 bg-amber-500/5",
  info: "border-sky-500/40 bg-sky-500/5",
};

const SEVERITY_ICON: Record<AlertSeverity, React.ComponentType<{ className?: string }>> = {
  critical: AlertTriangle,
  warning: AlertTriangle,
  info: Info,
};

const STATUS_LABEL: Record<AlertStatus, string> = {
  open: "Open",
  acknowledged: "Acknowledged",
  resolved: "Resolved",
};

export default function AlertsPage() {
  const session = useSession();
  const operator = canOperate(session.user?.role);
  const [tab, setTab] = useState<Tab>("open");
  const [busy, setBusy] = useState<Record<string, "ack" | "resolve" | undefined>>({});
  const [error, setError] = useState<string | null>(null);

  const fetcher = useCallback(
    (signal: AbortSignal) =>
      api.listAlerts({ status: TAB_QUERY[tab], limit: 200 }, signal),
    [tab]
  );
  const { data, loading, refresh } = usePolling<AlertItem[]>(fetcher, 15_000, [tab]);

  const summary = usePolling(
    (signal) => api.alertsSummary(signal),
    15_000
  );

  const handleAck = async (id: string) => {
    setBusy((b) => ({ ...b, [id]: "ack" }));
    setError(null);
    try {
      await api.acknowledgeAlert(id);
      refresh();
      summary.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy((b) => ({ ...b, [id]: undefined }));
    }
  };

  const handleResolve = async (id: string) => {
    setBusy((b) => ({ ...b, [id]: "resolve" }));
    setError(null);
    try {
      await api.resolveAlert(id);
      refresh();
      summary.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy((b) => ({ ...b, [id]: undefined }));
    }
  };

  const counts = useMemo(
    () => ({
      open: summary.data?.open ?? 0,
      acknowledged: summary.data?.acknowledged ?? 0,
      critical: summary.data?.critical_open ?? 0,
    }),
    [summary.data]
  );

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          <Button asChild variant="ghost" size="sm">
            <Link href="/">
              <ArrowLeft className="h-3.5 w-3.5" />
              Dashboard
            </Link>
          </Button>
          <div>
            <h1 className="text-xl font-semibold">Alerts</h1>
            <p className="text-sm text-muted-foreground">
              Issues raised by the bridge that need attention
            </p>
          </div>
        </div>
        <div className="flex items-center gap-4 text-sm">
          <span>
            <span className="font-semibold text-red-600">{counts.critical}</span>{" "}
            <span className="text-muted-foreground">critical</span>
          </span>
          <span>
            <span className="font-semibold">{counts.open}</span>{" "}
            <span className="text-muted-foreground">open</span>
          </span>
          <span>
            <span className="font-semibold">{counts.acknowledged}</span>{" "}
            <span className="text-muted-foreground">acknowledged</span>
          </span>
          <Button asChild variant="outline" size="sm">
            <Link href="/notifications">
              <Bell className="h-3.5 w-3.5" />
              Channels
            </Link>
          </Button>
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        {error && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
              {error}
            </CardContent>
          </Card>
        )}

        <div className="flex gap-1 border-b">
          {(["open", "acknowledged", "resolved", "all"] as Tab[]).map((t) => (
            <button
              key={t}
              type="button"
              onClick={() => setTab(t)}
              className={`px-3 py-2 text-sm border-b-2 -mb-px ${
                tab === t
                  ? "border-foreground font-semibold"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
            >
              {t.charAt(0).toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>

        {loading && !data ? (
          <div className="flex flex-col gap-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 w-full" />
            ))}
          </div>
        ) : data && data.length > 0 ? (
          <ul className="space-y-2">
            {data.map((a) => {
              const Icon = SEVERITY_ICON[a.severity];
              const tone = SEVERITY_TONE[a.severity];
              return (
                <li key={a.id}>
                  <Card className={`border ${tone}`}>
                    <CardContent className="p-3 flex items-start gap-3">
                      <Icon className="h-4 w-4 mt-0.5 shrink-0" />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2 flex-wrap">
                          <Link
                            href={`/devices/${encodeURIComponent(a.device_id)}`}
                            className="font-medium hover:underline"
                          >
                            {a.device_name}
                          </Link>
                          <span className="text-[11px] uppercase tracking-wide text-muted-foreground/80">
                            {a.alert_key}
                          </span>
                          <span className="text-[11px] text-muted-foreground/70">
                            · {STATUS_LABEL[a.status]}
                          </span>
                          <span className="text-[11px] text-muted-foreground/70 ml-auto">
                            {formatRelative(a.opened_at)}
                          </span>
                        </div>
                        {a.message && (
                          <div className="mt-1 text-sm text-muted-foreground">
                            {a.message}
                          </div>
                        )}
                        {a.status !== "open" && (
                          <div className="mt-1 text-[11px] text-muted-foreground/70">
                            {a.acknowledged_at &&
                              `Acknowledged ${formatRelative(a.acknowledged_at)} by ${a.acknowledged_by || "—"}`}
                            {a.acknowledged_at && a.resolved_at && " · "}
                            {a.resolved_at &&
                              `Resolved ${formatRelative(a.resolved_at)} by ${a.resolved_by || "—"}`}
                          </div>
                        )}
                      </div>
                      {operator && (
                        <div className="flex gap-1 shrink-0">
                          {a.status === "open" && (
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() => handleAck(a.id)}
                              disabled={busy[a.id] !== undefined}
                            >
                              {busy[a.id] === "ack" ? (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              ) : (
                                <Check className="h-3.5 w-3.5" />
                              )}
                              Ack
                            </Button>
                          )}
                          {(a.status === "open" || a.status === "acknowledged") && (
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => handleResolve(a.id)}
                              disabled={busy[a.id] !== undefined}
                            >
                              {busy[a.id] === "resolve" ? (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              ) : (
                                <X className="h-3.5 w-3.5" />
                              )}
                              Resolve
                            </Button>
                          )}
                        </div>
                      )}
                    </CardContent>
                  </Card>
                </li>
              );
            })}
          </ul>
        ) : (
          <Card>
            <CardContent className="p-10 text-center space-y-2">
              <CheckCircle2 className="h-8 w-8 text-emerald-500 mx-auto" />
              <div className="text-sm text-muted-foreground">
                {tab === "open"
                  ? "No open alerts. Everything looks healthy."
                  : "Nothing to show here."}
              </div>
            </CardContent>
          </Card>
        )}
      </div>

      <div className="border-t bg-card/30 px-6 py-2 text-xs text-muted-foreground flex items-center gap-2">
        <Bell className="h-3 w-3" />
        Polls every 15 seconds. Open alerts auto-resolve when the bridge sends a
        recovery event.
      </div>
    </div>
  );
}
