import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Cloud, Fingerprint, Gauge, HeartPulse, ListOrdered } from "lucide-react";
import { formatMetricValue, prettyMetricKey } from "@/lib/utils";

interface Props {
  metrics: Record<string, unknown> | null | undefined;
  lensMetrics?: Record<string, unknown> | null;
  error?: string;
  // Keys shown elsewhere on the page (the Levels card), left out here.
  exclude?: Set<string>;
}

// Keys never shown on the device page. Older Collector builds still send
// the Tesira topology / AVB clock dumps (removed from the adapter
// 2026-09); hide them so the panel is tidy before those sites update.
const HIDDEN_KEYS = new Set(["discovered_servers", "discovered_server_count", "ptp_info"]);

// The device's own details are grouped: what it is, how it's doing, and
// everything else it reports. Each group keeps this order, whatever order
// the metrics arrive in (Postgres jsonb reorders keys); the rest follow
// alphabetically.
const IDENTITY = [
  "hostname",
  "part_number",
  "model",
  "serial_number",
  "software_version",
  "firmware_version",
  "ip_address",
  "mac_address",
];
const HEALTH = ["fault_count", "faults", "poll_error", "ttp_error", "last_poll"];

function orderedEntries(
  metrics: Record<string, unknown> | null | undefined,
  exclude?: Set<string>,
): [string, unknown][] {
  if (!metrics) return [];
  const order = [...IDENTITY, ...HEALTH];
  const rank = (k: string) => {
    const i = order.indexOf(k);
    return i === -1 ? order.length : i;
  };
  return Object.entries(metrics)
    .filter(([k]) => !HIDDEN_KEYS.has(k) && !exclude?.has(k))
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b));
}

const ISO_TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/;

// Units from the reading's name: voltage_v, total_power_w, outlet_1_current_a,
// response_ms. Returns undefined when the key has no unit suffix.
function withUnit(key: string, value: unknown): string | undefined {
  if (typeof value !== "number") return undefined;
  const round = (n: number, dp: number) => Number(n.toFixed(dp)).toLocaleString("en-GB");
  if (key.endsWith("_ms")) return value >= 1000 ? `${round(value / 1000, 1)} s` : `${round(value, 0)} ms`;
  if (key.endsWith("_v")) return `${round(value, 1)} V`;
  if (key.endsWith("_w")) return `${round(value, 1)} W`;
  if (key.endsWith("_a")) return `${round(value, 2)} A`;
  if (key.endsWith("_db")) return `${round(value, 1)} dB`;
  if (key.endsWith("_s")) return `${round(value, 0)} s`;
  return undefined;
}

// The label without its unit, once the value shows it: "Total Power", not
// "Total Power W".
function unitlessLabel(key: string): string {
  if (key === "response_ms") return "Response time";
  return prettyMetricKey(key.replace(/_(ms|v|w|a|db|s)$/, ""));
}

// Numbered readings ("outlet_3_power_w", "port_12_link") become a table:
// one row per outlet or port, one column per kind of reading.
const NUMBERED = /^(outlet|port)_(\d+)_(.+)$/;
const GROUP_LABEL: Record<string, string> = { outlet: "Outlets", port: "Ports" };
// Columns that lead, in this order; others follow alphabetically.
const COLUMN_ORDER = ["name", "state", "link", "status", "speed", "current_a", "power_w"];

interface NumberedGroup {
  prefix: string;
  columns: string[];
  rows: { n: number; values: Record<string, unknown> }[];
}

function numberedGroups(entries: [string, unknown][]): { groups: NumberedGroup[]; rest: [string, unknown][] } {
  const byPrefix = new Map<string, Map<number, Record<string, unknown>>>();
  const rest: [string, unknown][] = [];
  for (const [k, v] of entries) {
    const m = NUMBERED.exec(k);
    if (!m || isStructured(v)) {
      rest.push([k, v]);
      continue;
    }
    const rows = byPrefix.get(m[1]) ?? new Map<number, Record<string, unknown>>();
    const n = Number(m[2]);
    rows.set(n, { ...(rows.get(n) ?? {}), [m[3]]: v });
    byPrefix.set(m[1], rows);
  }
  const groups: NumberedGroup[] = [];
  for (const [prefix, rows] of byPrefix) {
    // A single numbered reading isn't worth a table.
    if (rows.size < 2) {
      for (const [n, values] of rows) {
        for (const [col, v] of Object.entries(values)) rest.push([`${prefix}_${n}_${col}`, v]);
      }
      continue;
    }
    const cols = new Set<string>();
    rows.forEach((values) => Object.keys(values).forEach((c) => cols.add(c)));
    const rank = (c: string) => {
      const i = COLUMN_ORDER.indexOf(c);
      return i === -1 ? COLUMN_ORDER.length : i;
    };
    groups.push({
      prefix,
      columns: [...cols].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b)),
      rows: [...rows.entries()].sort(([a], [b]) => a - b).map(([n, values]) => ({ n, values })),
    });
  }
  return { groups, rest };
}

