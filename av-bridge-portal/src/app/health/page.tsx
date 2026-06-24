"use client";

import { HeartPulse, RefreshCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Skeleton } from "@/components/ui/skeleton";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { UserMenu } from "@/components/user-menu";
import { usePolling } from "@/hooks/usePolling";
import { api, API_BASE } from "@/lib/api";

const UPSTREAM_LABEL = API_BASE || "av-bridge (proxied via Next.js)";
import type { FleetStatus, HealthResponse } from "@/lib/types";

export default function HealthPage() {
  const health = usePolling<HealthResponse>(
    (signal) => api.health(signal),
    10_000
  );
  const status = usePolling<FleetStatus>(
    (signal) => api.fleetStatus(signal),
    10_000
  );
  const metrics = usePolling<string>(
    (signal) => api.metrics(signal),
    15_000
  );

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center text-primary">
            <HeartPulse className="h-4 w-4" />
          </div>
          <div>
            <h1 className="text-xl font-semibold">Health</h1>
            <p className="text-sm text-muted-foreground">
              Raw output from {UPSTREAM_LABEL}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              health.refresh();
              status.refresh();
              metrics.refresh();
            }}
          >
            <RefreshCcw className="h-3.5 w-3.5" />
            Refresh
          </Button>
          <ConnectionIndicator />
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="grid gap-6 p-6 lg:grid-cols-2">
          <JsonCard
            title="GET /healthz"
            data={health.data}
            error={health.error?.message}
            loading={health.loading && !health.data}
          />
          <JsonCard
            title="GET /api/v1/status"
            data={status.data}
            error={status.error?.message}
            loading={status.loading && !status.data}
          />
          <Card className="lg:col-span-2 flex flex-col h-[60vh] min-h-[360px]">
            <CardHeader>
              <CardTitle>GET /metrics</CardTitle>
            </CardHeader>
            <CardContent className="flex-1 min-h-0 p-0">
              {metrics.loading && !metrics.data ? (
                <div className="px-5 pb-5">
                  <Skeleton className="h-full w-full" />
                </div>
              ) : metrics.error ? (
                <div className="px-5 pb-5 text-sm [color:hsl(var(--destructive))]">
                  {metrics.error.message}
                </div>
              ) : (
                <ScrollArea className="h-full px-5 pb-5 scrollbar-thin">
                  <pre className="text-[11px] leading-relaxed font-mono whitespace-pre-wrap break-all text-foreground/80">
                    {metrics.data}
                  </pre>
                </ScrollArea>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

interface JsonCardProps {
  title: string;
  data: unknown;
  error?: string;
  loading: boolean;
}

function JsonCard({ title, data, error, loading }: JsonCardProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {loading ? (
          <Skeleton className="h-24" />
        ) : error ? (
          <div className="text-sm [color:hsl(var(--destructive))]">{error}</div>
        ) : (
          <pre className="text-xs font-mono bg-muted/40 rounded-md p-3 overflow-x-auto whitespace-pre-wrap">
            {JSON.stringify(data, null, 2)}
          </pre>
        )}
      </CardContent>
    </Card>
  );
}
