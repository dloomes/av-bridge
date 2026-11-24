import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ExternalLink } from "lucide-react";
import type { DeviceDetail } from "@/lib/types";
import { isCloudSource } from "@/lib/types";
import { formatRelative, formatTimestamp } from "@/lib/utils";

type Metrics = Record<string, unknown> | null | undefined;

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
const bool = (v: unknown): boolean | undefined => (typeof v === "boolean" ? v : undefined);

const TAG_STATE: Record<string, string> = {
  pending: "Tag pending (Lens can take up to 10 minutes)",
  tagged: "Tagged in Lens",
  failed: "Tagging failed",
  none: "Not tagged",
};

// formatUptime: "3d 4h", "5h 12m", "8m" from seconds.
function formatUptime(v: unknown): string {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
  if (!Number.isFinite(n) || n < 0) return "";
  const d = Math.floor(n / 86400);
  const h = Math.floor((n % 86400) / 3600);
  const m = Math.floor((n % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

const yesNo = (v: boolean | undefined) => (v === undefined ? "unknown" : v ? "yes" : "no");

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
    <Badge variant={v ? "success" : "warning"} title="Teams sign-in state reported for the room">
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
    <Badge variant={good ? "success" : "warning"} title="Teams health reported for the room">
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

// connected is undefined when the state is unknown (a Neat pairing that
// matches no device in Pulse): never counted as missing.
type Linked = { name: string; model: string; connected: boolean | undefined; lastSeen: string };

// The linked devices Lens listed for the room (controller, camera, mini PC...).
function linkedDevices(v: unknown): Linked[] {
  if (!Array.isArray(v)) return [];
  return v
    .filter((x): x is Record<string, unknown> => !!x && typeof x === "object")
    .map((x) => ({ name: str(x.name), model: str(x.model), connected: bool(x.connected), lastSeen: str(x.last_seen) }));
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

const CALL_STATE: Record<string, string> = { in_call: "In a call", idle: "Not in a call", unknown: "Unknown" };

const CALL_PLATFORM: Record<string, string> = {
  zoom: "Zoom",
  teams: "Microsoft Teams",
  meet: "Google Meet",
  byod: "BYOD / open mode",
  other: "Other",
};

const UPGRADE_STATUS: Record<string, string> = {
  idle: "",
  downloading: "Downloading update",
  updating: "Updating",
  reboot_required: "Restart needed to finish the update",
  error: "Update failed",
  uninstalled: "Update removed",
};

const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const fmt = (v: number | undefined, digits: number, unit: string) =>
  v === undefined ? undefined : `${Number(v.toFixed(digits))}${unit}`;

// Sensors reported by a Neat device through Neat Pulse; nothing when it sent none.
function SensorsCard({ m }: { m: Record<string, unknown> }) {
  const shutter = bool(m.privacy_shutter_closed);
  const people = num(m.people_count);
  const rows: [string, string | undefined][] = [
    ["People", people === undefined ? undefined : String(people)],
    ["Temperature", fmt(num(m.temperature_c), 1, " °C")],
    ["Humidity", fmt(num(m.humidity_pct), 0, "%")],
    ["CO₂", fmt(num(m.co2_ppm), 0, " ppm")],
    ["VOC", fmt(num(m.voc), 0, "")],
    ["VOC index", fmt(num(m.voc_index), 0, "")],
    ["Light", fmt(num(m.illuminance_lux), 0, " lux")],
    ["Privacy shutter", shutter === undefined ? undefined : shutter ? "Closed" : "Open"],
  ];
  const shown = rows.filter(([, v]) => v !== undefined);
  if (shown.length === 0) return null;
  const at = str(m.sensor_at);
  return (
    <Panel title="Sensors">
      {shown.map(([label, v]) => (
        <Row key={label} label={label}>
          {v}
        </Row>
      ))}
      {at && (
        <Row label="Sample time">
          <span title={formatTimestamp(at)}>{formatRelative(at)}</span>
        </Row>
      )}
    </Panel>
  );
}

// The cards on a room system's page: a Teams Room managed by Poly Lens, a
// Teams or Zoom Room on a Neat device managed by Neat Pulse, or a Windows
// Teams Room a Collector reads directly over WinRM (source collector,
// protocol mtr_windows). Every value is optional: fields that are missing
// show as unknown, never as bad.
export function TeamsRoomCards({
  device,
  metrics,
  collectorName,
}: {
  device: DeviceDetail;
  metrics: Metrics;
  // Direct rooms: the name of the Collector that reads the room PC.
  collectorName?: string;
}) {
  const m = metrics ?? {};
  const direct = !isCloudSource(device.source);
  const neat = device.source === "neat_pulse";
  const platform = str(m.call_platform);
  const fwUpdate = str(m.firmware_update);
  const upgradeKey = str(m.firmware_upgrade_status);
  const upgrade = upgradeKey in UPGRADE_STATUS ? UPGRADE_STATUS[upgradeKey] : upgradeKey;
  const lastConnected = str(m.last_connected);
  const lastRestart = str(m.last_restart);
  const heartbeat = str(m.last_heartbeat);
  const lastError = str(m.last_error);
  const uptime = formatUptime(m.uptime_s);
  const callState = str(m.call_state) || "unknown";
  const https = device.tags?.winrm_https !== "false";
  const port = str(device.tags?.port) || (https ? "5986" : "5985");
  const peripherals = linkedDevices(m.peripherals);
  const missing = peripherals.filter((p) => p.connected === false).length;
  const linked = peripherals.length > 0 ? missing === 0 : bool(m.peripherals_linked);
  const lastSeen = str(m.lens_last_seen);
  const model = str(device.model);
  const serial = str(device.serial_number);
  const integ = device.integration;
  const tagState = integ?.tag_state ?? "";

  return (
    <div className="grid gap-6 md:grid-cols-2">
      {neat ? (
        <Panel title={device.type === "zoom_room" ? "Zoom Room" : "Teams Room"}>
          <Row label="Primary mode">{str(m.primary_mode) || dash}</Row>
          {str(m.pulse_room) && <Row label="Pulse room">{str(m.pulse_room)}</Row>}
          {bool(m.byod) && <Row label="BYOD">In use as a USB device</Row>}
        </Panel>
      ) : (
      <Panel title="Teams Rooms">
        <Row label="App version">{str(m.teams_app_version) || dash}</Row>
        {str(m.teams_presence) && <Row label="Presence">{str(m.teams_presence)}</Row>}
        {direct && (
          <>
            <Row label="Last heartbeat">
              {heartbeat ? <span title={formatTimestamp(heartbeat)}>{formatRelative(heartbeat)}</span> : dash}
            </Row>
            <Row label="Last error">
              {lastError ? <span className="[color:hsl(var(--warning))]">{lastError}</span> : <span className="text-muted-foreground">None in the last 15 minutes</span>}
            </Row>
          </>
        )}
      </Panel>
      )}
      {(direct || neat) && (
        <Card>
          <CardHeader>
            <CardTitle>Call</CardTitle>
          </CardHeader>
          <CardContent>
            <dl>
              <Row label="State">{CALL_STATE[callState] ?? callState}</Row>
              {neat && callState === "in_call" && <Row label="Platform">{CALL_PLATFORM[platform] ?? (platform || dash)}</Row>}
            </dl>
            {direct && (
              <p className="mt-2 text-xs text-muted-foreground">
                Mic/camera in use: {yesNo(bool(m.call_signal_mic))} · Teams media ports open: {yesNo(bool(m.call_signal_udp))}
              </p>
            )}
          </CardContent>
        </Card>
      )}
      {neat && <SensorsCard m={m} />}
      <Panel title="Peripherals">
        <Row label="Status">
          {linked === undefined ? (
            <span className="text-muted-foreground">Unknown</span>
          ) : linked ? (
            peripherals.length > 0 ? "All peripherals connected" : "All peripherals linked"
          ) : (
            <span className="[color:hsl(var(--warning))]">
              {peripherals.length > 0 ? `${missing} missing` : "Peripheral missing"}
            </span>
          )}
        </Row>
        {peripherals.map((p, i) => (
          <div key={i} className="flex items-start justify-between gap-3 border-b border-border/50 py-2 last:border-0">
            <div className="min-w-0">
              <div className="text-sm font-semibold break-words">{p.model || p.name || "Unknown device"}</div>
              {p.name && p.model && <div className="text-xs text-muted-foreground break-words">{p.name}</div>}
              <div className="text-xs text-muted-foreground">
                {p.lastSeen ? (
                  <span title={formatTimestamp(p.lastSeen)}>Last seen {formatRelative(p.lastSeen)}</span>
                ) : p.connected === undefined ? (
                  "State unknown"
                ) : (
                  "Not seen yet"
                )}
              </div>
            </div>
            <Badge
              variant={p.connected === undefined ? "secondary" : p.connected ? "success" : "warning"}
              className="shrink-0"
            >
              {p.connected === undefined ? "Unknown" : p.connected ? "Connected" : "Missing"}
            </Badge>
          </div>
        ))}
      </Panel>
      <Panel title="Device">
        <Row label="Model">{model || dash}</Row>
        <Row label={direct ? "Windows version" : neat ? "Firmware" : "Software"}>{str(m.software_version) || str(device.firmware_version) || dash}</Row>
        {neat && (
          <Row label="Firmware update">
            {upgrade ? (
              <span className="[color:hsl(var(--warning))]">{upgrade}</span>
            ) : fwUpdate ? (
              <span>
                {fwUpdate} available{bool(m.firmware_update_scheduled) ? " (scheduled)" : ""}
              </span>
            ) : str(m.software_version) ? (
              "Up to date"
            ) : (
              dash
            )}
          </Row>
        )}
        <Row label="Serial">{serial || dash}</Row>
        {direct ? (
          <Row label="Uptime">{uptime || dash}</Row>
        ) : neat ? (
          <>
            <Row label="Connected since">
              {lastConnected ? <span title={formatTimestamp(lastConnected)}>{formatRelative(lastConnected)}</span> : dash}
            </Row>
            <Row label="Last restart">
              {lastRestart ? <span title={formatTimestamp(lastRestart)}>{formatRelative(lastRestart)}</span> : dash}
            </Row>
          </>
        ) : (
          <Row label="Last seen by Lens">
            {lastSeen ? <span title={formatTimestamp(lastSeen)}>{formatRelative(lastSeen)}</span> : dash}
          </Row>
        )}
      </Panel>
      {direct ? (
        <Panel title="Source">
          <Row label="Managed by">Direct via Collector {collectorName || ""}</Row>
          <Row label="Connection">
            WinRM over {https ? "HTTPS" : "HTTP"} · port {port}
          </Row>
          <Row label="Address">{str(device.address) || dash}</Row>
        </Panel>
      ) : neat ? (
        <Panel title="Source">
          <Row label="Managed by">Neat Pulse{integ?.account_name ? ` (${integ.account_name})` : ""}</Row>
          <Row label="Access">Read only</Row>
          <Row label="Pulse">
            <a
              href="https://pulse.neat.no"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-primary hover:underline"
            >
              Open in Neat Pulse <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          </Row>
        </Panel>
      ) : (
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
      )}
    </div>
  );
}
