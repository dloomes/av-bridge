"use client";

import { useCallback, useEffect, useState } from "react";
import {
  AlertTriangle,
  Loader2,
  Lock,
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
import {
  PERMISSIONS_BY_CATEGORY,
  KNOWN_PERMISSION_KEYS,
} from "@/lib/permissions";
import { isAdmin } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type { CreateRoleBody, RoleRow, UpdateRoleBody } from "@/lib/types";

// RolesPage — per-tenant role catalogue with a permission-matrix editor.
//
// Reads: any authed user in a tenant sees the catalogue. Admin actions
// (New role, edit, delete) are hidden for non-admins, and the backend
// re-enforces via wrapAdmin at the route.
//
// System-default roles (admin/operator/viewer) show a Lock badge and
// render read-only — they still open in the modal so you can see what
// permissions the built-in bundles grant, but every checkbox is disabled
// and the Save button is hidden.
//
// The banner up top tells the user why creating custom roles doesn't
// immediately grant access — the permission engine that consumes
// role_permissions ships in a follow-up slice.
export default function RolesPage() {
  const session = useSession();
  const admin = isAdmin(session.user?.role);
  const [roles, setRoles] = useState<RoleRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ mode: "create" | "edit" | "view"; existing?: RoleRow } | null>(null);
  const [deleting, setDeleting] = useState<RoleRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listRoles(signal);
      if (signal?.aborted) return;
      setRoles(list);
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
    <div className="min-h-screen">
      <div className="max-w-6xl mx-auto p-6 space-y-4">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Roles</h1>
            <p className="text-sm text-muted-foreground">
              Define the roles available to users in this tenant.
            </p>
          </div>
          <div className="flex items-center gap-2">
            {admin && (
              <Button onClick={() => setEditing({ mode: "create" })}>
                <Plus className="h-4 w-4" />
                New role
              </Button>
            )}
            <UserMenu />
          </div>
        </header>

        {loadError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {loadError}
          </div>
        )}

        {roles === null ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 w-full" />
            ))}
          </div>
        ) : (
          <Card>
            <CardContent className="p-0">
              <div className="divide-y">
                {roles.map((r) => (
                  <RoleRowView
                    key={r.id}
                    role={r}
                    admin={admin}
                    onView={() => setEditing({ mode: "view", existing: r })}
                    onEdit={() => setEditing({ mode: "edit", existing: r })}
                    onDelete={() => setDeleting(r)}
                  />
                ))}
              </div>
            </CardContent>
          </Card>
        )}

        <Modal
          open={editing !== null}
          onClose={() => setEditing(null)}
          title={
            editing?.mode === "view"
              ? `Role: ${editing.existing?.name ?? ""}`
              : editing?.mode === "edit"
              ? `Edit role: ${editing.existing?.name ?? ""}`
              : "New role"
          }
        >
          {editing && (
            <RoleForm
              mode={editing.mode}
              existing={editing.existing}
              onCancel={() => setEditing(null)}
              onSaved={async () => {
                setEditing(null);
                await load();
              }}
            />
          )}
        </Modal>

        <Modal
          open={deleting !== null}
          onClose={() => setDeleting(null)}
          title={`Delete role ${deleting?.name ?? ""}`}
          wide={false}
        >
          {deleting && (
            <DeleteRoleConfirm
              role={deleting}
              onCancel={() => setDeleting(null)}
              onDone={async () => {
                setDeleting(null);
                await load();
              }}
            />
          )}
        </Modal>
      </div>
    </div>
  );
}

