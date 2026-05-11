import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Cloud, Gauge } from "lucide-react";
import { formatMetricValue, prettyMetricKey } from "@/lib/utils";

interface Props {
  metrics: Record<string, unknown> | null | undefined;
  lensMetrics?: Record<string, unknown> | null;
  error?: string;
}

export function TelemetryGrid({ metrics, lensMetrics, error }: Props) {
  const direct = metrics ? Object.entries(metrics) : [];
  const lens = lensMetrics ? Object.entries(lensMetrics) : [];

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <Gauge className="h-4 w-4 text-primary" />
          <CardTitle>Telemetry</CardTitle>
        </div>
      </CardHeader>
      <CardContent className="space-y-6">
        {error ? (
          <div className="text-sm text-destructive-foreground/90 [color:hsl(var(--destructive))]">
            {error}
          </div>
        ) : direct.length === 0 && lens.length === 0 ? (
          <div className="text-sm text-muted-foreground py-2">
            No metrics available.
          </div>
        ) : (
          <>
            <MetricsSection
              icon={<Gauge className="h-3.5 w-3.5" />}
              label="Direct from device"
              entries={direct}
              emptyHint="No direct metrics."
            />
            {lens.length > 0 && (
              <MetricsSection
                icon={<Cloud className="h-3.5 w-3.5" />}
                label="Poly Lens"
                entries={lens}
                emptyHint=""
              />
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function MetricsSection({
  icon,
  label,
  entries,
  emptyHint,
}: {
  icon: React.ReactNode;
  label: string;
  entries: [string, unknown][];
  emptyHint: string;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {icon}
        <span>{label}</span>
      </div>
      {entries.length === 0 ? (
        <div className="text-sm text-muted-foreground py-1">{emptyHint}</div>
      ) : (
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-3">
          {entries.map(([key, value]) => (
            <div
              key={key}
              className="flex items-baseline justify-between gap-3 border-b border-border/50 py-2"
            >
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">
                {prettyMetricKey(key)}
              </dt>
              <dd className="text-sm font-medium text-right break-all">
                {formatMetricValue(value)}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}
