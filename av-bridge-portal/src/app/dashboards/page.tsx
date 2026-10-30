"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import {
  Copy,
  LayoutDashboard,
  MonitorPlay,
  Pencil,
  Plus,
  Share2,
  Trash2,
  Tv,
  Users,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Modal } from "@/components/modal";
import { UserMenu } from "@/components/user-menu";
import { DashboardGrid } from "@/components/dashboard/grid";
import { DisplayLinks } from "@/components/dashboard/display-links";
import {
  WIDGETS,
  WIDGET_BY_TYPE,
  type ConfigField,
  type WidgetConfig,
} from "@/components/dashboard/widgets";
import {
  api,
  type Dashboard,
  type DashboardSummary,
  type DashboardWidget,
  type DashboardWidgetSize,
  type RoomTag,
} from "@/lib/api";
import type { DeviceSummary, NamedRow } from "@/lib/types";
import { cn } from "@/lib/utils";

const LAST_KEY = "marcus.dashboard.last";

const labelClass = "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

// A new dashboard can start from these, so it isn't empty on day one.
const STARTER: DashboardWidget[] = [
  { id: "status", type: "status", size: "m", config: {} },
  { id: "attention", type: "attention", size: "m", config: {} },
  { id: "readiness", type: "readiness", size: "m", config: {} },
  { id: "alerts", type: "alerts", size: "l", config: {} },
  { id: "usage", type: "usage", size: "l", config: {} },
];

function newId() {
  return Math.random().toString(36).slice(2, 10);
}

function errorText(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e);
  const i = msg.indexOf("{");
  if (i >= 0) {
    try {
      const body = JSON.parse(msg.slice(i));
      if (typeof body.error === "string") return body.error;
    } catch {}
  }
  return msg;
}

function readLast(): string | null {
  try {
    return localStorage.getItem(LAST_KEY);
  } catch {
    return null;
  }
}

function writeLast(id: string) {
  try {
    localStorage.setItem(LAST_KEY, id);
  } catch {}
}