function columnLabel(col: string): string {
  const unit = /_(v|w|a|ms|db)$/.exec(col)?.[1];
  const base = prettyMetricKey(unit ? col.slice(0, -(unit.length + 1)) : col);
  return unit ? `${base} (${unit === "db" ? "dB" : unit.toUpperCase()})` : base;
}

function NumberedTable({ group }: { group: NumberedGroup }) {
  const one = group.prefix === "port" ? "Port" : "Outlet";
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <ListOrdered className="h-3.5 w-3.5" />
        <span>{GROUP_LABEL[group.prefix] ?? prettyMetricKey(group.prefix)}</span>
      </div>
      <div className="relative overflow-x-auto rounded-md border">
        <table className="w-full text-sm">
          <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">{one}</th>
              {group.columns.map((c) => (
                <th key={c} scope="col" className="px-3 py-2 font-medium">{columnLabel(c)}</th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y">
            {group.rows.map((r) => (
              <tr key={r.n}>
                <td className="px-3 py-2 tabular-nums text-muted-foreground">{r.n}</td>
                {group.columns.map((c) => {
                  const v = r.values[c];
                  const num = typeof v === "number";
                  return (
                    <td key={c} className={num ? "px-3 py-2 tabular-nums" : "px-3 py-2"}>
                      {v === undefined || v === null || v === "" ? (
                        <span className="text-muted-foreground">—</span>
                      ) : c === "state" || c === "link" || c === "status" ? (
                        <StateText value={String(v)} />
                      ) : num ? (
                        Number((v as number).toFixed(2)).toLocaleString("en-GB")
                      ) : (
                        String(v)
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// On / off style states get a coloured dot as well as the word.
function StateText({ value }: { value: string }) {
  const v = value.toLowerCase();
  const on = v === "on" || v === "up" || v === "online" || v === "true";
  const off = v === "off" || v === "down" || v === "offline" || v === "false";
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        aria-hidden="true"
        className={
          on ? "h-2 w-2 rounded-full bg-success" : off ? "h-2 w-2 rounded-full bg-muted-foreground/40" : "h-2 w-2 rounded-full bg-warning"
        }
      />
      <span className="capitalize">{value}</span>
    </span>
  );
}

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

export function TelemetryGrid({ metrics, lensMetrics, error, exclude }: Props) {
  const direct = orderedEntries(metrics, exclude);
  const lens = orderedEntries(lensMetrics);
  const identity = direct.filter(([k]) => IDENTITY.includes(k));
  const health = direct.filter(([k]) => HEALTH.includes(k));
  const { groups, rest: other } = numberedGroups(
    direct.filter(([k]) => !IDENTITY.includes(k) && !HEALTH.includes(k)),
  );

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
            {identity.length > 0 && (
              <MetricsSection icon={<Fingerprint className="h-3.5 w-3.5" />} label="Identity" entries={identity} emptyHint="" />
            )}
            {health.length > 0 && (
              <MetricsSection icon={<HeartPulse className="h-3.5 w-3.5" />} label="Health" entries={health} emptyHint="" />
            )}
            {other.length > 0 && (
              <MetricsSection icon={<Gauge className="h-3.5 w-3.5" />} label="Readings" entries={other} emptyHint="" />
            )}
            {groups.map((g) => (
              <NumberedTable key={g.prefix} group={g} />
            ))}
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
                  {withUnit(key, value) !== undefined ? unitlessLabel(key) : prettyMetricKey(key)}
                </dt>
                <dd className="text-sm font-medium text-right break-all tabular-nums">
                  {withUnit(key, value) ?? formatScalar(value)}
                </dd>
              </div>
            )
          )}
        </dl>
      )}
    </div>
  );
}
