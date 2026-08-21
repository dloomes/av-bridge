"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import {
  ArrowRight,
  Building2,
  Palette,
  Pencil,
  Plus,
  Search,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { NewCustomerForm, EditCustomerForm } from "@/components/customer-forms";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { setScope } from "@/lib/session";
import { api, type HelpdeskOverviewItem } from "@/lib/api";

// /customers — vendor-only directory of every customer on the platform.
// Sibling to /helpdesk: helpdesk is the ops-first rollup (per-customer
// alerts, offline devices, bridge freshness), customers is the identity
// directory (name, slug, Entra tenant, quick actions). Same backend data,
// two lenses.
//
// Non-vendor sessions are bounced to / on hydrate. The sidebar only shows
// the item to vendors — this redirect is defence-in-depth for direct-URL
// navigation.
export default function CustomersPage() {
  const session = useSession();
  const router = useRouter();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<HelpdeskOverviewItem | null>(null);
  const [q, setQ] = useState("");

  const { data, loading, error, refresh } = usePolling<HelpdeskOverviewItem[]>(
    (signal) => api.helpdeskOverview(signal),
    60_000
  );

  useEffect(() => {
    if (!session.hydrated) return;
    if (session.user && !session.user.is_vendor) {
      router.replace("/");
    }
  }, [session.hydrated, session.user, router]);

  const filtered = useMemo(() => {
    if (!data) return [];
    const needle = q.trim().toLowerCase();
    if (needle === "") return data;
    return data.filter(
      (c) =>
        c.name.toLowerCase().includes(needle) ||
        (c.slug ?? "").toLowerCase().includes(needle) ||
        (c.entra_tenant_id ?? "").toLowerCase().includes(needle)
    );
  }, [data, q]);

  const actAs = (customerId: string) => {
    setScope(customerId);
    router.push("/");
  };

  // Edit-branding drops the vendor into the branding page scoped to this
  // customer, so the same page a customer admin sees for their own tenant
  // is what the vendor edits on the customer's behalf. The branding page
  // shows a banner + "back to customers" button while scope is set.
  const editBranding = (customerId: string) => {
    setScope(customerId);
    router.push("/settings/branding");
  };

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3">
          <div className="h-9 w-9 rounded-lg bg-primary/10 flex items-center justify-center">
            <Building2 className="h-4 w-4 text-primary" />
          </div>
          <div>
            <h1 className="text-xl font-semibold">Customers</h1>
            <p className="text-sm text-muted-foreground">
              {data ? `${data.length} tenants` : "Loading…"}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" />
            New customer
          </Button>
          <UserMenu />
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

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto max-w-6xl space-y-4 p-6">
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

          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <input
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Filter by name, slug or Entra tenant ID"
              className="h-9 w-full rounded-md border border-input bg-background pl-9 pr-3 text-sm"
            />
          </div>

          {loading && !data ? (
            <div className="space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14" />
              ))}
            </div>
          ) : filtered.length > 0 ? (
            <Card>
              <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,2fr)_minmax(0,2fr)_auto_auto_auto] items-center gap-3 border-b bg-muted/40 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                <div>Customer</div>
                <div>Slug</div>
                <div>Entra tenant</div>
                <div className="text-right pr-2">Devices</div>
                <div className="text-right pr-2">Collectors</div>
                <div className="w-[168px]" />
              </div>
              {filtered.map((c) => (
                <CustomerRow
                  key={c.id}
                  c={c}
                  onActAs={() => actAs(c.id)}
                  onEdit={() => setEditing(c)}
                  onEditBranding={() => editBranding(c.id)}
                />
              ))}
            </Card>
          ) : (
            <Card>
              <CardContent className="p-10 text-center text-sm text-muted-foreground">
                {q ? (
                  <>No customers match &ldquo;{q}&rdquo;.</>
                ) : (
                  <>No customers yet.</>
                )}
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}

// CustomerRow is a single table row in the directory. Kept dense on purpose
// so vendors scanning across a lot of tenants can absorb the list fast.
// Ops-shaped signals (alert counts, offline devices) live on /helpdesk;
// this row prioritises identity fields + a one-click "act as" jump.
function CustomerRow({
  c,
  onActAs,
  onEdit,
  onEditBranding,
}: {
  c: HelpdeskOverviewItem;
  onActAs: () => void;
  onEdit: () => void;
  onEditBranding: () => void;
}) {
  const slugHost = c.slug ? `${c.slug}.uat.involvecloud.com` : null;
  return (
    <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,2fr)_minmax(0,2fr)_auto_auto_auto] items-center gap-3 border-b px-4 py-3 text-sm last:border-b-0 hover:bg-muted/30">
      <div className="min-w-0">
        <div className="font-medium truncate">{c.name}</div>
      </div>
      <div className="min-w-0">
        {slugHost ? (
          <span className="font-mono text-xs text-muted-foreground truncate block">
            {slugHost}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground/60">—</span>
        )}
      </div>
      <div className="min-w-0">
        {c.entra_tenant_id ? (
          <span className="font-mono text-xs text-muted-foreground truncate block">
            {c.entra_tenant_id}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground/60">local auth</span>
        )}
      </div>
      <div className="text-right pr-2 tabular-nums">{c.devices_total}</div>
      <div className="text-right pr-2 tabular-nums">{c.collectors_total}</div>
      <div className="flex items-center gap-1 justify-end w-[168px]">
        <Button
          size="icon"
          variant="ghost"
          aria-label="Edit branding"
          title="Edit branding"
          onClick={onEditBranding}
        >
          <Palette className="h-3.5 w-3.5" />
        </Button>
        <Button
          size="icon"
          variant="ghost"
          aria-label="Edit customer"
          title="Edit name / slug"
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
  );
}
