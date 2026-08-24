"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  Building2,
  Loader2,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type { RoleMappingRow, RoleRow } from "@/lib/types";

// /sign-in-mappings — Entra group → role mappings (M3).
//
// Applied only on JIT create today (M3 v1): the first Entra sign-in for
// a new user reads the token's groups claim, matches against these rows,
// and grants the corresponding roles. Manual role assignments made
// afterwards are not overridden on subsequent sign-ins — see the callout
// at the top of the customer section.
//
// Two independent surfaces:
//   * Customer scope: uses this tenant's roles catalogue. Anyone with
//     role_mapping.manage sees this section.
//   * Vendor scope: legacy 3-role model (admin/operator/viewer). Only
//     visible to users with is_vendor=true.
//
// Vendor cross-tenant editing rides X-Customer-Scope + the vendor-bypass
// on the permission check, so no separate "acting as" chrome is needed
// here beyond the sidebar's global scope switcher.
export default function SignInMappingsPage() {
  const session = useSession();
  const canManage = hasPermission(session.user, "role_mapping.manage");
  const isVendor = !!session.user?.is_vendor;

  return (
    <div className="min-h-screen">
      <div className="max-w-6xl mx-auto p-6 space-y-6">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Sign-in mappings</h1>
            <p className="text-sm text-muted-foreground max-w-2xl">
              Map Entra security groups to roles so SSO users land with the
              right permissions on their first sign-in.
            </p>
          </div>
          <UserMenu />
        </header>

        {!canManage && !isVendor ? (
          <Card>
            <CardContent className="p-10 text-center text-sm text-muted-foreground">
              You don&rsquo;t have permission to manage sign-in mappings.
            </CardContent>
          </Card>
        ) : (
          <>
            {canManage && <CustomerSection />}
            {isVendor && <VendorSection />}
          </>
        )}
      </div>
    </div>
  );
}

// ------------------------------------------------------------
// Customer scope
// ------------------------------------------------------------

function CustomerSection() {
  const [mappings, setMappings] = useState<RoleMappingRow[] | null>(null);
  const [roles, setRoles] = useState<RoleRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<
    { mode: "create" } | { mode: "edit"; row: RoleMappingRow } | null
  >(null);
  const [deleting, setDeleting] = useState<RoleMappingRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [m, r] = await Promise.all([
        api.listRoleMappings(signal),
        api.listRoles(signal),
      ]);
      if (signal?.aborted) return;
      setMappings(m);
      setRoles(r);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  return (
    <Card>
      <CardContent className="p-0">
        <div className="p-4 flex items-start justify-between gap-3 border-b">
          <div>
            <div className="flex items-center gap-2 mb-1">
              <ShieldCheck className="h-4 w-4 text-muted-foreground" />
              <h2 className="font-medium">This tenant</h2>
            </div>
            <p className="text-xs text-muted-foreground max-w-xl">
              Each mapping grants a role to any signing-in user who is a
              member of the Entra group. Roles applied on first sign-in;
              manual promotions made later are not overridden on subsequent
              sign-ins.
            </p>
          </div>
          <Button size="sm" onClick={() => setEditing({ mode: "create" })}>
            <Plus className="h-3.5 w-3.5" />
            New mapping
          </Button>
        </div>

        {loadError && (
          <div className="p-4 text-sm [color:hsl(var(--destructive))] flex items-center gap-2">
            <AlertTriangle className="h-4 w-4" />
            {loadError}
          </div>
        )}

        {mappings === null && !loadError ? (
          <div className="p-4 space-y-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} className="h-11" />
            ))}
          </div>
        ) : (
          <MappingTable
            rows={mappings ?? []}
            onEdit={(row) => setEditing({ mode: "edit", row })}
            onDelete={setDeleting}
            emptyText="No mappings yet — click New mapping to add one."
            showRoleIdWarning
          />
        )}
      </CardContent>

      <Modal
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing?.mode === "edit" ? "Edit mapping" : "New mapping"}
        wide={false}
      >
        {editing && roles !== null && (
          <MappingForm
            initial={editing.mode === "edit" ? editing.row : undefined}
            roleOptions={roles.map((r) => r.name)}
            onCancel={() => setEditing(null)}
            onSubmit={async ({ group_id, role }) => {
              if (editing.mode === "edit") {
                await api.updateRoleMapping(editing.row.id, { role });
              } else {
                await api.createRoleMapping({ group_id, role });
              }
              setEditing(null);
              await load();
            }}
          />
        )}
      </Modal>

      <Modal
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title="Delete mapping"
        wide={false}
      >
        {deleting && (
          <DeleteConfirm
            groupID={deleting.group_id}
            role={deleting.role}
            onCancel={() => setDeleting(null)}
            onConfirm={async () => {
              await api.deleteRoleMapping(deleting.id);
              setDeleting(null);
              await load();
            }}
          />
        )}
      </Modal>
    </Card>
  );
}

