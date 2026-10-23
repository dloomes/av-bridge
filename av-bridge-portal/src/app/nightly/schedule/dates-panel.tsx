"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { CalendarDays, Loader2, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { api } from "@/lib/api";
import type { CreateScheduleDateBody, NightlyRoomRow, ScheduleDate } from "@/lib/api";
import { isFutureDate } from "./shared";

// One-off dates — days that differ from the weekly schedule. Closed: no
// power-on and no readiness check (a bank holiday, a closure). Open: the
// room is in use with its own times, even on a day the weekly schedule
// skips (a weekend sitting, a late finish). For one room or every room; a
// room's own date wins.

const inputClass =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50";

function formatDay(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y, m - 1, d).toLocaleDateString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}

function describe(d: ScheduleDate): string {
  if (d.closed) return "Closed";
  const on = d.power_on_time ?? "usual on";
  const off = d.power_off_time ?? "usual off";
  if (!d.power_on_time && !d.power_off_time) return "Open, usual times";
  return `Open ${on} – ${off}`;
}

export function DatesPanel({
  canManage,
  rooms,
}: {
  canManage: boolean;
  rooms: NightlyRoomRow[] | null;
}) {
  const { toast } = useToast();
  const [dates, setDates] = useState<ScheduleDate[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const ds = await api.listScheduleDates(signal);
      if (signal?.aborted) return;
      setDates(ds);
      setError(null);
    } catch (e) {
      if (!signal?.aborted) setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void load(ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  const upcoming = useMemo(
    () => (dates ?? []).filter((d) => isFutureDate(d.date)),
    [dates]
  );

  const remove = async (d: ScheduleDate) => {
    const what = `${formatDay(d.date)} (${d.room_name ?? "every room"})`;
    if (!window.confirm(`Remove ${what}? The weekly schedule applies again.`)) return;
    setRemoving(d.id);
    try {
      await api.deleteScheduleDate(d.id);
      await load();
      toast({ title: "Date removed", variant: "success" });
    } catch (e) {
      toast({
        title: "Couldn't remove the date",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setRemoving(null);
    }
  };

  return (
    <Card>
      <CardContent className="p-6 space-y-4">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold">One-off dates</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Days that differ from the weekly schedule. A closed day has no
              power-on and no readiness check. An open day runs even when the
              weekly schedule doesn&rsquo;t (a weekend sitting), with its own
              times if you set them (a late finish). A room&rsquo;s own date
              wins over one for every room.
            </p>
          </div>
          {canManage && (
            <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
              <Plus aria-hidden="true" className="h-4 w-4 mr-1" />
              Add a date
            </Button>
          )}
        </div>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}
        {dates === null && !error && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading dates…
          </div>
        )}
        {dates !== null && upcoming.length === 0 && (
          <div className="flex items-center gap-2 rounded-md border border-dashed px-3 py-4 text-sm text-muted-foreground">
            <CalendarDays aria-hidden="true" className="h-4 w-4 shrink-0" />
            No dates coming up. Every room follows the weekly schedule.
          </div>
        )}
        {upcoming.length > 0 && (
          <ul className="divide-y rounded-md border">
            {upcoming.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <span className="font-medium tabular-nums">{formatDay(d.date)}</span>
                    <span className="text-muted-foreground">
                      {d.room_name ?? "Every room"}
                    </span>
                  </div>
                  {d.note && (
                    <div className="mt-0.5 text-xs text-muted-foreground break-words">{d.note}</div>
                  )}
                </div>
                <Badge variant={d.closed ? "secondary" : "outline"}>{describe(d)}</Badge>
                {canManage && (
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Remove ${formatDay(d.date)}`}
                    disabled={removing === d.id}
                    onClick={() => void remove(d)}
                  >
                    {removing === d.id ? (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    ) : (
                      <Trash2 aria-hidden="true" className="h-4 w-4" />
                    )}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>

      {adding && (
        <AddDateModal
          rooms={rooms ?? []}
          onClose={() => setAdding(false)}
          onAdded={async () => {
            setAdding(false);
            await load();
            toast({ title: "Date added", variant: "success" });
          }}
        />
      )}
    </Card>
  );
}

function AddDateModal({
  rooms,
  onClose,
  onAdded,
}: {
  rooms: NightlyRoomRow[];
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  const [date, setDate] = useState("");
  const [roomID, setRoomID] = useState("");
  const [closed, setClosed] = useState(true);
  const [on, setOn] = useState("");
  const [off, setOff] = useState("");
  const [note, setNote] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Rooms grouped by building for the picker.
  const groups = useMemo(() => {
    const m = new Map<string, NightlyRoomRow[]>();
    for (const r of rooms) {
      const k = r.building_name || "Other";
      m.set(k, [...(m.get(k) ?? []), r]);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [rooms]);

  const timesInvalid = !closed && on !== "" && off !== "" && on >= off;
  const dirty = date !== "" || roomID !== "" || note !== "" || on !== "" || off !== "";

  const save = async () => {
    if (!date) {
      setError("Pick a date.");
      return;
    }
    if (timesInvalid) {
      setError("Power on must be before power off.");
      return;
    }
    const body: CreateScheduleDateBody = { date, closed, note: note.trim() || undefined };
    if (roomID) body.room_id = roomID;
    if (!closed) {
      if (on) body.power_on_time = on;
      if (off) body.power_off_time = off;
    }
    setSaving(true);
    setError(null);
    try {
      await api.createScheduleDate(body);
      await onAdded();
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  return (
    <Modal open onClose={onClose} title="Add a date" wide={false} dirty={dirty}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1">
            <label htmlFor="sd_date" className="text-xs font-medium">
              Date
            </label>
            <input
              id="sd_date"
              type="date"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              disabled={saving}
              className={inputClass}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="sd_room" className="text-xs font-medium">
              Rooms
            </label>
            <select
              id="sd_room"
              value={roomID}
              onChange={(e) => setRoomID(e.target.value)}
              disabled={saving}
              className={inputClass}
            >
              <option value="">Every room</option>
              {groups.map(([building, rs]) => (
                <optgroup key={building} label={building}>
                  {rs.map((r) => (
                    <option key={r.room_id} value={r.room_id}>
                      {r.room_name}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
          </div>
        </div>

        <fieldset className="space-y-2">
          <legend className="text-xs font-medium">On this day</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { v: true, label: "Closed", hint: "No power-on, no readiness check" },
              { v: false, label: "Open", hint: "In use, even off the weekly days" },
            ].map((o) => (
              <label
                key={o.label}
                className={`flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm ${
                  closed === o.v ? "border-primary bg-primary/5" : ""
                }`}
              >
                <input
                  type="radio"
                  name="sd_kind"
                  checked={closed === o.v}
                  onChange={() => setClosed(o.v)}
                  disabled={saving}
                  className="mt-1"
                />
                <span>
                  <span className="font-medium">{o.label}</span>
                  <span className="block text-xs text-muted-foreground">{o.hint}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        {!closed && (
          <div className="space-y-1">
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-1">
                <label htmlFor="sd_on" className="text-xs font-medium">
                  Power on at
                </label>
                <input
                  id="sd_on"
                  type="time"
                  value={on}
                  onChange={(e) => setOn(e.target.value)}
                  disabled={saving}
                  className={inputClass}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="sd_off" className="text-xs font-medium">
                  Power off at
                </label>
                <input
                  id="sd_off"
                  type="time"
                  value={off}
                  onChange={(e) => setOff(e.target.value)}
                  disabled={saving}
                  className={inputClass}
                />
              </div>
            </div>
            <p className="text-xs text-muted-foreground">
              Leave a time blank to use the room&rsquo;s usual one.
            </p>
            {timesInvalid && (
              <p role="alert" className="text-xs [color:hsl(var(--destructive))]">
                Power on must be before power off.
              </p>
            )}
          </div>
        )}

        <div className="space-y-1">
          <label htmlFor="sd_note" className="text-xs font-medium">
            Note <span className="font-normal text-muted-foreground">(optional)</span>
          </label>
          <input
            id="sd_note"
            type="text"
            maxLength={200}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            disabled={saving}
            placeholder="Bank holiday, Saturday sitting…"
            className={inputClass}
          />
        </div>

        {error && (
          <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={() => void save()} disabled={saving || timesInvalid}>
            {saving && <Loader2 className="h-4 w-4 mr-1 animate-spin" />}
            Add date
          </Button>
        </div>
      </div>
    </Modal>
  );
}
