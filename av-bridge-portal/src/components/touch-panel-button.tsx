"use client";

import { useState } from "react";
import { ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { errorText } from "@/components/remote-access";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { hasPermission } from "@/lib/session";

type PanelDevice = {
  id: string;
  protocol: string;
  address?: string;
  tags?: Record<string, string>;
};

// TouchPanelButton opens a touch panel's built-in web UI. With the Remote
// access permission it starts a remote session (relayed through the cloud
// and the device's Collector, so it works from anywhere); without it, it
// falls back to a direct link to the panel on the LAN.
//
// Shown for Aurora RXT panels and for any device with a touch_panel_path
// tag; the path is that tag, else "/user".
export function TouchPanelButton({
  device,
  className,
  iconClassName = "h-3.5 w-3.5",
}: {
  device: PanelDevice;
  className?: string;
  iconClassName?: string;
}) {
  const session = useSession();
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);

  const tagPath = device.tags?.touch_panel_path;
  if (device.protocol !== "aurora_rxt" && !tagPath) return null;
  const path = tagPath || "/user";

  if (hasPermission(session.user, "device.remote_access")) {
    const open = async () => {
      // Open the tab now, inside the click, so popup blockers allow it; it's
      // pointed at the session once the cloud has created it.
      const tab = window.open("", "_blank");
      if (tab) {
        tab.opener = null;
        tab.document.title = "Connecting…";
        tab.document.body.innerHTML =
          '<p style="font-family:system-ui,sans-serif;color:#475569;padding:24px">Connecting to the touch panel…</p>';
      }
      setBusy(true);
      try {
        const s = await api.startRemoteSession(device.id, { scheme: "https", path });
        if (tab) tab.location.href = s.url;
        else window.open(s.url, "_blank", "noopener");
      } catch (e) {
        tab?.close();
        toast({ title: "Couldn't open the touch panel", description: errorText(e), variant: "destructive" });
      } finally {
        setBusy(false);
      }
    };
    return (
      <Button variant="outline" size="sm" className={className} onClick={open} disabled={busy}>
        Touch Panel
        <ExternalLink className={iconClassName} />
      </Button>
    );
  }

  const panelHost = device.tags?.ip_address ?? device.address?.split(":")[0];
  if (!panelHost) return null;
  return (
    <Button asChild variant="outline" size="sm" className={className}>
      <a href={`https://${panelHost}${path.startsWith("/") ? path : `/${path}`}`} target="_blank" rel="noopener noreferrer">
        Touch Panel
        <ExternalLink className={iconClassName} />
      </a>
    </Button>
  );
}
