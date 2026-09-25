"use client";

import { useCallback, useEffect, useState } from "react";
import {
  KeyRound,
  Loader2,
  MapPin,
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
import { MagicLinkModal, MagicLinkTrigger } from "@/components/magic-link-modal";
import {
  EMPTY_SCOPE,
  ScopeTreePicker,
  describeScope,
  scopeIsEmpty,
  type ScopeSelection,
} from "@/components/scope-tree-picker";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import type { Branding, BusinessUnit } from "@/lib/api";
import { hasPermission } from "@/lib/session";
import { formatRelative } from "@/lib/utils";
import type {
  BuildingRow,
  CreateUserBody,
  NamedRow,
  RoleRow,
  UpdateUserBody,
  UserRow,
} from "@/lib/types";

// UsersPage — tenant user roster with multi-role assignment + optional
// physical scope. Reads are any authed user with view.users; writes are
// gated by user.create / user.update / user.reset_password / user.delete.
// Physical scope is edited with ScopeTreePicker and enforced by database
// row-level security. Only whole-tenant users may change it; restricted
// admins see it read-only (the API rejects their scope changes).
export default function UsersPage() {
  const session = useSession();
  // "admin" flag now = "can perform any user management action".
  // Individual buttons still hit backend perms — this is a page-level
  // "show the management chrome" gate, not per-action authorisation.
  const admin =
    hasPermission(session.user, "user.create") ||
    hasPermission(session.user, "user.update") ||
    hasPermission(session.user, "user.delete") ||
    hasPermission(session.user, "user.reset_password");
  const [users, setUsers] = useState<UserRow[] | null>(null);
  const [roles, setRoles] = useState<RoleRow[] | null>(null);
  const [buildings, setBuildings] = useState<BuildingRow[] | null>(null);
  const [regions, setRegions] = useState<NamedRow[]>([]);
  const [locations, setLocations] = useState<NamedRow[]>([]);
  const [rooms, setRooms] = useState<NamedRow[]>([]);
  const [businessUnits, setBusinessUnits] = useState<BusinessUnit[]>([]);
  const [buFlagOn, setBUFlagOn] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ mode: "create" | "edit"; existing?: UserRow } | null>(null);
  const [resetting, setResetting] = useState<UserRow | null>(null);
  const [deleting, setDeleting] = useState<UserRow | null>(null);
  // Break-glass sign-in link (M4.1). Populated after a successful mint;
  // rendering shows the URL + expiry countdown. Vendor-only in UI +
  // enforced server-side.
  const [magicLink, setMagicLink] = useState<
    { user: UserRow; url: string; expiresAt: string } | null
  >(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      // Fetch everything the page + form needs in parallel so the modal
      // has its role + building + BU lists ready as soon as it opens.
      // Branding gives us the business_units_enabled flag; BU list is
      // best-effort (empty on failure keeps the page working).
      const [us, rs, bs, br, bus, rgs, locs, rms] = await Promise.all([
        api.listUsers(signal),
        api.listRoles(signal),
        api.listBuildings(signal),
        api.getBranding(signal).catch(() => ({} as Branding)),
        api.listBusinessUnits(signal).catch(() => [] as BusinessUnit[]),
        api.listRegions(signal),
        api.listLocations(signal),
        api.listRooms(signal),
      ]);
      if (signal?.aborted) return;
      setUsers(us);
      setRoles(rs);
      setBuildings(bs);
      setBUFlagOn(Boolean(br.business_units_enabled));
      setBusinessUnits(bus);
      setRegions(rgs);
      setLocations(locs);
      setRooms(rms);
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
                    isVendor={!!session.user?.is_vendor}
                    isSelf={u.id === session.user?.user_id}
                    onEdit={() => setEditing({ mode: "edit", existing: u })}
                    onReset={() => setResetting(u)}
                    onToggle={() => handleToggleDisabled(u)}
                    onDelete={() => setDeleting(u)}
                    onMagicLink={(r) => setMagicLink({ user: u, ...r })}
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
        >
          {editing && roles && buildings && (
            <UserForm
              mode={editing.mode}
              existing={editing.existing}
              roles={roles}
              buildings={buildings}
              businessUnits={buFlagOn ? businessUnits : []}
              regions={regions}
              locations={locations}
              rooms={rooms}
              canEditScope={!session.user?.is_scoped}
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

        <Modal
          open={magicLink !== null}
          onClose={() => setMagicLink(null)}
          title="One-time sign-in link"
          wide={false}
        >
          {magicLink && (
            <MagicLinkModal
              url={magicLink.url}
              expiresAt={magicLink.expiresAt}
              targetLabel={magicLink.user.full_name || magicLink.user.email}
              onClose={() => setMagicLink(null)}
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
  isVendor,
  isSelf,
  onEdit,
  onReset,
  onToggle,
  onDelete,
  onMagicLink,
}: {
  user: UserRow;
  admin: boolean;
  isVendor: boolean;
  isSelf: boolean;
  onEdit: () => void;
  onReset: () => void;
  onToggle: () => void;
  onDelete: () => void;
  onMagicLink: (result: { url: string; expiresAt: string }) => void;
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
      <div className="flex flex-wrap items-center gap-1 max-w-xs justify-end">
        {user.role_names.length === 0 ? (
          <span className="text-[11px] text-muted-foreground italic">no roles</span>
        ) : (
          user.role_names.map((name) => (
            <span
              key={name}
              className={`rounded px-1.5 py-0.5 text-[11px] font-medium ${
                name === "admin"
                  ? "bg-amber-500/10 text-amber-700 dark:text-amber-300"
                  : name === "operator"
                  ? "bg-sky-500/10 text-sky-700 dark:text-sky-300"
                  : name === "viewer"
                  ? "bg-muted text-muted-foreground"
                  : "bg-primary/10 text-primary"
              }`}
            >
              {name}
            </span>
          ))
        )}
        {!scopeIsEmpty(userScope(user)) && (
          <span
            className="inline-flex items-center gap-1 rounded bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 px-1.5 py-0.5 text-[11px] font-medium"
            title={`Restricted to ${describeScope(userScope(user))}`}
          >
            <MapPin className="h-3 w-3" />
            {describeScope(userScope(user))}
          </span>
        )}
        {user.disabled && (
          <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-[11px] font-medium [color:hsl(var(--destructive))]">
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
          {isVendor && !user.disabled && (
            <MagicLinkTrigger
              onMint={() => api.issueMagicLink(user.id)}
              onResult={onMagicLink}
            />
          )}
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

// UserForm handles both create and edit. Create requires password + at
// least one role. Edit doesn't change email (email is the login identifier)
// but everything else is mutable including full multi-role reassignment.
function UserForm({
  mode,
  existing,
  roles,
  buildings,
  businessUnits,
  regions,
  locations,
  rooms,
  canEditScope,
  onCancel,
  onSaved,
}: {
  mode: "create" | "edit";
  existing?: UserRow;
  roles: RoleRow[];
  buildings: BuildingRow[];
  // Empty array = no business-unit tier in the scope tree (either the flag
  // is off for this tenant, or the tenant has no BUs).
  businessUnits: BusinessUnit[];
  regions: NamedRow[];
  locations: NamedRow[];
  rooms: NamedRow[];
  // False for restricted admins — the API only lets whole-tenant users
  // change physical scope, so the tree is shown read-only.
  canEditScope: boolean;
  onCancel: () => void;
  onSaved: () => Promise<void> | void;
}) {
  const [email, setEmail] = useState(existing?.email ?? "");
  const [fullName, setFullName] = useState(existing?.full_name ?? "");
  const [selectedRoles, setSelectedRoles] = useState<Set<string>>(
    () => new Set(existing?.role_ids ?? [])
  );
  const [scope, setScope] = useState<ScopeSelection>(() =>
    existing ? userScope(existing) : EMPTY_SCOPE
  );
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggleRole = (id: string) => {
    setSelectedRoles((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (selectedRoles.size === 0) {
      setError("Pick at least one role.");
      return;
    }
    setBusy(true);
    try {
      if (mode === "create") {
        const body: CreateUserBody = {
          email: email.trim(),
          password,
          full_name: fullName.trim() || undefined,
          role_ids: Array.from(selectedRoles),
          // Restricted admins can't set scope; their new users inherit it.
          ...(canEditScope && !scopeIsEmpty(scope) ? scopeBody(scope) : {}),
        };
        await api.createUser(body);
      } else if (existing) {
        const body: UpdateUserBody = {
          full_name: fullName.trim(),
          role_ids: Array.from(selectedRoles),
          ...(canEditScope ? scopeBody(scope) : {}),
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

  // Sort roles: system defaults first (admin > operator > viewer), then custom alphabetically.
  const sortedRoles = [...roles].sort((a, b) => {
    if (a.is_system_default !== b.is_system_default) {
      return a.is_system_default ? -1 : 1;
    }
    if (a.is_system_default) {
      const order = { admin: 0, operator: 1, viewer: 2 };
      const ao = order[a.name as keyof typeof order] ?? 99;
      const bo = order[b.name as keyof typeof order] ?? 99;
      return ao - bo;
    }
    return a.name.localeCompare(b.name);
  });

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div className="space-y-1">
          <label htmlFor="uf-email" className="text-xs font-medium text-muted-foreground">
            Email
          </label>
          <input
            id="uf-email"
            type="email"
            autoComplete="off"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={mode === "edit" || busy}
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60"
          />
          {mode === "edit" && (
            <p className="text-[11px] text-muted-foreground">
              Email is the login identifier — to change it, delete and recreate the user.
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
      </div>

      <div className="space-y-1">
        <div className="flex items-center justify-between">
          <label className="text-xs font-medium text-muted-foreground">
            Roles <span className="text-destructive">*</span>
          </label>
          <span className="text-xs text-muted-foreground">
            {selectedRoles.size} of {roles.length} selected
          </span>
        </div>
        <div className="rounded-md border max-h-56 overflow-y-auto divide-y">
          {sortedRoles.length === 0 ? (
            <div className="p-3 text-xs text-muted-foreground">
              No roles defined yet — an admin needs to create at least one role first.
            </div>
          ) : (
            sortedRoles.map((r) => {
              const on = selectedRoles.has(r.id);
              return (
                <label
                  key={r.id}
                  className="flex items-start gap-2 p-2 cursor-pointer hover:bg-accent/30 text-sm"
                >
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={() => toggleRole(r.id)}
                    disabled={busy}
                    className="mt-0.5"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2">
                      <span className="font-medium">{r.name}</span>
                      {r.is_system_default && (
                        <span className="text-[10px] uppercase tracking-wide bg-muted px-1 py-0.5 rounded text-muted-foreground">
                          system
                        </span>
                      )}
                      <span className="text-[10px] text-muted-foreground">
                        {r.permissions.length} perm{r.permissions.length === 1 ? "" : "s"}
                      </span>
                    </span>
                    {r.description && (
                      <span className="block text-[11px] text-muted-foreground leading-snug mt-0.5">
                        {r.description}
                      </span>
                    )}
                  </span>
                </label>
              );
            })
          )}
        </div>
        <p className="text-[11px] text-muted-foreground">
          Effective permissions are the union of every selected role&apos;s permission bundle.
        </p>
      </div>

      <div className="space-y-1">
        <div className="flex items-center justify-between">
          <label className="text-xs font-medium text-muted-foreground flex items-center gap-1">
            <MapPin className="h-3 w-3" /> Physical scope{" "}
            <span className="text-muted-foreground/70">(optional)</span>
          </label>
          <span className="text-xs text-muted-foreground">{describeScope(scope)}</span>
        </div>
        <ScopeTreePicker
          businessUnits={businessUnits}
          regions={regions}
          locations={locations}
          buildings={buildings}
          rooms={rooms}
          value={scope}
          onChange={setScope}
          disabled={busy || !canEditScope}
        />
        <p className="text-[11px] text-muted-foreground">
          {canEditScope
            ? "Leave everything unticked for access to the whole tenant. Ticking a business unit, region, location, building or room gives access to it and everything inside it, including anything added later. Ticks at different levels combine."
            : mode === "create"
              ? "You're restricted to part of the tenant, so new users get your scope. Only admins with access to the whole tenant can change it."
              : "You're restricted to part of the tenant, so you can't change a user's scope. Ask an admin with access to the whole tenant."}
        </p>
      </div>

      {mode === "create" && (
        <div className="space-y-1">
          <label htmlFor="uf-pw" className="text-xs font-medium text-muted-foreground">
            Initial password
          </label>
          <input
            id="uf-pw"
            type="password"
            autoComplete="new-password"
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

// userScope reads a user's physical scope as one selection. The region /
// location / room lists are optional on the wire (older API builds).
function userScope(u: UserRow): ScopeSelection {
  return {
    business_unit_ids: u.business_unit_scope_ids ?? [],
    region_ids: u.region_scope_ids ?? [],
    location_ids: u.location_scope_ids ?? [],
    building_ids: u.building_scope_ids ?? [],
    room_ids: u.room_scope_ids ?? [],
  };
}

// scopeBody maps a selection onto the create/update request fields.
function scopeBody(s: ScopeSelection) {
  return {
    business_unit_scope_ids: s.business_unit_ids,
    region_scope_ids: s.region_ids,
    location_scope_ids: s.location_ids,
    building_scope_ids: s.building_ids,
    room_scope_ids: s.room_ids,
  };
}
