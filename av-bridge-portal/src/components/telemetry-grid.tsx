import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Cloud, Gauge } from "lucide-react";
import { formatMetricValue, prettyMetricKey } from "@/lib/utils";

interface Props {
  metrics: Record<string, unknown> | null | undefined;
  lensMetrics?: Record<string, unknown> | null;
  error?: string;
}

// Keys never shown on the device page. Older Collector builds still send
// the Tesira topology / AVB clock dumps (removed from the adapter
// 2026-09); hide them so the panel is tidy before those sites update.
const HIDDEN_KEYS = new Set(["discovered_servers", "discovered_server_count", "ptp_info"]);

// Identity first, in this order, whatever order the metrics arrive in
// (Postgres jsonb reorders keys). Everything else follows alphabetically.
const PREFERRED_ORDER = [
  "hostname",
  "part_number",
  "model",
  "serial_number",
  "software_version",
  "firmware_version",
  "ip_address",
  "mac_address",
  "fault_count",
  "faults",
  "last_poll",
];

function orderedEntries(metrics: Record<string, unknown> | null | undefined): [string, unknown][] {
  if (!metrics) return [];
  const rank = (k: string) => {
    const i = PREFERRED_ORDER.indexOf(k);
    return i === -1 ? PREFERRED_ORDER.length : i;
  };
  return Object.entries(metrics)
    .filter(([k]) => !HIDDEN_KEYS.has(k))
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b));
}

const ISO_TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/;

function formatScalar(value: unknown): string {
  if (typeof value === "string" && ISO_TIMESTAMP.test(value)) {
    const d = new Date(value);
    if (!Number.isNaN(d.getTime())) {
      return d.toLocaleString("en-GB", { dateStyle: "medium", timeStyle: "medium" });
    }
  }
  return formatMetricValue(value);
}

function isStructured(value: unknown): value is object {
  return typeof value === "object" && value !== null;
}

function structuredSummary(value: object): string {
  if (Array.isArray(value)) {
    return value.length === 1 ? "1 item" : `${value.length} items`;
  }
  const n = Object.keys(value).length;
  return n === 1 ? "1 field" : `${n} fields`;
}

export function TelemetryGrid({ metrics, lensMetrics, error }: Props) {
  const direct = orderedEntries(metrics);
  const lens = orderedEntries(lensMetrics);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <Gauge className="h-4 w-4 text-primary" />
          <CardTitle>Device Information</CardTitle>
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
          {entries.map(([key, value]) =>
            isStructured(value) ? (
              // Lists and objects (e.g. an active fault list) collapse to a
              // one-line summary and expand across the full width on click,
              // instead of a raw JSON blob squeezed into a value cell.
              <div key={key} className="sm:col-span-2 border-b border-border/50 py-2">
                <details>
                  <summary className="flex cursor-pointer items-baseline justify-between gap-3 list-none [&::-webkit-details-marker]:hidden">
                    <span className="text-xs uppercase tracking-wide text-muted-foreground">
                      {prettyMetricKey(key)}
                    </span>
                    <span className="text-sm font-medium text-primary">
                      {structuredSummary(value)} ▾
                    </span>
                  </summary>
                  <pre className="mt-2 max-h-64 overflow-auto rounded bg-muted/50 p-2 text-[11px] text-foreground/80 whitespace-pre-wrap break-all">
                    {JSON.stringify(value, null, 2)}
                  </pre>
                </details>
              </div>
            ) : (
              <div
                key={key}
                className="flex items-baseline justify-between gap-3 border-b border-border/50 py-2"
              >
                <dt className="text-xs uppercase tracking-wide text-muted-foreground">
                  {prettyMetricKey(key)}
                </dt>
                <dd className="text-sm font-medium text-right break-all">
                  {formatScalar(value)}
                </dd>
              </div>
            )
          )}
        </dl>
      )}
    </div>
  );
}