export default function DashboardsPage() {
  const router = useRouter();
  const params = useSearchParams();
  const [list, setList] = useState<DashboardSummary[] | null>(null);
  const [canShare, setCanShare] = useState(false);
  const [current, setCurrent] = useState<Dashboard | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<DashboardWidget[]>([]);
  const [saving, setSaving] = useState(false);

  const [pickerOpen, setPickerOpen] = useState(false);
  const [settingsFor, setSettingsFor] = useState<string | null>(null);
  const [dialog, setDialog] = useState<null | "new" | "rename" | "delete" | "displays">(null);

  const wantedId = params.get("id");

  const loadList = useCallback(async () => {
    const r = await api.listDashboards();
    setList(r.dashboards);
    setCanShare(r.can_share);
    return r.dashboards;
  }, []);

  const open = useCallback(
    async (id: string) => {
      try {
        const d = await api.getDashboard(id);
        setCurrent(d);
        setError(null);
        writeLast(id);
      } catch (e) {
        setError(errorText(e));
      }
    },
    []
  );

  // First load: the dashboard in the URL, else the last one opened, else
  // the first in the list.
  useEffect(() => {
    loadList()
      .then((ds) => {
        const pick =
          (wantedId && ds.find((d) => d.id === wantedId)) ||
          ds.find((d) => d.id === readLast()) ||
          ds[0];
        if (pick) void open(pick.id);
      })
      .catch((e) => setError(errorText(e)));
  }, [loadList, open, wantedId]);

  const select = (id: string) => {
    if (editing) return;
    router.replace(`/dashboards?id=${encodeURIComponent(id)}`);
    void open(id);
  };

  const startEdit = () => {
    if (!current) return;
    setDraft(current.layout.widgets.map((w) => ({ ...w, config: { ...(w.config ?? {}) } })));
    setEditing(true);
  };

  const save = async () => {
    if (!current) return;
    setSaving(true);
    try {
      const d = await api.updateDashboard(current.id, { layout: { widgets: draft } });
      setCurrent(d);
      setEditing(false);
      void loadList();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setSaving(false);
    }
  };

  const copy = async () => {
    if (!current) return;
    try {
      const d = await api.createDashboard({ name: `${current.name} (copy)`, layout: current.layout });
      await loadList();
      select(d.id);
    } catch (e) {
      setError(errorText(e));
    }
  };

  const toggleShare = async () => {
    if (!current) return;
    try {
      const d = await api.updateDashboard(current.id, { shared: !current.shared });
      setCurrent(d);
      void loadList();
    } catch (e) {
      setError(errorText(e));
    }
  };

  const widgets = editing ? draft : current?.layout.widgets ?? [];
  const shared = useMemo(() => (list ?? []).filter((d) => d.shared), [list]);
  const mine = useMemo(() => (list ?? []).filter((d) => !d.shared), [list]);

  return (
    <div className="flex h-app flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <LayoutDashboard className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold">Dashboards</h1>
          {list && list.length > 0 && (
            <select
              aria-label="Dashboard"
              className={cn(inputClass, "w-auto min-w-[14rem]")}
              value={current?.id ?? ""}
              onChange={(e) => select(e.target.value)}
              disabled={editing}
            >
              {shared.length > 0 && (
                <optgroup label="Shared with everyone">
                  {shared.map((d) => (
                    <option key={d.id} value={d.id}>{d.name}</option>
                  ))}
                </optgroup>
              )}
              {mine.length > 0 && (
                <optgroup label="My dashboards">
                  {mine.map((d) => (
                    <option key={d.id} value={d.id}>{d.name}</option>
                  ))}
                </optgroup>
              )}
            </select>
          )}
          {current?.shared && !editing && (
            <span className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
              <Users className="h-3 w-3" /> Shared
            </span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {editing ? (
            <>
              <Button variant="outline" size="sm" onClick={() => setPickerOpen(true)}>
                <Plus className="h-3.5 w-3.5" /> Add widget
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setEditing(false)} disabled={saving}>
                Cancel
              </Button>
              <Button size="sm" onClick={save} disabled={saving}>
                {saving ? "Saving…" : "Save"}
              </Button>
            </>
          ) : (
            <>
              {current?.can_edit && (
                <Button variant="outline" size="sm" onClick={startEdit}>
                  <Pencil className="h-3.5 w-3.5" /> Edit
                </Button>
              )}
              {current && (
                <Button asChild variant="outline" size="sm" title="Show this dashboard full screen">
                  <a href={`/display?id=${encodeURIComponent(current.id)}`} target="_blank" rel="noopener noreferrer">
                    <MonitorPlay className="h-3.5 w-3.5" /> Display
                  </a>
                </Button>
              )}
              {canShare && (
                <Button variant="outline" size="sm" onClick={() => setDialog("displays")} title="Wall screens">
                  <Tv className="h-3.5 w-3.5" /> Display links
                </Button>
              )}
              {current && (
                <Button variant="outline" size="sm" onClick={copy} title="Make your own copy">
                  <Copy className="h-3.5 w-3.5" /> Copy
                </Button>
              )}
              {current?.can_edit && (
                <Button variant="outline" size="sm" onClick={() => setDialog("rename")}>
                  Rename
                </Button>
              )}
              {current && canShare && (
                <Button variant="outline" size="sm" onClick={toggleShare}>
                  <Share2 className="h-3.5 w-3.5" /> {current.shared ? "Stop sharing" : "Share"}
                </Button>
              )}
              {current?.can_edit && (
                <Button variant="outline" size="sm" onClick={() => setDialog("delete")} aria-label="Delete dashboard">
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              )}
              <Button size="sm" onClick={() => setDialog("new")}>
                <Plus className="h-3.5 w-3.5" /> New dashboard
              </Button>
            </>
          )}
          <UserMenu />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto p-6">
        {error && (
          <div role="alert" className="mb-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}

        {list === null ? (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-12">
            <Skeleton className="h-48 lg:col-span-4" />
            <Skeleton className="h-48 lg:col-span-4" />
            <Skeleton className="h-48 lg:col-span-4" />
          </div>
        ) : list.length === 0 ? (
          <Card className="mx-auto max-w-lg">
            <CardContent className="space-y-3 p-8 text-center">
              <LayoutDashboard className="mx-auto h-8 w-8 text-muted-foreground" />
              <div className="font-medium">No dashboards yet</div>
              <p className="text-sm text-muted-foreground">
                Build your own view of the estate from widgets: alerts, rooms that need attention, Room Readiness
                results, room usage and more. Drag them into the order you want.
              </p>
              <Button onClick={() => setDialog("new")}>
                <Plus className="h-3.5 w-3.5" /> New dashboard
              </Button>
            </CardContent>
          </Card>
        ) : !current ? null : widgets.length === 0 ? (
          <Card className="mx-auto max-w-lg">
            <CardContent className="space-y-3 p-8 text-center">
              <div className="font-medium">This dashboard is empty</div>
              {editing ? (
                <Button onClick={() => setPickerOpen(true)}>
                  <Plus className="h-3.5 w-3.5" /> Add widget
                </Button>
              ) : current.can_edit ? (
                <Button onClick={startEdit}>
                  <Pencil className="h-3.5 w-3.5" /> Edit to add widgets
                </Button>
              ) : null}
            </CardContent>
          </Card>
        ) : (
          <DashboardGrid
            widgets={widgets}
            editing={editing}
            onChange={setDraft}
            onSettings={setSettingsFor}
          />
        )}
      </div>

      <Modal open={pickerOpen} onClose={() => setPickerOpen(false)} title="Add a widget">
        <WidgetPicker
          onPick={(type) => {
            const def = WIDGET_BY_TYPE[type];
            const w: DashboardWidget = { id: newId(), type, size: def.defaultSize, config: {} };
            setDraft((d) => [...d, w]);
            setPickerOpen(false);
            if (def.fields.some((f) => f.kind === "room" || f.kind === "device")) setSettingsFor(w.id);
          }}
        />
      </Modal>

      <Modal
        open={settingsFor !== null}
        onClose={() => setSettingsFor(null)}
        title="Widget settings"
        wide={false}
      >
        {settingsFor && draft.find((w) => w.id === settingsFor) && (
          <WidgetSettings
            widget={draft.find((w) => w.id === settingsFor)!}
            onDone={(next) => {
              setDraft((d) => d.map((w) => (w.id === next.id ? next : w)));
              setSettingsFor(null);
            }}
          />
        )}
      </Modal>

      <Modal open={dialog === "new"} onClose={() => setDialog(null)} title="New dashboard" wide={false}>
        <NewDashboard
          canShare={canShare}
          onCreated={async (d) => {
            setDialog(null);
            await loadList();
            select(d.id);
          }}
        />
      </Modal>

      <Modal open={dialog === "displays"} onClose={() => setDialog(null)} title="Display links" wide={false}>
        <DisplayLinks shared={shared} />
      </Modal>

      <Modal open={dialog === "rename"} onClose={() => setDialog(null)} title="Rename dashboard" wide={false}>
        {current && (
          <RenameDashboard
            dashboard={current}
            onDone={(d) => {
              setCurrent(d);
              setDialog(null);
              void loadList();
            }}
          />
        )}
      </Modal>

      <Modal open={dialog === "delete"} onClose={() => setDialog(null)} title="Delete dashboard" wide={false}>
        {current && (
          <div className="space-y-4 text-sm">
            <p>
              Delete <strong>{current.name}</strong>?
              {current.shared && " It's shared, so it will disappear for everyone."} This can&apos;t be undone.
            </p>
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setDialog(null)}>Cancel</Button>
              <Button
                size="sm"
                variant="destructive"
                onClick={async () => {
                  try {
                    await api.deleteDashboard(current.id);
                    setDialog(null);
                    setCurrent(null);
                    const ds = await loadList();
                    if (ds[0]) select(ds[0].id);
                    else router.replace("/dashboards");
                  } catch (e) {
                    setError(errorText(e));
                    setDialog(null);
                  }
                }}
              >
                Delete
              </Button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}

// ── Picker, settings, dialogs ────────────────────────────────────────────

function WidgetPicker({ onPick }: { onPick: (type: string) => void }) {
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
      {WIDGETS.map((w) => (
        <button
          key={w.type}
          type="button"
          onClick={() => onPick(w.type)}
          className="flex items-start gap-3 rounded-md border p-3 text-left hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <w.icon className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground" />
          <span>
            <span className="block text-sm font-medium">{w.title}</span>
            <span className="block text-xs text-muted-foreground">{w.description}</span>
          </span>
        </button>
      ))}
    </div>
  );
}

function WidgetSettings({ widget, onDone }: { widget: DashboardWidget; onDone: (w: DashboardWidget) => void }) {
  const def = WIDGET_BY_TYPE[widget.type];
  const [config, setConfig] = useState<WidgetConfig>({ ...(widget.config ?? {}) });
  const needsPlaces = def?.fields.some((f) => f.kind !== "select" && f.kind !== "text" && f.kind !== "number");
  const [buildings, setBuildings] = useState<NamedRow[]>([]);
  const [regions, setRegions] = useState<NamedRow[]>([]);
  const [rooms, setRooms] = useState<NamedRow[]>([]);
  const [roomTags, setRoomTags] = useState<RoomTag[]>([]);
  const [devices, setDevices] = useState<DeviceSummary[]>([]);
  useEffect(() => {
    if (!needsPlaces) return;
    const ctrl = new AbortController();
    api.listBuildings(ctrl.signal).then(setBuildings).catch(() => {});
    api.listRegions(ctrl.signal).then(setRegions).catch(() => {});
    api.listRooms(ctrl.signal).then(setRooms).catch(() => {});
    api.listRoomTags(ctrl.signal).then(setRoomTags).catch(() => {});
    api.listDevices(ctrl.signal).then(setDevices).catch(() => {});
    return () => ctrl.abort();
  }, [needsPlaces]);
  if (!def) return null;

  const field = (f: ConfigField) => {
    const value = typeof config[f.key] === "string" ? (config[f.key] as string) : "";
    const set = (v: string, extra?: WidgetConfig) => setConfig((c) => ({ ...c, [f.key]: v, ...extra }));
    if (f.kind === "text") {
      return (
        <div key={f.key} className="space-y-1.5">
          <label className={labelClass} htmlFor={`wf-${f.key}`}>{f.label}</label>
          {f.multiline ? (
            <textarea
              id={`wf-${f.key}`}
              className={cn(inputClass, "h-28 py-2")}
              value={value}
              maxLength={f.maxLength}
              placeholder={f.placeholder}
              onChange={(e) => set(e.target.value)}
            />
          ) : (
            <input
              id={`wf-${f.key}`}
              className={inputClass}
              value={value}
              maxLength={f.maxLength}
              placeholder={f.placeholder}
              onChange={(e) => set(e.target.value)}
            />
          )}
        </div>
      );
    }
    if (f.kind === "number") {
      const n = config[f.key];
      return (
        <div key={f.key} className="space-y-1.5">
          <label className={labelClass} htmlFor={`wf-${f.key}`}>{f.label}</label>
          <input
            id={`wf-${f.key}`}
            type="number"
            inputMode="numeric"
            className={inputClass}
            min={f.min}
            max={f.max}
            value={typeof n === "number" || typeof n === "string" ? String(n) : String(f.default)}
            onChange={(e) => setConfig((c) => ({ ...c, [f.key]: Number(e.target.value) }))}
          />
        </div>
      );
    }
    let options: { value: string; label: string }[];
    if (f.kind === "select") {
      options = f.options;
    } else if (f.kind === "region") {
      options = [...regions].sort((a, b) => a.name.localeCompare(b.name)).map((r) => ({ value: r.name, label: r.name }));
    } else if (f.kind === "building") {
      options = [...buildings].sort((a, b) => a.name.localeCompare(b.name)).map((b) => ({ value: b.name, label: b.name }));
    } else if (f.kind === "room") {
      options = [...rooms].sort((a, b) => a.name.localeCompare(b.name)).map((r) => ({ value: r.id, label: r.name }));
    } else if (f.kind === "roomtag") {
      options = roomTags.map((t) => ({ value: t.id, label: t.name }));
    } else {
      options = [...devices]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((d) => ({ value: d.id, label: d.location ? `${d.name} · ${d.location}` : d.name }));
    }
    return (
      <div key={f.key} className="space-y-1.5">
        <label className={labelClass} htmlFor={`wf-${f.key}`}>{f.label}</label>
        <select
          id={`wf-${f.key}`}
          className={inputClass}
          value={value || (f.kind === "select" ? f.default : "")}
          onChange={(e) => {
            const v = e.target.value;
            // Room and device widgets keep the name for their heading.
            if (f.kind === "room") set(v, { room_name: rooms.find((r) => r.id === v)?.name ?? "" });
            else if (f.kind === "device") set(v, { device_name: devices.find((d) => d.id === v)?.name ?? "" });
            else if (f.kind === "roomtag") set(v, { tag_name: roomTags.find((t) => t.id === v)?.name ?? "" });
            else set(v);
          }}
        >
          {f.kind !== "select" && <option value="">{f.optional ? "All" : "Choose…"}</option>}
          {options.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
      </div>
    );
  };

  return (
    <div className="space-y-4 text-sm">
      <p className="text-muted-foreground">{def.description}</p>
      {def.fields.map(field)}
      <div className="flex justify-end">
        <Button size="sm" onClick={() => onDone({ ...widget, config })}>Done</Button>
      </div>
    </div>
  );
}

function NewDashboard({ canShare, onCreated }: { canShare: boolean; onCreated: (d: Dashboard) => void }) {
  const [name, setName] = useState("");
  const [shared, setShared] = useState(false);
  const [starter, setStarter] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const d = await api.createDashboard({
        name: name.trim(),
        shared,
        layout: { widgets: starter ? STARTER.map((w) => ({ ...w, id: newId() })) : [] },
      });
      onCreated(d);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="space-y-4 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) void create();
      }}
    >
      <div className="space-y-1.5">
        <label className={labelClass} htmlFor="nd-name">Name</label>
        <input id="nd-name" className={inputClass} value={name} onChange={(e) => setName(e.target.value)} maxLength={80} autoFocus />
      </div>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={starter} onChange={(e) => setStarter(e.target.checked)} />
        Start with suggested widgets
      </label>
      {canShare && (
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-0.5" checked={shared} onChange={(e) => setShared(e.target.checked)} />
          <span>
            Share with everyone in the organisation
            <span className="block text-xs text-muted-foreground">
              Everyone can view and copy it; each person only sees the devices their access allows.
            </span>
          </span>
        </label>
      )}
      {error && <div role="alert" className="text-xs [color:hsl(var(--destructive))]">{error}</div>}
      <div className="flex justify-end">
        <Button size="sm" type="submit" disabled={busy || !name.trim()}>{busy ? "Creating…" : "Create"}</Button>
      </div>
    </form>
  );
}

function RenameDashboard({ dashboard, onDone }: { dashboard: Dashboard; onDone: (d: Dashboard) => void }) {
  const [name, setName] = useState(dashboard.name);
  const [error, setError] = useState<string | null>(null);
  return (
    <form
      className="space-y-4 text-sm"
      onSubmit={async (e) => {
        e.preventDefault();
        try {
          onDone(await api.updateDashboard(dashboard.id, { name: name.trim() }));
        } catch (err) {
          setError(errorText(err));
        }
      }}
    >
      <div className="space-y-1.5">
        <label className={labelClass} htmlFor="rd-name">Name</label>
        <input id="rd-name" className={inputClass} value={name} onChange={(e) => setName(e.target.value)} maxLength={80} autoFocus />
      </div>
      {error && <div role="alert" className="text-xs [color:hsl(var(--destructive))]">{error}</div>}
      <div className="flex justify-end">
        <Button size="sm" type="submit" disabled={!name.trim()}>Save</Button>
      </div>
    </form>
  );
}
