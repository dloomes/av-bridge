"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Loader2, Plug } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { LensConnectForm, apiMessage } from "@/components/integrations/lens-connect-form";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import type { Integration, IntegrationProvider } from "@/lib/types";
import { hasPermission } from "@/lib/session";
import { formatRelative } from "@/lib/utils";

interface Vendor {
  provider: IntegrationProvider;
  name: string;
  category: string;
  logo: string;
}

const VENDORS: Vendor[] = [
  { provider: "poly_lens", name: "Poly Lens", category: "Device management cloud", logo: "/integrations/poly.svg" },
  { provider: "logitech_sync", name: "Logitech Sync", category: "Device management cloud", logo: "/integrations/logitech.svg" },
  { provider: "microsoft_365", name: "Microsoft 365", category: "Teams Rooms presence, bookings, call quality", logo: "/integrations/microsoft.svg" },
  { provider: "servicenow", name: "ServiceNow", category: "Incidents from alerts", logo: "/integrations/servicenow.svg" },
];

export default function IntegrationsPage() {
  const session = useSession();
  const { toast } = useToast();
  const isVendor = !!session.user?.is_vendor;
  const canManage = isVendor || hasPermission(session.user, "integration.manage");
  const [items, setItems] = useState<Integration[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [disconnectError, setDisconnectError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listIntegrations(signal);
      if (!signal?.aborted) {
        setItems(list);
        setLoadError(null);
      }
    } catch (e) {
      if (!signal?.aborted) setLoadError(apiMessage(e));
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const byProvider = (p: IntegrationProvider) => items?.find((i) => i.provider === p) ?? null;
  const lens = byProvider("poly_lens");
  const lensConnected = lens?.status === "connected" || lens?.status === "error";

  function closeEditor() {
    setEditing(false);
    setDirty(false);
  }

  return (
    <div className="min-h-app">
      <div className="max-w-4xl mx-auto p-6 space-y-4">
        <header className="flex items-center gap-3">
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
            <Plug aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div>
            <h1 className="text-2xl font-semibold leading-tight">Integrations</h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Connect the systems that manage your rooms. Credentials are stored encrypted and never shown again.
            </p>
          </div>
        </header>

        {loadError && (
          <p role="alert" className="text-sm text-destructive">
            Could not load integrations: {loadError}
          </p>
        )}

        {items === null && !loadError ? (
          <div className="grid gap-3 sm:grid-cols-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-36 w-full" />
            ))}
          </div>
        ) : (
          items && (
            <div className="grid gap-3 sm:grid-cols-2">
              {VENDORS.map((v) => (
                <IntegrationCard
                  key={v.provider}
                  vendor={v}
                  integration={byProvider(v.provider)}
                  canManage={canManage}
                  onManage={() => setEditing(true)}
                />
              ))}
            </div>
          )
        )}
      </div>

      <Modal
        open={editing}
        onClose={closeEditor}
        title={lensConnected ? "Poly Lens" : "Connect Poly Lens"}
        wide={false}
        dirty={dirty}
      >
        {editing && (
          <LensConnectForm
            integration={lens}
            isVendor={isVendor}
            onCancel={closeEditor}
            onDirty={setDirty}
            onSaved={() => {
              closeEditor();
              toast({ title: "Poly Lens saved", variant: "success" });
              void load();
            }}
            onDisconnect={() => {
              setDisconnectError(null);
              setConfirmingDisconnect(true);
            }}
          />
        )}
      </Modal>

      <Modal
        open={confirmingDisconnect}
        onClose={() => setConfirmingDisconnect(false)}
        title="Disconnect Poly Lens?"
        wide={false}
      >
        <div className="space-y-4">
          <p className="text-sm">
            M.A.R.C.U.S. will stop reading from Poly Lens and delete the stored credentials. Devices linked from Poly
            Lens stay in M.A.R.C.U.S., but their status becomes unknown until you connect again.
          </p>
          {disconnectError && (
            <p role="alert" className="text-sm text-destructive">
              {disconnectError}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setConfirmingDisconnect(false)} disabled={disconnecting}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={disconnecting}
              onClick={async () => {
                setDisconnecting(true);
                setDisconnectError(null);
                try {
                  await api.disconnectLens();
                  setConfirmingDisconnect(false);
                  closeEditor();
                  toast({ title: "Poly Lens disconnected", variant: "success" });
                  void load();
                } catch (e) {
                  setDisconnectError(apiMessage(e));
                } finally {
                  setDisconnecting(false);
                }
              }}
            >
              {disconnecting && <Loader2 className="h-4 w-4 animate-spin" />}
              Disconnect
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}

function IntegrationCard({
  vendor,
  integration,
  canManage,
  onManage,
}: {
  vendor: Vendor;
  integration: Integration | null;
  canManage: boolean;
  onManage: () => void;
}) {
  const status = integration?.status ?? "not_connected";
  const isLens = vendor.provider === "poly_lens";
  const isServiceNow = vendor.provider === "servicenow";
  const connected = status === "connected";
  const active = connected || status === "error";

  let pill: React.ReactNode;
  if (status === "coming_soon") {
    pill = <Badge variant="secondary">Coming soon</Badge>;
  } else if (connected) {
    pill = <Badge variant="success">Connected</Badge>;
  } else if (status === "error") {
    pill = <Badge variant="warning">Needs attention</Badge>;
  } else {
    pill = <Badge variant="secondary">Not connected</Badge>;
  }

  let summary: string | null = null;
  if (isLens && active) {
    const n = integration?.linked_devices ?? 0;
    summary = `${n} ${n === 1 ? "device" : "devices"} linked`;
    if (integration?.last_success_at) summary += ` · checked ${formatRelative(integration.last_success_at)}`;
    if (integration?.account_name) summary = `${integration.account_name} · ${summary}`;
  } else if (isServiceNow) {
    summary = connected ? "via Alert channels" : "Set up under Alert channels";
  }

  return (
    <Card>
      <CardContent className="p-4 space-y-3">
        <div className="flex items-center gap-3">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={vendor.logo}
            alt=""
            width={36}
            height={36}
            className="h-9 w-9 rounded-md bg-white p-1 object-contain ring-1 ring-border"
          />
          <div className="min-w-0">
            <div className="font-semibold leading-tight">{vendor.name}</div>
            <div className="text-xs text-muted-foreground leading-tight">{vendor.category}</div>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          {pill}
          {summary && <span className="text-muted-foreground">{summary}</span>}
        </div>
        {status === "error" && integration?.last_error && (
          <p className="text-xs break-words [color:hsl(var(--warning))]">{integration.last_error}</p>
        )}
        {isLens && canManage && (
          <Button size="sm" variant={active ? "outline" : "default"} onClick={onManage}>
            {active ? "Manage" : "Connect"}
          </Button>
        )}
        {isServiceNow && (
          <Link href="/notifications" className="inline-block text-sm font-medium text-primary hover:underline">
            Alert channels →
          </Link>
        )}
      </CardContent>
    </Card>
  );
}