// ------------------------------------------------------------
// Vendor scope
// ------------------------------------------------------------

const VENDOR_ROLES = ["admin", "operator", "viewer"] as const;

function VendorSection() {
  const [mappings, setMappings] = useState<RoleMappingRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<
    { mode: "create" } | { mode: "edit"; row: RoleMappingRow } | null
  >(null);
  const [deleting, setDeleting] = useState<RoleMappingRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const m = await api.listVendorRoleMappings(signal);
      if (signal?.aborted) return;
      setMappings(m);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  return (
    <Card>
      <CardContent className="p-0">
        <div className="p-4 flex items-start justify-between gap-3 border-b">
          <div>
            <div className="flex items-center gap-2 mb-1">
              <Building2 className="h-4 w-4 text-muted-foreground" />
              <h2 className="font-medium">Vendor tenant</h2>
            </div>
            <p className="text-xs text-muted-foreground max-w-xl">
              Helpdesk staff sign-in mappings. Uses the legacy three-role
              model (admin / operator / viewer) because the vendor tenant
              doesn&rsquo;t carry a per-tenant roles catalogue.
            </p>
          </div>
          <Button size="sm" onClick={() => setEditing({ mode: "create" })}>
            <Plus className="h-3.5 w-3.5" />
            New mapping
          </Button>
        </div>

        {loadError && (
          <div className="p-4 text-sm [color:hsl(var(--destructive))] flex items-center gap-2">
            <AlertTriangle className="h-4 w-4" />
            {loadError}
          </div>
        )}

        {mappings === null && !loadError ? (
          <div className="p-4 space-y-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} className="h-11" />
            ))}
          </div>
        ) : (
          <MappingTable
            rows={mappings ?? []}
            onEdit={(row) => setEditing({ mode: "edit", row })}
            onDelete={setDeleting}
            emptyText="No vendor mappings yet."
            showRoleIdWarning={false}
          />
        )}
      </CardContent>

      <Modal
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing?.mode === "edit" ? "Edit vendor mapping" : "New vendor mapping"}
        wide={false}
      >
        {editing && (
          <MappingForm
            initial={editing.mode === "edit" ? editing.row : undefined}
            roleOptions={[...VENDOR_ROLES]}
            onCancel={() => setEditing(null)}
            onSubmit={async ({ group_id, role }) => {
              if (editing.mode === "edit") {
                await api.updateVendorRoleMapping(editing.row.id, { role });
              } else {
                await api.createVendorRoleMapping({ group_id, role });
              }
              setEditing(null);
              await load();
            }}
          />
        )}
      </Modal>

      <Modal
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title="Delete mapping"
        wide={false}
      >
        {deleting && (
          <DeleteConfirm
            groupID={deleting.group_id}
            role={deleting.role}
            onCancel={() => setDeleting(null)}
            onConfirm={async () => {
              await api.deleteVendorRoleMapping(deleting.id);
              setDeleting(null);
              await load();
            }}
          />
        )}
      </Modal>
    </Card>
  );
}

// ------------------------------------------------------------
// Shared components
// ------------------------------------------------------------

interface MappingTableProps {
  rows: RoleMappingRow[];
  onEdit: (row: RoleMappingRow) => void;
  onDelete: (row: RoleMappingRow) => void;
  emptyText: string;
  // showRoleIdWarning renders the "unknown role" pill when role_id is
  // empty on a customer mapping (a role got renamed or deleted). Vendor
  // rows never have role_id, so the flag hides the pill entirely there.
  showRoleIdWarning: boolean;
}

