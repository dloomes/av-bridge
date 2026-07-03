"use client";

import { useCallback, useEffect, useState } from "react";
import {
  KeyRound,
  Loader2,
  Pencil,
  Plus,
  Trash2,
  UserCheck,
  UserX,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type { CreateUserBody, UpdateUserBody, UserRow } from "@/lib/types";

// UsersPage — tenant user roster.
//
// Read is any authed user in a tenant. Writes (create / edit / reset /
// delete) are admin-only, gated both at the sidebar level (link hidden for
// non-admins) and by the backend RequireRole middleware. The UI still hides
// write affordances when the user isn't an admin — matches how the rest of
// the portal treats role gating (UX polish; the backend is authoritative).
export default function UsersPage() {
  const session = useSession();
  const admin = isAdmin(session.user?.role);
  const [users, setUsers] = useState<UserRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ mode: "create" | "edit"; existing?: UserRow } | null>(null);
  const [resetting, setResetting] = useState<UserRow | null>(null);
  const [deleting, setDeleting] = useState<UserRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listUsers(signal);
      if (signal?.aborted) return;
      setUsers(list);
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

  const handleToggleDisabled = async (u: UserRow) => {
    try {
      await api.updateUser(u.id, { disabled: !u.disabled });
      await load();
    } catch (e) {
      alert((e as Error).message);
    }
  };

  return (
    <div className="min-h-screen">
      <div className="max-w-6xl mx-auto p-6 space-y-6">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Users</h1>
            <p className="text-sm text-muted-foreground">
              {session.user?.is_vendor && session.scope
                ? "Users in the customer you're currently acting as."
                : "Users in your tenant."}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {admin && (
              <Button onClick={() => setEditing({ mode: "create" })}>
                <Plus className="h-4 w-4" />
                New user
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

        {users === null ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : users.length === 0 ? (
          <Card>
            <CardContent className="p-8 text-center text-sm text-muted-foreground">
              No users yet. {admin && "Click New user to add one."}
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="p-0">
              <div className="divide-y">
                {users.map((u) => (
                  <UserRowView
                    key={u.id}
                    user={u}
                    admin={admin}
                    isSelf={u.id === session.user?.user_id}
                    onEdit={() => setEditing({ mode: "edit", existing: u })}
                    onReset={() => setResetting(u)}
                    onToggle={() => handleToggleDisabled(u)}
                    onDelete={() => setDeleting(u)}
                  />
                ))}
              </div>
            </CardContent>
          </Card>
        )}

        <Modal
          open={editing !== null}
          onClose={() => setEditing(null)}
          title={editing?.mode === "edit" ? "Edit user" : "New user"}
          wide={false}
        >
          {editing && (
            <UserForm
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
          open={resetting !== null}
          onClose={() => setResetting(null)}
          title={`Reset password for ${resetting?.email ?? ""}`}
          wide={false}
        >
          {resetting && (
            <ResetPasswordForm
              user={resetting}
              onCancel={() => setResetting(null)}
              onDone={async () => {
                setResetting(null);
                await load();
              }}
            />
          )}
        </Modal>

        <Modal
          open={deleting !== null}
          onClose={() => setDeleting(null)}
          title={`Delete user ${deleting?.email ?? ""}`}
          wide={false}
        >
          {deleting && (
            <DeleteUserConfirm
              user={deleting}
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

function UserRowView({
  user,
  admin,
  isSelf,
  onEdit,
  onReset,
  onToggle,
  onDelete,
}: {
  user: UserRow;
  admin: boolean;
  isSelf: boolean;
  onEdit: () => void;
  onReset: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="flex items-center gap-3 p-3">
      <div
        className={`h-9 w-9 rounded-full flex items-center justify-center text-[11px] font-semibold shrink-0 ${
          user.disabled ? "bg-muted text-muted-foreground" : "bg-foreground text-background"
        }`}
        aria-hidden="true"
      >
        {(user.full_name ?? user.email).slice(0, 2).toUpperCase()}
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium flex items-center gap-2">
          {user.full_name || user.email}
          {isSelf && <span className="text-[10px] uppercase tracking-wide bg-muted px-1 py-0.5 rounded">you</span>}
        </div>
        <div className="text-xs text-muted-foreground truncate">{user.email}</div>
      </div>
      <div className="flex items-center gap-2 text-[11px]">
        <span
          className={`rounded px-1.5 py-0.5 font-medium ${
            user.role === "admin"
              ? "bg-amber-500/10 text-amber-700 dark:text-amber-300"
              : user.role === "operator"
              ? "bg-sky-500/10 text-sky-700 dark:text-sky-300"
              : "bg-muted text-muted-foreground"
          }`}
        >
          {user.role}
        </span>
        {user.disabled && (
          <span className="rounded bg-destructive/10 px-1.5 py-0.5 font-medium [color:hsl(var(--destructive))]">
            disabled
          </span>
        )}
      </div>
      <div className="hidden md:block w-40 text-right text-xs text-muted-foreground">
        {user.last_login_at ? `Last login ${formatRelative(user.last_login_at)}` : "Never signed in"}
      </div>
      {admin && (
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="icon" aria-label="Edit user" onClick={onEdit}>
            <Pencil className="h-3.5 w-3.5" />
          </Button>
          <Button variant="ghost" size="icon" aria-label="Reset password" onClick={onReset}>
            <KeyRound className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label={user.disabled ? "Enable user" : "Disable user"}
            onClick={onToggle}
            disabled={isSelf && !user.disabled}
            title={isSelf && !user.disabled ? "You cannot disable yourself" : undefined}
          >
            {user.disabled ? (
              <UserCheck className="h-3.5 w-3.5" />
            ) : (
              <UserX className="h-3.5 w-3.5" />
            )}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Delete user"
            onClick={onDelete}
            disabled={isSelf}
            title={isSelf ? "You cannot delete yourself" : undefined}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}
    </div>
  );
}

// UserForm handles both create and edit. Create requires password; edit
// keeps it optional (there's a separate reset flow).
function UserForm({
  mode,
  existing,
  onCancel,
  onSaved,
}: {
  mode: "create" | "edit";
  existing?: UserRow;
  onCancel: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const [email, setEmail] = useState(existing?.email ?? "");
  const [fullName, setFullName] = useState(existing?.full_name ?? "");
  const [role, setRole] = useState<UserRow["role"]>(existing?.role ?? "viewer");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (mode === "create") {
        const body: CreateUserBody = {
          email: email.trim(),
          password,
          full_name: fullName.trim() || undefined,
          role,
        };
        await api.createUser(body);
      } else if (existing) {
        const body: UpdateUserBody = {
          full_name: fullName.trim(),
          role,
        };
        await api.updateUser(existing.id, body);
      }
      await onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="space-y-1">
        <label htmlFor="uf-email" className="text-xs font-medium text-muted-foreground">
          Email
        </label>
        <input
          id="uf-email"
          type="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          disabled={mode === "edit" || busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60"
        />
        {mode === "edit" && (
          <p className="text-[11px] text-muted-foreground">
            Email is the login identifier — changing it would require deleting and recreating the user.
          </p>
        )}
      </div>

      <div className="space-y-1">
        <label htmlFor="uf-name" className="text-xs font-medium text-muted-foreground">
          Full name
        </label>
        <input
          id="uf-name"
          type="text"
          value={fullName}
          onChange={(e) => setFullName(e.target.value)}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
        />
      </div>

      <div className="space-y-1">
        <label htmlFor="uf-role" className="text-xs font-medium text-muted-foreground">
          Role
        </label>
        <select
          id="uf-role"
          value={role}
          onChange={(e) => setRole(e.target.value as UserRow["role"])}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="viewer">Viewer — read only</option>
          <option value="operator">Operator — reads + commands + alert lifecycle</option>
          <option value="admin">Admin — full tenant management</option>
        </select>
      </div>

      {mode === "create" && (
        <div className="space-y-1">
          <label htmlFor="uf-pw" className="text-xs font-medium text-muted-foreground">
            Initial password
          </label>
          <input
            id="uf-pw"
            type="password"
            required
            minLength={12}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={busy}
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          />
          <p className="text-[11px] text-muted-foreground">
            Minimum 12 characters. Share it with the user out of band; they can change it after first sign-in.
          </p>
        </div>
      )}

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {mode === "create" ? "Create user" : "Save"}
        </Button>
      </div>
    </form>
  );
}

function ResetPasswordForm({
  user,
  onCancel,
  onDone,
}: {
  user: UserRow;
  onCancel: () => void;
  onDone: () => Promise<void> | void;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await api.resetUserPassword(user.id, password);
      await onDone();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}
      <p className="text-sm text-muted-foreground">
        This sets a new password for <span className="font-medium">{user.email}</span> and revokes every active
        session they have. Share the new password out of band.
      </p>
      <div className="space-y-1">
        <label htmlFor="rp-pw" className="text-xs font-medium text-muted-foreground">
          New password
        </label>
        <input
          id="rp-pw"
          type="password"
          required
          minLength={12}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
        />
        <p className="text-[11px] text-muted-foreground">Minimum 12 characters.</p>
      </div>
      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Reset password
        </Button>
      </div>
    </form>
  );
}

function DeleteUserConfirm({
  user,
  onCancel,
  onDone,
}: {
  user: UserRow;
  onCancel: () => void;
  onDone: () => Promise<void> | void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const confirm = async () => {
    setError(null);
    setBusy(true);
    try {
      await api.deleteUser(user.id);
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
      <p className="text-sm">
        Permanently delete <span className="font-semibold">{user.email}</span>? Their sessions are revoked
        immediately. If you might need them back later, disable the user instead.
      </p>
      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="button" variant="destructive" onClick={confirm} disabled={busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Delete user
        </Button>
      </div>
    </div>
  );
}
