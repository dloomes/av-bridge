import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ExternalLink } from "lucide-react";
import type { DeviceDetail } from "@/lib/types";
import { formatRelative, formatTimestamp } from "@/lib/utils";

type Metrics = Record<string, unknown> | null | undefined;

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
const bool = (v: unknown): boolean | undefined => (typeof v === "boolean" ? v : undefined);

const TAG_STATE: Record<string, string> = {
  tagged: "Tagged in Lens",
  failed: "Tagging failed",
  none: "Not tagged",
};

// The reason a Lens device is unknown ("Poly Lens unreachable", ...), from
// the reading the poller writes.
export function lensReason(metrics: Metrics): string {
  return str(metrics?.error);
}

// "Teams: signed in" / "Teams: signed out", or nothing when Lens didn't say.
export function TeamsSignedInPill({ metrics, online }: { metrics: Metrics; online: boolean }) {
  const v = bool(metrics?.teams_signed_in);
  if (!online || v === undefined) return null;
  return (
    <Badge variant={v ? "success" : "warning"} title="Teams sign-in state reported by Poly Lens">
      {v ? "Teams: signed in" : "Teams: signed out"}
    </Badge>
  );
}

// "Teams health: Healthy" and so on; nothing when Lens didn't say.
export function TeamsHealthPill({ metrics, online }: { metrics: Metrics; online: boolean }) {
  const h = str(metrics?.teams_health);
  if (!online || !h) return null;
  const good = /^(healthy|ok|good)$/i.test(h);
  return (
    <Badge variant={good ? "success" : "warning"} title="Teams health reported by Poly Lens">
      Teams health: {h}
    </Badge>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-border/50 py-2 last:border-0">
      <dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="text-sm font-medium text-right break-words min-w-0">{children}</dd>
    </div>
  );
}

const dash = <span className="text-muted-foreground">—</span>;

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <dl>{children}</dl>
      </CardContent>
    </Card>
  );
}

// The four cards on a Poly Lens-managed Teams Room's page. Every value is
// optional: Lens fields that are missing show as unknown, never as bad.
export function TeamsRoomCards({ device, metrics }: { device: DeviceDetail; metrics: Metrics }) {
  const m = metrics ?? {};
  const linked = bool(m.peripherals_linked);
  const lastSeen = str(m.lens_last_seen);
  const model = str(m.model) || str(m.hardware_model);
  const serial = str(m.serial_number) || str(m.serial);
  const integ = device.integration;
  const tagState = integ?.tag_state ?? "";

  return (
    <div className="grid gap-6 md:grid-cols-2">
      <Panel title="Teams Rooms">
        <Row label="App version">{str(m.teams_app_version) || dash}</Row>
        <Row label="Presence">{str(m.teams_presence) || dash}</Row>
      </Panel>
      <Panel title="Peripherals">
        <Row label="Status">
          {linked === undefined ? (
            <span className="text-muted-foreground">Unknown</span>
          ) : linked ? (
            "All peripherals linked"
          ) : (
            <span className="[color:hsl(var(--warning))]">Peripheral missing</span>
          )}
        </Row>
      </Panel>
      <Panel title="Device">
        <Row label="Model">{model || dash}</Row>
        <Row label="Software">{str(m.software_version) || dash}</Row>
        <Row label="Serial">{serial || dash}</Row>
        <Row label="Last seen by Lens">
          {lastSeen ? <span title={formatTimestamp(lastSeen)}>{formatRelative(lastSeen)}</span> : dash}
        </Row>
      </Panel>
      <Panel title="Source">
        <Row label="Managed by">Poly Lens{integ?.account_name ? ` (${integ.account_name})` : ""}</Row>
        {integ?.external_name && <Row label="Lens name">{integ.external_name}</Row>}
        <Row label="Tag">{TAG_STATE[tagState] ?? (tagState || dash)}</Row>
        <Row label="Lens">
          <a
            href="https://lens.poly.com"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-primary hover:underline"
          >
            Open in Lens <ExternalLink className="h-3 w-3" aria-hidden="true" />
          </a>
        </Row>
      </Panel>
    </div>
  );
}