function MappingTable({ rows, onEdit, onDelete, emptyText, showRoleIdWarning }: MappingTableProps) {
  if (rows.length === 0) {
    return (
      <div className="p-10 text-center text-sm text-muted-foreground">
        {emptyText}
      </div>
    );
  }
  return (
    <div className="divide-y">
      {rows.map((r) => {
        const orphan = showRoleIdWarning && !r.role_id;
        return (
          <div
            key={r.id}
            className="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_auto_auto] items-center gap-3 px-4 py-3 text-sm"
          >
            <div className="min-w-0">
              <div className="font-mono text-xs text-muted-foreground truncate">
                {r.group_id}
              </div>
              {r.created_at && (
                <div className="text-[11px] text-muted-foreground/70 mt-0.5">
                  Added {formatRelative(r.created_at)}
                </div>
              )}
            </div>
            <div className="flex items-center gap-2 min-w-0">
              <span className="rounded-md border bg-muted/40 px-2 py-0.5 text-xs font-medium truncate">
                {r.role}
              </span>
              {orphan && (
                <span
                  title="This role name no longer resolves to a role in your tenant. Rename this mapping or recreate the role."
                  className="rounded-md border border-destructive/40 bg-destructive/5 px-1.5 py-0.5 text-[10px] uppercase tracking-wider [color:hsl(var(--destructive))]"
                >
                  Unknown role
                </span>
              )}
            </div>
            <Button size="icon" variant="ghost" aria-label="Edit mapping" onClick={() => onEdit(r)}>
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <Button size="icon" variant="ghost" aria-label="Delete mapping" onClick={() => onDelete(r)}>
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        );
      })}
    </div>
  );
}

interface MappingFormProps {
  initial?: RoleMappingRow;
  roleOptions: string[];
  onCancel: () => void;
  onSubmit: (values: { group_id: string; role: string }) => Promise<void>;
}

function MappingForm({ initial, roleOptions, onCancel, onSubmit }: MappingFormProps) {
  const [groupID, setGroupID] = useState(initial?.group_id ?? "");
  const [role, setRole] = useState(initial?.role ?? roleOptions[0] ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isEdit = !!initial;
  const roleDropdown = useMemo(() => {
    // If we're editing a mapping whose role no longer exists in the list,
    // still surface it as an option so the admin can see what's there
    // before switching. Prevents the dropdown showing a blank selection.
    if (isEdit && role && !roleOptions.includes(role)) {
      return [role, ...roleOptions];
    }
    return roleOptions;
  }, [isEdit, role, roleOptions]);

  const onFormSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!groupID.trim() || !role.trim()) {
      setError("Group ID and role are both required.");
      return;
    }
    setSubmitting(true);
    try {
      await onSubmit({ group_id: groupID.trim(), role });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={onFormSubmit} className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <label htmlFor="group_id" className="text-xs font-medium text-muted-foreground">
          Entra group ID
        </label>
        <input
          id="group_id"
          type="text"
          value={groupID}
          onChange={(e) => setGroupID(e.target.value)}
          disabled={isEdit || submitting}
          placeholder="e.g. 3a9c1b2e-…"
          className="h-9 rounded-md border border-input bg-background px-3 text-sm font-mono outline-none focus:border-primary"
        />
        <p className="text-[11px] text-muted-foreground">
          Object ID of the security group in Entra (a GUID). Group ID is
          fixed after create — delete and recreate to change it.
        </p>
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="role" className="text-xs font-medium text-muted-foreground">
          Role
        </label>
        <select
          id="role"
          value={role}
          onChange={(e) => setRole(e.target.value)}
          disabled={submitting}
          className="h-9 rounded-md border border-input bg-background px-3 text-sm outline-none focus:border-primary"
        >
          {roleDropdown.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      </div>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Saving…
            </>
          ) : isEdit ? (
            "Save"
          ) : (
            "Create"
          )}
        </Button>
      </div>
    </form>
  );
}

interface DeleteConfirmProps {
  groupID: string;
  role: string;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}

function DeleteConfirm({ groupID, role, onCancel, onConfirm }: DeleteConfirmProps) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setError(null);
    setSubmitting(true);
    try {
      await onConfirm();
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p>
        Remove the mapping for group{" "}
        <span className="font-mono text-xs">{groupID}</span> to role{" "}
        <span className="font-medium">{role}</span>? Existing users who
        received this role from a previous sign-in keep it — this only
        affects future sign-ins.
      </p>
      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="button" onClick={run} disabled={submitting}>
          {submitting ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Deleting…
            </>
          ) : (
            "Delete"
          )}
        </Button>
      </div>
    </div>
  );
}
