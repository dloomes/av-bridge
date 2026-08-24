"use client";

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  CircleUser,
  Link2,
  Loader2,
  Lock,
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
import { formatRelative } from "@/lib/utils";
import type { VendorUserRow } from "@/lib/types";

// /helpdesk/users — vendor-tenant user management (M3.1).
//
// Two things this surface exists for:
//
//   * Promote / demote helpdesk staff whose Entra group memberships
//     didn't cover their needs (or when a group mapping wasn't yet
//     configured at their first sign-in). Setting a role here flips
//     users.role_source to 'manual' so future Entra sign-ins won't
//     re-derive it from group churn.
//
//   * Deactivate a helpdesk user without deleting them (revokes every
//     open session immediately, retains audit history).
//
// Vendor-only: sidebar item is gated at the sidebar layer, and the
// backend endpoints RequireVendor. Non-vendor sessions that reach this
// URL directly get bounced by the AppShell.
export default function HelpdeskUsersPage() {
  const session = useSession();
  const router = useRouter();
  const [rows, setRows] = useState<VendorUserRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<VendorUserRow | null>(null);
  const [deleting, setDeleting] = useState<VendorUserRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listVendorUsers(signal);
      if (signal?.aborted) return;
      setRows(list);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    if (!session.hydrated) return;
    if (!session.user?.is_vendor) {
      router.replace("/");
      return;
    }
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [session.hydrated, session.user, router, load]);

  return (
    <div className="min-h-screen">
      <div className="max-w-6xl mx-auto p-6 space-y-4">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Helpdesk users</h1>
            <p className="text-sm text-muted-foreground">
              Your team. Roles here override Entra group mappings until
              cleared.
            </p>
          </div>
          <UserMenu />
        </header>

        {loadError && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))] flex items-center gap-2">
              <AlertTriangle className="h-4 w-4" />
              {loadError}
            </CardContent>
          </Card>
        )}

        {rows === null && !loadError ? (
          <Card>
            <CardContent className="p-4 space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14" />
              ))}
            </CardContent>
          </Card>
        ) : (rows ?? []).length === 0 ? (
          <Card>
            <CardContent className="p-10 text-center text-sm text-muted-foreground">
              No helpdesk users yet. Add someone to your Entra tenant and
              have them sign in.
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-3 border-b bg-muted/40 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                <div>User</div>
                <div>Role</div>
                <div>Last sign-in</div>
                <div className="w-[180px]" />
              </div>
              {(rows ?? []).map((u) => (
                <UserRow
                  key={u.id}
                  u={u}
                  isSelf={session.user?.user_id === u.id}
                  onEdit={() => setEditing(u)}
                  onDelete={() => setDeleting(u)}
                />
              ))}
            </CardContent>
          </Card>
        )}
      </div>

      <Modal
        open={editing !== null}
        onClose={() => setEditing(null)}
        title="Edit helpdesk user"
        wide={false}
      >
        {editing && (
          <EditForm
            user={editing}
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
        title="Delete helpdesk user"
        wide={false}
      >
        {deleting && (
          <DeleteConfirm
            user={deleting}
            onCancel={() => setDeleting(null)}
            onConfirm={async () => {
              await api.deleteVendorUser(deleting.id);
              setDeleting(null);
              await load();
            }}
          />
        )}
      </Modal>
    </div>
  );
}

interface UserRowProps {
  u: VendorUserRow;
  isSelf: boolean;
  onEdit: () => void;
  onDelete: () => void;
}

