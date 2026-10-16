"use client";

import { useCallback, useEffect, useState } from "react";
import { Globe, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/modal";
import { api, type RemoteSession } from "@/lib/api";
import { formatRelative } from "@/lib/utils";

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

// The cloud answers errors as `{"error": "..."}`; ApiError carries
// "<status> <text>: <body>".
function errorText(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e);
  const i = msg.indexOf("{");
  if (i >= 0) {
    try {
      const body = JSON.parse(msg.slice(i));
      if (typeof body.error === "string") return body.error;
    } catch {}
  }
  return msg;
}

// RemoteAccessButton opens a device's web interface in a new tab, relayed
// through the cloud and the device's Collector — the browser doesn't need
// to be on the device's network.
export function RemoteAccessButton({
  deviceId,
  deviceName,
  defaultPath,
}: {
  deviceId: string;
  deviceName: string;
  // The device's web_path tag, used when Path is left empty.
  defaultPath?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <Globe className="h-3.5 w-3.5" />
        Web interface
      </Button>
      <Modal open={open} onClose={() => setOpen(false)} title={`Web interface — ${deviceName}`} wide={false}>
        <RemoteAccessPanel deviceId={deviceId} defaultPath={defaultPath} />
      </Modal>
    </>
  );
}

function RemoteAccessPanel({ deviceId, defaultPath }: { deviceId: string; defaultPath?: string }) {
  const [scheme, setScheme] = useState<"" | "http" | "https">("");
  const [port, setPort] = useState("");
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sessions, setSessions] = useState<RemoteSession[]>([]);

  const load = useCallback(() => {
    api
      .listRemoteSessions({ deviceId })
      .then((r) => setSessions(r.sessions))
      .catch(() => {});
  }, [deviceId]);
  useEffect(load, [load]);

  const portNum = port.trim() === "" ? undefined : Number(port);
  const portInvalid = portNum !== undefined && (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535);

  const openSession = async () => {
    setError(null);
    // Open the tab now, inside the click, so popup blockers allow it; it's
    // pointed at the session once the cloud has created it.
    const tab = window.open("", "_blank");
    if (tab) {
      tab.opener = null;
      tab.document.title = "Connecting…";
      tab.document.body.innerHTML =
        '<p style="font-family:system-ui,sans-serif;color:#475569;padding:24px">Connecting to the device…</p>';
    }
    setBusy(true);
    try {
      const s = await api.startRemoteSession(deviceId, {
        scheme: scheme || undefined,
        port: portNum,
        path: path.trim() || undefined,
      });
      if (tab) tab.location.href = s.url;
      else window.open(s.url, "_blank", "noopener");
      load();
    } catch (e) {
      tab?.close();
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const end = async (id: string) => {
    try {
      await api.endRemoteSession(id);
    } finally {
      load();
    }
  };

  return (
    <div className="space-y-5 text-sm">
      <p className="text-muted-foreground">
        Opens the device&apos;s own web page in a new tab, relayed through its Collector, so it works from
        anywhere. The session ends after 15 minutes without activity, or 8 hours at most, and is
        recorded in the audit log.
      </p>

      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-1.5">
          <label className={labelClass} htmlFor="ra-scheme">Protocol</label>
          <select
            id="ra-scheme"
            className={inputClass}
            value={scheme}
            onChange={(e) => setScheme(e.target.value as "" | "http" | "https")}
          >
            <option value="">Automatic</option>
            <option value="http">HTTP</option>
            <option value="https">HTTPS</option>
          </select>
        </div>
        <div className="space-y-1.5">
          <label className={labelClass} htmlFor="ra-port">Port</label>
          <input
            id="ra-port"
            className={inputClass}
            inputMode="numeric"
            placeholder="Automatic"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            aria-invalid={portInvalid}
          />
        </div>
      </div>
      <div className="space-y-1.5">
        <label className={labelClass} htmlFor="ra-path">Path</label>
        <input
          id="ra-path"
          className={inputClass}
          placeholder={defaultPath || "/"}
          value={path}
          onChange={(e) => setPath(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <p className="text-xs text-muted-foreground -mt-2">
        Automatic uses the device&apos;s address, or its <code>web_port</code> tag, else HTTP on port 80.
        Ports 80, 443, 8080 and 8443, the device&apos;s own port and its <code>web_port</code> tag are allowed.
        Path is the page to open first, such as <code>/login</code>; left empty, it&apos;s the
        device&apos;s <code>web_path</code> tag, else the home page.
      </p>

      {error && (
        <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={openSession} disabled={busy || portInvalid}>
          <Globe className="h-3.5 w-3.5" />
          {busy ? "Connecting…" : "Open in new tab"}
        </Button>
      </div>

      {sessions.length > 0 && (
        <div className="space-y-2 border-t pt-4">
          <div className={labelClass}>Open sessions</div>
          <ul className="divide-y rounded-md border">
            {sessions.map((s) => (
              <li key={s.id} className="flex items-center justify-between gap-3 px-3 py-2">
                <div className="min-w-0">
                  <div className="truncate">{s.actor}</div>
                  <div className="text-xs text-muted-foreground">
                    {s.scheme.toUpperCase()} :{s.port} · started {formatRelative(s.created_at)}
                    {s.launched_at ? ` · active ${formatRelative(s.last_activity_at)}` : " · not opened yet"}
                  </div>
                </div>
                <Button variant="outline" size="sm" onClick={() => end(s.id)}>
                  <X className="h-3.5 w-3.5" />
                  End
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
