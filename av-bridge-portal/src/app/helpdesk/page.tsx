"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
  AlertTriangle,
  ArrowRight,
  Bell,
  Building2,
  CircleCheck,
  CircleHelp,
  CircleSlash,
  Link as LinkIcon,
  FileText,
  Pencil,
  Plus,
  ServerCrash,
  Signpost,
  ShieldCheck,
  Wifi,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { NewCustomerForm, EditCustomerForm } from "@/components/customer-forms";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { setScope } from "@/lib/session";
import { api, type HelpdeskCustomer, type HelpdeskOverviewItem } from "@/lib/api";
import { OffboardDialog, OffboardingBanner } from "@/components/offboarding";
import { formatRelative } from "@/lib/utils";

// HelpdeskPage — vendor-only landing view summarising every customer the
// helpdesk operator can support. Non-vendor sessions get bounced; vendor
// users land here naturally from the user menu and can jump into any
// customer with one click (which sets the X-Customer-Scope cookie and
// pushes to the dashboard, scoped to that tenant).
export default function HelpdeskPage() {
  const session = useSession();
  const router = useRouter();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<HelpdeskOverviewItem | null>(null);
  const { data, loading, error, refresh } = usePolling<HelpdeskOverviewItem[]>(
    (signal) => api.helpdeskOverview(signal),
    30_000
  );
  const [offboarding, setOffboarding] = useState<HelpdeskCustomer | null>(null);
  // The overview doesn't carry offboarding dates; the customer list does.
  const customersPoll = usePolling<HelpdeskCustomer[]>(
    (signal) => api.helpdeskCustomers(signal),
    30_000
  );
  const byId = new Map((customersPoll.data ?? []).map((c) => [c.id, c]));
  const refreshAll = () => {
    refresh();
    customersPoll.refresh();
  };

  // Non-vendor sessions don't belong here — bounce them home. We wait for
  // hydration so we don't redirect on the first paint before session loads.
  useEffect(() => {
    if (!session.hydrated) return;
    if (session.user && !session.user.is_vendor) {
      router.replace("/");
    }
  }, [session.hydrated, session.user, router]);

  const actAs = (customerId: string) => {
    setScope(customerId);
    router.push("/");
  };

  const totalOpenAlerts = data?.reduce((n, c) => n + c.alerts_open, 0) ?? 0;
  const totalCritical = data?.reduce((n, c) => n + c.alerts_critical, 0) ?? 0;
  const totalDevices = data?.reduce((n, c) => n + c.devices_total, 0) ?? 0;

  return (
    <div className="flex h-app flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          <div className="h-9 w-9 rounded-lg bg-amber-500/10 ring-1 ring-amber-500/30 flex items-center justify-center">
            <ShieldCheck className="h-4 w-4 text-amber-600" />
          </div>
          <div>
            <h1 className="text-xl font-semibold">Helpdesk overview</h1>
            <p className="text-sm text-muted-foreground">
              All customers · auto-refreshing every 30s
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
          <span>
            <span className="font-semibold text-red-600">{totalCritical}</span>{" "}
            <span className="text-muted-foreground">critical</span>
          </span>
          <span>
            <span className="font-semibold">{totalOpenAlerts}</span>{" "}
            <span className="text-muted-foreground">open</span>
          </span>
          <span>
            <span className="font-semibold">{totalDevices}</span>{" "}
            <span className="text-muted-foreground">devices</span>
          </span>
          {session.user?.role === "admin" && (
            <Button asChild size="sm" variant="outline">
              <Link href="/data-requests?helpdesk=1">
                <ShieldCheck className="h-3.5 w-3.5" />
                Data requests
              </Link>
            </Button>
          )}
          {session.user?.roadmap_editor && (
            <Button asChild size="sm" variant="outline">
              <Link href="/helpdesk/roadmap">
                <Signpost className="h-3.5 w-3.5" />
                Roadmap
              </Link>
            </Button>
          )}
          <Button asChild size="sm" variant="outline">
            <Link href="/helpdesk/deletions">
              <FileText className="h-3.5 w-3.5" />
              Deletions
            </Link>
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" />
            New customer
          </Button>
        </div>
      </header>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="Create customer"
        wide={false}
      >
        <NewCustomerForm
          onCancel={() => setCreating(false)}
          onCreated={async () => {
            setCreating(false);
            refresh();
          }}
        />
      </Modal>

      <Modal
        open={editing !== null}
        onClose={() => setEditing(null)}
        title="Edit customer"
        wide={false}
      >
        {editing && (
          <EditCustomerForm
            customer={editing}
            onCancel={() => setEditing(null)}
            onSaved={async () => {
              setEditing(null);
              refresh();
            }}
          />
        )}
      </Modal>

      <OffboardDialog
        customer={offboarding}
        onClose={() => setOffboarding(null)}
        onDone={() => {
          setOffboarding(null);
          refreshAll();
        }}
      />

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        {error && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
              {error.message}{" "}
              <Button size="sm" variant="ghost" onClick={refresh}>
                Retry
              </Button>
            </CardContent>
          </Card>
        )}

        {loading && !data ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-44" />
            ))}
          </div>
        ) : data && data.length > 0 ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {data.map((c) => (
              <CustomerCard
                key={c.id}
                c={c}
                onActAs={() => actAs(c.id)}
                onEdit={() => setEditing(c)}
                offboardInfo={byId.get(c.id)}
                offboardLoaded={customersPoll.data !== undefined && customersPoll.data !== null}
                offboardError={customersPoll.error ? customersPoll.error.message : null}
                onRetryOffboard={customersPoll.refresh}
                onOffboard={() => setOffboarding(byId.get(c.id) ?? { id: c.id, name: c.name })}
                onChanged={refreshAll}
              />
            ))}
          </div>
        ) : (
          <Card>
            <CardContent className="p-10 text-center text-sm text-muted-foreground">
              No customers yet.
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

function CustomerCard({
  c,
  onActAs,
  onEdit,
  offboardInfo,
  offboardLoaded,
  offboardError,
  onRetryOffboard,
  onOffboard,
  onChanged,
}: {
  c: HelpdeskOverviewItem;
  onActAs: () => void;
  onEdit: () => void;
  offboardInfo?: HelpdeskCustomer;
  offboardLoaded: boolean;
  offboardError: string | null;
  onRetryOffboard: () => void;
  onOffboard: () => void;
  onChanged: () => void;
}) {
  const isOffboarding = !!offboardInfo?.offboarding_started_at;
  const hasCritical = c.alerts_critical > 0;
  const hasOffline = c.devices_offline > 0;
  const hasUnknown = c.devices_unknown > 0;
  const stale = c.last_bridge_seen
    ? Date.now() - new Date(c.last_bridge_seen).getTime() > 5 * 60_000
    : false;

  // Border accent reflects the worst signal so the helpdesk eye is drawn to
  // troubled tenants first. Unknown devices (collector offline) get the
  // same amber tint as offline devices — either way there's something
  // for the operator to look at. Green means genuinely all clear: devices
  // being monitored, a collector reporting, and nothing wrong. A tenant
  // with nothing monitored yet stays neutral rather than looking healthy.
  const hasOpenAlerts = c.alerts_open > 0;
  const monitored = c.devices_total > 0 && !!c.last_bridge_seen;
  const healthy =
    monitored && !hasCritical && !hasOffline && !hasUnknown && !hasOpenAlerts && !stale;
  const tone = hasCritical
    ? "border-red-500/40 bg-red-500/5"
    : hasOffline || hasUnknown || hasOpenAlerts || stale
    ? "border-amber-500/40 bg-amber-500/5"
    : healthy
    ? "border-green-500/40 bg-green-500/5"
    : "";

  return (
    <Card className={`border ${tone}`}>
      <CardContent className="p-4 space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 min-w-0">
              <div className="text-base font-semibold truncate">{c.name}</div>
              {healthy && (
                <span className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-green-600">
                  <CircleCheck aria-hidden="true" className="h-3.5 w-3.5" />
                  No issues
                </span>
              )}
            </div>
            {c.slug && (
              <div className="mt-0.5 flex items-center gap-1 text-[11px] text-muted-foreground truncate">
                <LinkIcon className="h-3 w-3 shrink-0" />
                <span className="truncate">{c.slug}.uat.involvecloud.com</span>
              </div>
            )}
            {c.entra_tenant_id && (
              <div className="text-[11px] text-muted-foreground truncate">
                {c.entra_tenant_id}
              </div>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <Button
              size="icon"
              variant="ghost"
              aria-label="Edit customer"
              onClick={onEdit}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <Button size="sm" onClick={onActAs}>
              Act as
              <ArrowRight className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>

        <div className="grid grid-cols-3 gap-2">
          <Stat label="Devices" value={c.devices_total} icon={Wifi} />
          <Stat
            label="Offline"
            value={c.devices_offline}
            icon={CircleSlash}
            tone={c.devices_offline > 0 ? "warn" : undefined}
          />
          <Stat
            label="Unknown"
            value={c.devices_unknown}
            icon={CircleHelp}
            tone={c.devices_unknown > 0 ? "warn" : undefined}
          />
          <Stat
            label="Critical"
            value={c.alerts_critical}
            icon={AlertTriangle}
            tone={c.alerts_critical > 0 ? "bad" : undefined}
          />
          <Stat
            label="Open alerts"
            value={c.alerts_open}
            icon={Bell}
            tone={c.alerts_open > 0 ? "warn" : undefined}
          />
          <Stat label="Collectors" value={c.collectors_total} icon={Building2} />
          <Stat
            label="Bridge"
            value={
              c.last_bridge_seen
                ? formatRelative(c.last_bridge_seen) ?? "—"
                : "never"
            }
            icon={ServerCrash}
            tone={stale ? "warn" : undefined}
            small
          />
        </div>

        {isOffboarding && offboardInfo ? (
          <OffboardingBanner customer={offboardInfo} onChanged={onChanged} />
        ) : !offboardLoaded ? (
          offboardError ? (
            <p className="text-xs text-muted-foreground">
              Couldn&apos;t check whether this customer is being offboarded.{" "}
              <Button size="sm" variant="ghost" onClick={onRetryOffboard}>
                Try again
              </Button>
            </p>
          ) : (
            <Skeleton className="h-8 w-32" />
          )
        ) : (
          <Button
            size="sm"
            variant="ghost"
            className="text-destructive"
            onClick={onOffboard}
          >
            Offboard customer
          </Button>
        )}
      </CardContent>
    </Card>
  );
}


function Stat({
  label,
  value,
  icon: Icon,
  tone,
  small,
}: {
  label: string;
  value: number | string;
  icon: React.ComponentType<{ className?: string }>;
  tone?: "warn" | "bad";
  small?: boolean;
}) {
  const toneClass =
    tone === "bad"
      ? "text-red-600"
      : tone === "warn"
      ? "text-amber-600"
      : "text-foreground";
  return (
    <div className="rounded-md border bg-card/50 p-2">
      <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
        <Icon className="h-3 w-3" />
        {label}
      </div>
      <div className={`mt-0.5 ${small ? "text-xs" : "text-lg font-semibold"} ${toneClass}`}>
        {value}
      </div>
    </div>
  );
}