function UserRow({ u, isSelf, onEdit, onDelete }: UserRowProps) {
  return (
    <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-3 border-b px-4 py-3 text-sm last:border-b-0 hover:bg-muted/30">
      <div className="min-w-0">
        <div className="font-medium truncate flex items-center gap-2">
          {u.full_name || u.email}
          {u.disabled && (
            <span className="rounded-md border border-muted-foreground/40 bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
              Disabled
            </span>
          )}
          {isSelf && (
            <span className="rounded-md border border-primary/40 bg-primary/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-primary">
              You
            </span>
          )}
        </div>
        {u.full_name && (
          <div className="text-xs font-mono text-muted-foreground truncate">
            {u.email}
          </div>
        )}
      </div>
      <div className="flex items-center gap-2 min-w-0">
        <span className="rounded-md border bg-background px-2 py-0.5 text-xs font-medium capitalize">
          {u.role}
        </span>
        {u.role_source === "entra" ? (
          <span
            title="Role is derived from an Entra group mapping — will auto-sync on next sign-in."
            className="inline-flex items-center gap-1 rounded-md border border-input bg-transparent px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground"
          >
            <Link2 className="h-3 w-3" />
            Entra
          </span>
        ) : (
          <span
            title="Manual override — Entra group changes won't affect this role until cleared by an admin."
            className="inline-flex items-center gap-1 rounded-md border border-input bg-transparent px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground"
          >
            <Lock className="h-3 w-3" />
            Manual
          </span>
        )}
      </div>
      <div className="text-xs text-muted-foreground min-w-0 truncate">
        {u.last_login_at ? formatRelative(u.last_login_at) : "—"}
      </div>
      <div className="flex items-center gap-1 justify-end w-[180px]">
        <Button size="sm" variant="ghost" onClick={onEdit}>
          Edit
        </Button>
        <Button
          size="icon"
          variant="ghost"
          aria-label="Delete user"
          onClick={onDelete}
          disabled={isSelf}
          title={isSelf ? "You can't delete yourself" : "Delete user"}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}

interface EditFormProps {
  user: VendorUserRow;
  onCancel: () => void;
  onSaved: () => Promise<void>;
}

const ROLES = ["admin", "operator", "viewer"] as const;

function EditForm({ user, onCancel, onSaved }: EditFormProps) {
  const [fullName, setFullName] = useState(user.full_name ?? "");
  const [role, setRole] = useState(user.role);
  const [disabled, setDisabled] = useState(user.disabled);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const dirty =
    fullName.trim() !== (user.full_name ?? "").trim() ||
    role !== user.role ||
    disabled !== user.disabled;

  const roleChanged = role !== user.role;

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!dirty) {
      onCancel();
      return;
    }
    setSubmitting(true);
    try {
      const body: {
        full_name?: string;
        role?: string;
        disabled?: boolean;
      } = {};
      if (fullName.trim() !== (user.full_name ?? "").trim()) {
        body.full_name = fullName.trim();
      }
      if (role !== user.role) body.role = role;
      if (disabled !== user.disabled) body.disabled = disabled;
      await api.updateVendorUser(user.id, body);
      await onSaved();
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <label className="text-xs font-medium text-muted-foreground">Email</label>
        <div className="font-mono text-sm truncate">{user.email}</div>
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="full_name" className="text-xs font-medium text-muted-foreground">
          Full name
        </label>
        <input
          id="full_name"
          value={fullName}
          onChange={(e) => setFullName(e.target.value)}
          disabled={submitting}
          className="h-9 rounded-md border border-input bg-background px-3 text-sm outline-none focus:border-primary"
        />
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
          {ROLES.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
        {roleChanged && (
          <p className="mt-0.5 flex items-start gap-1.5 text-[11px] text-muted-foreground">
            <ShieldCheck className="mt-px h-3 w-3 shrink-0" />
            Setting a role here flips the source to manual — future Entra
            sign-ins won&rsquo;t change this role.
          </p>
        )}
      </div>

      <label className="flex items-start gap-3 rounded-md border border-input p-3">
        <input
          type="checkbox"
          className="mt-1 h-4 w-4"
          checked={disabled}
          onChange={(e) => setDisabled(e.target.checked)}
          disabled={submitting}
        />
        <div>
          <div className="text-sm font-medium">Disable this user</div>
          <div className="text-xs text-muted-foreground">
            Revokes all active sessions immediately. Re-enable at any time.
          </div>
        </div>
      </label>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting || !dirty}>
          {submitting ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Saving…
            </>
          ) : (
            "Save"
          )}
        </Button>
      </div>
    </form>
  );
}

interface DeleteConfirmProps {
  user: VendorUserRow;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}

function DeleteConfirm({ user, onCancel, onConfirm }: DeleteConfirmProps) {
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
      <p className="flex items-start gap-2">
        <CircleUser className="mt-0.5 h-4 w-4 text-muted-foreground shrink-0" />
        Delete <span className="font-medium">{user.full_name || user.email}</span>?
        Their sessions drop immediately and they&rsquo;ll be JIT-created fresh
        on their next Entra sign-in (unless you also remove their access in
        Entra).
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
