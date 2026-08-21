"use client";

import { useCallback, useEffect, useState } from "react";
import {
  Bell,
  CheckCircle2,
  Loader2,
  Mail,
  MessageSquare,
  Plus,
  Send,
  Trash2,
  Webhook,
  XCircle,
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
import type {
  AlertSeverity,
  NotificationChannel,
  NotificationChannelBody,
  NotificationChannelType,
} from "@/lib/types";

const TYPE_ICON: Record<NotificationChannelType, React.ComponentType<{ className?: string }>> = {
  email: Mail,
  teams: MessageSquare,
  webhook: Webhook,
};

const TYPE_DESCRIPTION: Record<NotificationChannelType, string> = {
  email: "Email address — alerts arrive as plain-text email via the SMTP relay.",
  teams: "Microsoft Teams incoming-webhook URL — pasted from the Teams channel connector setup.",
  webhook: "Generic HTTP POST URL — receives the alert as JSON. Use for ServiceNow, Dynamics 365, Zapier, etc.",
};

const TARGET_PLACEHOLDER: Record<NotificationChannelType, string> = {
  email: "oncall@your-org.com",
  teams: "https://outlook.office.com/webhook/...",
  webhook: "https://hooks.example.com/incoming",
};

export default function NotificationsPage() {
  const session = useSession();
  // Variable names preserved for callsite compatibility. "admin" now
  // means "can CRUD notification channels"; "operator" means "can fire
  // a test message" — the finer split that already existed on the
  // backend, now honoured by the UI.
  const admin = hasPermission(session.user, "notification.crud");
  const operator = hasPermission(session.user, "notification.test");
  const [channels, setChannels] = useState<NotificationChannel[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ mode: "create" | "edit"; existing?: NotificationChannel } | null>(null);
  const [deleting, setDeleting] = useState<NotificationChannel | null>(null);
  const [testBusy, setTestBusy] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{ id: string; ok: boolean; msg: string } | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const cs = await api.listNotificationChannels(signal);
      if (signal?.aborted) return;
      setChannels(cs);
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

  const handleTest = async (ch: NotificationChannel) => {
    setTestBusy(ch.id);
    setTestResult(null);
    try {
      await api.testNotificationChannel(ch.id);
      setTestResult({ id: ch.id, ok: true, msg: "Sent. Check the destination." });
      void load();
    } catch (e) {
      setTestResult({ id: ch.id, ok: false, msg: (e as Error).message });
      void load();
    } finally {
      setTestBusy(null);
    }
  };

  const handleDelete = async () => {
    if (!deleting) return;
    await api.deleteNotificationChannel(deleting.id);
    setDeleting(null);
    void load();
  };

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div>
          <h1 className="text-xl font-semibold">Notification channels</h1>
          <p className="text-sm text-muted-foreground">
            Where alerts get dispatched when they first open
          </p>
        </div>
        <div className="flex items-center gap-3">
          {admin && (
            <Button size="sm" onClick={() => setEditing({ mode: "create" })}>
              <Plus className="h-3.5 w-3.5" />
              New channel
            </Button>
          )}
          <UserMenu />
        </div>
      </header>

      {editing && (
        <Modal
          open
          onClose={() => setEditing(null)}
          title={editing.mode === "edit" ? "Edit channel" : "New channel"}
          wide={false}
        >
          <ChannelForm
            mode={editing.mode}
            existing={editing.existing}
            onCancel={() => setEditing(null)}
            onSuccess={() => {
              setEditing(null);
              void load();
            }}
          />
        </Modal>
      )}

      {deleting && (
        <Modal
          open
          onClose={() => setDeleting(null)}
          title="Delete channel"
          wide={false}
        >
          <div className="space-y-4">
            <p className="text-sm">
              Delete <span className="font-semibold">{deleting.name}</span>? Future
              alerts will no longer be dispatched to this {deleting.type}.
            </p>
            <div className="flex justify-end gap-2 pt-2 border-t">
              <Button variant="ghost" onClick={() => setDeleting(null)}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={handleDelete}>
                Delete
              </Button>
            </div>
          </div>
        </Modal>
      )}

      <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-4">
        {loadError && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="p-4 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </CardContent>
          </Card>
        )}

        {channels === null ? (
          <div className="space-y-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} className="h-24 w-full" />
            ))}
          </div>
        ) : channels.length > 0 ? (
          <ul className="space-y-2">
            {channels.map((ch) => {
              const Icon = TYPE_ICON[ch.type];
              const showResult = testResult?.id === ch.id;
              return (
                <li key={ch.id}>
                  <Card>
                    <CardContent className="p-4">
                      <div className="flex items-start gap-3">
                        <div className="h-9 w-9 rounded-md bg-muted flex items-center justify-center shrink-0">
                          <Icon className="h-4 w-4" />
                        </div>
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="font-medium">{ch.name}</span>
                            {!ch.enabled && (
                              <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] uppercase tracking-wide text-muted-foreground">
                                disabled
                              </span>
                            )}
                            <span className="ml-auto text-[11px] text-muted-foreground">
                              min: <span className="font-medium">{ch.min_severity}</span>
                            </span>
                          </div>
                          <div className="mt-0.5 text-xs text-muted-foreground truncate">
                            {ch.target}
                          </div>
                          {(ch.last_sent_at || ch.last_error) && (
                            <div className="mt-2 text-[11px] flex flex-wrap gap-x-3 gap-y-0.5">
                              {ch.last_sent_at && (
                                <span className="text-muted-foreground">
                                  Last sent {formatRelative(ch.last_sent_at)}
                                </span>
                              )}
                              {ch.last_error && (
                                <span className="text-red-600">
                                  Last error: {ch.last_error}
                                </span>
                              )}
                            </div>
                          )}
                          {showResult && (
                            <div
                              className={`mt-2 inline-flex items-center gap-1 text-[11px] rounded px-2 py-0.5 ${
                                testResult.ok
                                  ? "bg-emerald-500/10 text-emerald-700"
                                  : "bg-red-500/10 text-red-700"
                              }`}
                            >
                              {testResult.ok ? (
                                <CheckCircle2 className="h-3 w-3" />
                              ) : (
                                <XCircle className="h-3 w-3" />
                              )}
                              {testResult.msg}
                            </div>
                          )}
                        </div>
                        <div className="flex gap-1 shrink-0">
                          {operator && (
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() => handleTest(ch)}
                              disabled={testBusy === ch.id}
                            >
                              {testBusy === ch.id ? (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              ) : (
                                <Send className="h-3.5 w-3.5" />
                              )}
                              Test
                            </Button>
                          )}
                          {admin && (
                            <>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => setEditing({ mode: "edit", existing: ch })}
                          >
                            Edit
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label="Delete channel"
                            onClick={() => setDeleting(ch)}
                          >
                            <Trash2 className="h-3.5 w-3.5 text-destructive" />
                          </Button>
                            </>
                          )}
                        </div>
                      </div>
                    </CardContent>
                  </Card>
                </li>
              );
            })}
          </ul>
        ) : (
          <Card>
            <CardContent className="p-10 text-center space-y-3">
              <Bell className="h-8 w-8 text-muted-foreground mx-auto" />
              <div className="text-sm text-muted-foreground">
                {admin
                  ? "No notification channels yet. Add one to start receiving alerts outside the portal."
                  : "No notification channels yet — ask an admin to add one."}
              </div>
              {admin && (
                <Button onClick={() => setEditing({ mode: "create" })}>
                  <Plus className="h-3.5 w-3.5" />
                  Add first channel
                </Button>
              )}
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

interface ChannelFormProps {
  mode: "create" | "edit";
  existing?: NotificationChannel;
  onCancel: () => void;
  onSuccess: () => void;
}

function ChannelForm({ mode, existing, onCancel, onSuccess }: ChannelFormProps) {
  const [name, setName] = useState(existing?.name ?? "");
  const [type, setType] = useState<NotificationChannelType>(existing?.type ?? "email");
  const [target, setTarget] = useState(existing?.target ?? "");
  const [minSeverity, setMinSeverity] = useState<AlertSeverity>(existing?.min_severity ?? "warning");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const body: NotificationChannelBody = {
        name: name.trim(),
        target: target.trim(),
        min_severity: minSeverity,
        enabled,
      };
      if (mode === "create") {
        body.type = type;
        await api.createNotificationChannel(body);
      } else {
        await api.updateNotificationChannel(existing!.id, body);
      }
      onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Name
        </label>
        <input
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="On-call email, AV ops Teams, ..."
          required
        />
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Type
        </label>
        <select
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm disabled:opacity-50"
          value={type}
          disabled={mode === "edit"}
          onChange={(e) => setType(e.target.value as NotificationChannelType)}
        >
          <option value="email">Email</option>
          <option value="teams">Microsoft Teams</option>
          <option value="webhook">Generic webhook</option>
        </select>
        <p className="mt-1 text-[11px] text-muted-foreground">
          {TYPE_DESCRIPTION[type]}
        </p>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Target
        </label>
        <input
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder={TARGET_PLACEHOLDER[type]}
          required
        />
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
            Minimum severity
          </label>
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            value={minSeverity}
            onChange={(e) => setMinSeverity(e.target.value as AlertSeverity)}
          >
            <option value="info">Info (everything)</option>
            <option value="warning">Warning</option>
            <option value="critical">Critical only</option>
          </select>
        </div>
        <div>
          <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
            Enabled
          </label>
          <label className="flex h-9 items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
              className="h-4 w-4"
            />
            Dispatching active
          </label>
        </div>
      </div>

      <div className="flex justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {mode === "create" ? "Create channel" : "Save changes"}
        </Button>
      </div>
    </form>
  );
}