function RoleRowView({
  role,
  admin,
  onView,
  onEdit,
  onDelete,
}: {
  role: RoleRow;
  admin: boolean;
  onView: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const canEdit = admin && !role.is_system_default;
  const canDelete = admin && !role.is_system_default && role.assigned_users === 0;

  return (
    <div className="flex items-center gap-3 p-3">
      <div
        className={`h-9 w-9 rounded-md flex items-center justify-center shrink-0 ${
          role.is_system_default
            ? "bg-amber-500/10 ring-1 ring-amber-500/30"
            : "bg-primary/10 ring-1 ring-primary/30"
        }`}
      >
        {role.is_system_default ? (
          <Lock className="h-4 w-4 text-amber-600" />
        ) : (
          <ShieldCheck className="h-4 w-4 text-primary" />
        )}
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium flex items-center gap-2">
          <span>{role.name}</span>
          {role.is_system_default && (
            <span className="text-[10px] uppercase tracking-wide bg-amber-500/10 text-amber-700 dark:text-amber-400 px-1.5 py-0.5 rounded">
              system default
            </span>
          )}
        </div>
        {role.description && (
          <div className="text-xs text-muted-foreground truncate">{role.description}</div>
        )}
      </div>
      <div className="hidden md:block w-32 text-right text-xs text-muted-foreground">
        {role.permissions.length} permission{role.permissions.length === 1 ? "" : "s"}
      </div>
      <div className="hidden md:block w-32 text-right text-xs text-muted-foreground">
        {role.assigned_users} user{role.assigned_users === 1 ? "" : "s"}
      </div>
      <div className="hidden lg:block w-32 text-right text-xs text-muted-foreground">
        {role.created_at ? formatRelative(role.created_at) : ""}
      </div>
      <div className="flex items-center gap-1">
        <Button variant="ghost" size="icon" aria-label="View / edit" onClick={canEdit ? onEdit : onView}>
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        {admin && (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Delete role"
            onClick={onDelete}
            disabled={!canDelete}
            title={
              role.is_system_default
                ? "System-default roles cannot be deleted"
                : role.assigned_users > 0
                ? "Reassign these users before deleting this role"
                : undefined
            }
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
    </div>
  );
}

// RoleForm handles create, edit, and view modes. View mode locks every
// input so an admin can inspect the system-default bundles without
// accidentally trying to change them.
function RoleForm({
  mode,
  existing,
  onCancel,
  onSaved,
}: {
  mode: "create" | "edit" | "view";
  existing?: RoleRow;
  onCancel: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const readOnly = mode === "view";
  const [name, setName] = useState(existing?.name ?? "");
  const [description, setDescription] = useState(existing?.description ?? "");
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(existing?.permissions.filter((p) => KNOWN_PERMISSION_KEYS.has(p)) ?? [])
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggle = (key: string) => {
    if (readOnly) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (readOnly) return;
    setError(null);
    setBusy(true);
    try {
      const perms = Array.from(selected);
      if (mode === "create") {
        const body: CreateRoleBody = {
          name: name.trim(),
          description: description.trim() || undefined,
          permissions: perms,
        };
        await api.createRole(body);
      } else if (existing) {
        const body: UpdateRoleBody = {
          name: name.trim(),
          description: description.trim(),
          permissions: perms,
        };
        await api.updateRole(existing.id, body);
      }
      await onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const totalSelected = selected.size;

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      {readOnly && existing?.is_system_default && (
        <div className="rounded-md border bg-muted/40 px-3 py-2 text-xs flex items-start gap-2">
          <Lock className="h-3.5 w-3.5 mt-0.5 text-amber-600 shrink-0" />
          <div>
            This is a system-default role. Its permission bundle is fixed so every tenant has a known-good
            starting point. To vary from these bundles, create a new custom role instead.
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div className="space-y-1">
          <label htmlFor="rf-name" className="text-xs font-medium text-muted-foreground">
            Role name
          </label>
          <input
            id="rf-name"
            type="text"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={readOnly || busy}
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60"
          />
          {!readOnly && (
            <p className="text-[11px] text-muted-foreground">
              Reserved names: <code>admin</code>, <code>operator</code>, <code>viewer</code>.
            </p>
          )}
        </div>
        <div className="space-y-1">
          <label htmlFor="rf-desc" className="text-xs font-medium text-muted-foreground">
            Description
          </label>
          <input
            id="rf-desc"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            disabled={readOnly || busy}
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60"
          />
        </div>
      </div>

      <div className="space-y-1">
        <div className="flex items-center justify-between">
          <label className="text-xs font-medium text-muted-foreground">
            Permissions
          </label>
          <div className="text-xs text-muted-foreground">
            {totalSelected} of {KNOWN_PERMISSION_KEYS.size} selected
          </div>
        </div>
        <div className="rounded-md border divide-y">
          {PERMISSIONS_BY_CATEGORY.map((group) => (
            <PermissionGroup
              key={group.category}
              category={group.category}
              items={group.items}
              selected={selected}
              readOnly={readOnly}
              onToggle={toggle}
              onToggleAll={(keys, on) => {
                if (readOnly) return;
                setSelected((prev) => {
                  const next = new Set(prev);
                  for (const k of keys) {
                    if (on) next.add(k);
                    else next.delete(k);
                  }
                  return next;
                });
              }}
            />
          ))}
        </div>
      </div>

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          {readOnly ? "Close" : "Cancel"}
        </Button>
        {!readOnly && (
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {mode === "create" ? "Create role" : "Save"}
          </Button>
        )}
      </div>
    </form>
  );
}

function PermissionGroup({
  category,
  items,
  selected,
  readOnly,
  onToggle,
  onToggleAll,
}: {
  category: string;
  items: readonly { key: string; title: string; description: string }[];
  selected: Set<string>;
  readOnly: boolean;
  onToggle: (key: string) => void;
  onToggleAll: (keys: string[], on: boolean) => void;
}) {
  const allKeys = items.map((i) => i.key);
  const countSelected = allKeys.filter((k) => selected.has(k)).length;
  const allOn = countSelected === allKeys.length;
  const anyOn = countSelected > 0;

  return (
    <div className="p-3">
      <div className="flex items-center justify-between mb-2">
        <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {category}
        </div>
        {!readOnly && (
          <button
            type="button"
            onClick={() => onToggleAll(allKeys, !allOn)}
            className="text-[11px] text-muted-foreground hover:text-foreground underline underline-offset-2"
          >
            {allOn ? "clear all" : anyOn ? "select all" : "select all"}
          </button>
        )}
      </div>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1.5">
        {items.map((p) => {
          const on = selected.has(p.key);
          return (
            <label
              key={p.key}
              className={`flex items-start gap-2 text-sm cursor-pointer rounded-md p-1.5 hover:bg-accent/30 ${
                readOnly ? "cursor-default" : ""
              }`}
            >
              <input
                type="checkbox"
                checked={on}
                onChange={() => onToggle(p.key)}
                disabled={readOnly}
                className="mt-0.5"
              />
              <span className="min-w-0">
                <span className="block text-sm">{p.title}</span>
                <span className="block text-[11px] text-muted-foreground leading-snug">
                  {p.description}
                </span>
              </span>
            </label>
          );
        })}
      </div>
    </div>
  );
}

function DeleteRoleConfirm({
  role,
  onCancel,
  onDone,
}: {
  role: RoleRow;
  onCancel: () => void;
  onDone: () => Promise<void> | void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const confirm = async () => {
    setError(null);
    setBusy(true);
    try {
      await api.deleteRole(role.id);
      await onDone();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <div className="flex gap-3 items-start">
        <AlertTriangle className="h-5 w-5 mt-0.5 text-amber-500 shrink-0" />
        <div className="text-sm">
          Delete role <span className="font-semibold">{role.name}</span>?
          {role.assigned_users > 0 && (
            <p className="mt-2 text-muted-foreground">
              {role.assigned_users} user{role.assigned_users === 1 ? "" : "s"} still hold this role — the
              backend will reject the delete. Reassign those users first.
            </p>
          )}
        </div>
      </div>
      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="button" variant="destructive" onClick={confirm} disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Delete role
        </Button>
      </div>
    </div>
  );
}
