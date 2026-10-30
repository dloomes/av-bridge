"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  FileCode2,
  Loader2,
  Play,
  RotateCcw,
  Settings2,
  Tags,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { api } from "@/lib/api";
import type { RoomTag } from "@/lib/api";
import type { NamedRow } from "@/lib/types";
import { RoomTagChips, TagChip, TagFilter } from "@/components/room-tags";
import type {
  NightlyRoutineRow,
  ReadinessRoomRow,
  ReadinessTagRow,
  ReadinessSchedule,
  UpdateReadinessRoomBody,
  UpdateReadinessScheduleBody,
} from "@/lib/api";
import {
  DAY_BUTTONS,
  duringPowerOff,
  isFutureDate,
  summariseDays,
  toggleDay,
} from "./shared";

// Readiness checks — the routine half of Room Readiness, on its own
// schedule, separate from the power schedule. A default (routine, time,
// days) for every room; a room tag's, for rooms with that tag; and a
// room's own where it needs different checks or a different time. Each
// field resolves room → tag → default (migration 0071).

const inputClass =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50";

function DayButtons({
  days,
  onChange,
  disabled,
}: {
  days: number[];
  onChange: (days: number[]) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-wrap gap-1" role="group" aria-label="Days the check runs">
      {DAY_BUTTONS.map((d) => {
        const active = days.includes(d.iso);
        return (
          <button
            key={d.iso}
            type="button"
            aria-pressed={active}
            aria-label={d.full}
            disabled={disabled}
            onClick={() => onChange(toggleDay(days, d.iso))}
            className={`w-10 rounded-md border px-2 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 ${
              active
                ? "bg-primary text-primary-foreground border-primary"
                : "bg-background hover:bg-accent"
            }`}
          >
            {d.label}
          </button>
        );
      })}
    </div>
  );
}

function RoutineSelect({
  id,
  value,
  routines,
  onChange,
  disabled,
  allowNone,
}: {
  id?: string;
  value: string;
  routines: NightlyRoutineRow[];
  onChange: (id: string) => void;
  disabled?: boolean;
  allowNone?: boolean;
}) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      className={inputClass}
    >
      {allowNone && (
        <option value="">
          {routines.length === 0 ? "No routines yet" : "— choose a routine —"}
        </option>
      )}
      {routines.map((r) => (
        <option key={r.id} value={r.id}>
          {r.name} ({r.step_count} {r.step_count === 1 ? "step" : "steps"})
        </option>
      ))}
    </select>
  );
}

export function ReadinessPanel({ canManage }: { canManage: boolean }) {
  const { toast } = useToast();
  const router = useRouter();

  const [loaded, setLoaded] = useState<ReadinessSchedule | null>(null);
  const [rooms, setRooms] = useState<ReadinessRoomRow[] | null>(null);
  const [routines, setRoutines] = useState<NightlyRoutineRow[]>([]);
  const [error, setError] = useState<string | null>(null);

  const [enabled, setEnabled] = useState(false);
  const [routineID, setRoutineID] = useState("");
  const [runTime, setRunTime] = useState("07:45");
  const [days, setDays] = useState<number[]>([1, 2, 3, 4, 5]);
  const [saving, setSaving] = useState(false);

  const [editing, setEditing] = useState<ReadinessRoomRow | null>(null);
  const [busyRoom, setBusyRoom] = useState<string | null>(null);
  // Room tags, for the tag chips and filter on the rooms list.
  const [tags, setTags] = useState<RoomTag[]>([]);
  const [tagsByRoom, setTagsByRoom] = useState<Map<string, string[]>>(new Map());
  const [tagFilter, setTagFilter] = useState("");
  const [readinessTags, setReadinessTags] = useState<ReadinessTagRow[]>([]);
  const [editingTag, setEditingTag] = useState<ReadinessTagRow | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [s, rs, rt, tg, roomList, rtags] = await Promise.all([
        api.getReadinessSchedule(signal),
        api.listReadinessRooms(signal),
        api.listNightlyRoutines(signal),
        api.listRoomTags(signal).catch(() => [] as RoomTag[]),
        api.listRooms(signal).catch(() => [] as NamedRow[]),
        api.listReadinessTags(signal).catch(() => [] as ReadinessTagRow[]),
      ]);
      if (signal?.aborted) return;
      setLoaded(s);
      setEnabled(s.enabled);
      setRoutineID(s.test_routine_id ?? "");
      setRunTime(s.run_time);
      setDays([...s.days_of_week].sort((a, b) => a - b));
      setRooms(rs);
      setRoutines(rt);
      setTags(tg);
      setTagsByRoom(new Map(roomList.map((r) => [r.id, r.tag_ids ?? []])));
      setReadinessTags(rtags);
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

  const dirty =
    loaded !== null &&
    (enabled !== loaded.enabled ||
      routineID !== (loaded.test_routine_id ?? "") ||
      runTime !== loaded.run_time ||
      JSON.stringify(days) !==
        JSON.stringify([...loaded.days_of_week].sort((a, b) => a - b)));

  const handleSave = async () => {
    if (!loaded) return;
    const body: UpdateReadinessScheduleBody = {};
    if (enabled !== loaded.enabled) body.enabled = enabled;
    if (routineID !== (loaded.test_routine_id ?? "")) body.test_routine_id = routineID;
    if (runTime !== loaded.run_time) body.run_time = runTime;
    if (
      JSON.stringify(days) !==
      JSON.stringify([...loaded.days_of_week].sort((a, b) => a - b))
    ) {
      body.days_of_week = days;
    }
    setSaving(true);
    try {
      await api.updateReadinessSchedule(body);
      await load();
      toast({ title: "Readiness checks saved", variant: "success" });
    } catch (e) {
      toast({
        title: "Save failed",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setSaving(false);
    }
  };

  // Run now: the room's routine straight away, no power cycle. Opens the
  // run so the steps can be watched as they complete.
  const handleRunNow = async (row: ReadinessRoomRow) => {
    setBusyRoom(row.room_id);
    try {
      const res = await api.runRoutineNow(row.room_id);
      toast({
        title: `Started the check on "${row.room_name}"`,
        variant: "success",
      });
      router.push(`/nightly/runs/${res.run_id}`);
    } catch (e) {
      toast({
        title: "Couldn't start the check",
        description: (e as Error).message,
        variant: "destructive",
      });
      setBusyRoom(null);
    }
  };

  const handleReset = async (row: ReadinessRoomRow) => {
    setBusyRoom(row.room_id);
    try {
      await api.resetReadinessRoom(row.room_id);
      await load();
      toast({
        title: `Reset "${row.room_name}"`,
        description: "The room now uses the default readiness check.",
        variant: "success",
      });
    } catch (e) {
      toast({
        title: "Reset failed",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setBusyRoom(null);
    }
  };

  const groups = useMemo(() => {
    const map = new Map<string, { label: string; rows: ReadinessRoomRow[] }>();
    for (const r of rooms ?? []) {
      if (tagFilter && !tagsByRoom.get(r.room_id)?.includes(tagFilter)) continue;
      const label = [r.region_name, r.location_name, r.building_name]
        .filter(Boolean)
        .join(" · ");
      const key = r.building_id || "unassigned";
      if (!map.has(key)) map.set(key, { label: label || "Unassigned", rows: [] });
      map.get(key)!.rows.push(r);
    }
    return Array.from(map.values());
  }, [rooms, tagFilter, tagsByRoom]);

  if (error) {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
        {error}
      </div>
    );
  }
  if (!loaded || !rooms) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />
        Loading readiness checks…
      </div>
    );
  }

  const inputsDisabled = !canManage || saving;

  return (
    <div className="space-y-6">
      {/* ── Default check ─────────────────────────────────────────────── */}
      <Card>
        <CardContent className="p-6 space-y-5">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-sm font-semibold">Readiness checks</h2>
              <p className="mt-1 text-xs text-muted-foreground">
                Run a test routine in every room at a set time, to prove it&rsquo;s
                ready before the day starts. Separate from the power schedule:
                a room can have either, both or neither.
              </p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={enabled}
              aria-label="Readiness checks on"
              disabled={inputsDisabled}
              onClick={() => setEnabled((v) => !v)}
              className={`shrink-0 relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${
                enabled ? "bg-[color:hsl(var(--success))]" : "bg-muted"
              }`}
            >
              <span
                aria-hidden="true"
                className={`inline-block h-5 w-5 rounded-full bg-white shadow transition-transform ${
                  enabled ? "translate-x-5" : "translate-x-0.5"
                }`}
              />
            </button>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1 sm:col-span-2">
              <div className="flex items-center justify-between">
                <label htmlFor="readiness-routine" className="text-xs font-medium">
                  Default routine
                </label>
                <Link
                  href="/nightly/routines"
                  className="text-xs text-primary hover:underline"
                >
                  Manage routines →
                </Link>
              </div>
              <RoutineSelect
                id="readiness-routine"
                value={routineID}
                routines={routines}
                onChange={setRoutineID}
                disabled={inputsDisabled}
                allowNone
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="readiness-time" className="text-xs font-medium">
                Run at
              </label>
              <input
                id="readiness-time"
                type="time"
                value={runTime}
                onChange={(e) => setRunTime(e.target.value)}
                disabled={inputsDisabled}
                className={inputClass}
              />
              <p className="text-xs text-muted-foreground">
                In {loaded.timezone}. If a room&rsquo;s power cycle is still running,
                its check waits for it to finish.
              </p>
            </div>
            <div className="space-y-1">
              <span className="text-xs font-medium">Days</span>
              <DayButtons days={days} onChange={setDays} disabled={inputsDisabled} />
            </div>
          </div>

          {canManage && (
            <div className="flex items-center justify-end gap-2 border-t pt-4">
              <span className="mr-auto text-xs text-muted-foreground">
                {dirty ? "You have unsaved changes." : ""}
              </span>
              <Button size="sm" onClick={handleSave} disabled={!dirty || saving}>
                {saving && <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin" />}
                Save changes
              </Button>
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── By room tag ──────────────────────────────────────────────── */}
      {readinessTags.length > 0 && (
        <ReadinessTagsCard
          tags={readinessTags}
          canManage={canManage}
          onEdit={setEditingTag}
          onChanged={() => void load()}
        />
      )}

      {/* ── Rooms ─────────────────────────────────────────────────────── */}
      <Card>
        <CardContent className="p-6 space-y-4">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-sm font-semibold">Rooms</h2>
              <p className="mt-1 text-xs text-muted-foreground">
                Each room uses its tag&apos;s check, or the default, unless you
                customise it. Excluded rooms (set on the Power schedule tab) are skipped.
              </p>
            </div>
            <div className="flex shrink-0 flex-wrap items-center gap-3">
              <TagFilter tags={tags} value={tagFilter} onChange={setTagFilter} />
              <Link
                href="/nightly/runs"
                className="text-xs text-primary hover:underline"
              >
                View runs →
              </Link>
            </div>
          </div>
          {groups.length === 0 && (
            <div className="text-sm text-muted-foreground">
              {tagFilter ? "No rooms have this tag." : "No rooms yet."}
            </div>
          )}
          {groups.map((g) => (
            <div key={g.label} className="space-y-2">
              <div className="text-xs font-medium text-muted-foreground">{g.label}</div>
              <div className="overflow-x-auto rounded-md border relative">
                <table className="w-full min-w-[560px] text-sm">
                  <thead>
                    <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wide text-muted-foreground">
                      <th scope="col" className="px-3 py-2 font-medium">Room</th>
                      <th scope="col" className="px-3 py-2 font-medium">Check</th>
                      <th scope="col" className="px-3 py-2 font-medium">Status</th>
                      <th scope="col" className="px-3 py-2 font-medium text-right">
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {g.rows.map((r) => {
                      const excluded = r.excluded_until && isFutureDate(r.excluded_until);
                      const clash =
                        r.power_enabled &&
                        r.power_off_time &&
                        r.power_on_time &&
                        r.effective_test_routine_id &&
                        duringPowerOff(r.effective_run_time, r.power_off_time, r.power_on_time);
                      return (
                        <tr
                          key={r.room_id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-3 py-2.5 font-medium">
                            <div>{r.room_name}</div>
                            <div className="mt-1">
                              <RoomTagChips tagIDs={tagsByRoom.get(r.room_id)} tags={tags} />
                            </div>
                          </td>
                          <td className="px-3 py-2.5 text-xs text-muted-foreground">
                            {r.effective_test_routine_name ? (
                              <>
                                <div className="flex items-center gap-1">
                                  <FileCode2 aria-hidden="true" className="h-3 w-3 shrink-0" />
                                  <span className={r.override_test_routine_id ? "text-foreground" : undefined}>
                                    {r.effective_test_routine_name}
                                  </span>
                                </div>
                                <div className="mt-0.5">
                                  <span className="font-mono">{r.effective_run_time}</span>
                                  <span className="ml-2">{summariseDays(r.effective_days_of_week)}</span>
                                </div>
                              </>
                            ) : (
                              "Not checked"
                            )}
                          </td>
                          <td className="px-3 py-2.5">
                            <div className="flex flex-wrap items-center gap-1.5">
                              {excluded ? (
                                <Badge variant="warning">Excluded until {r.excluded_until}</Badge>
                              ) : r.has_override ? (
                                <Badge variant="secondary">Customised</Badge>
                              ) : r.tag_name ? (
                                <span className="text-xs text-muted-foreground">From tag {r.tag_name}</span>
                              ) : (
                                <span className="text-xs text-muted-foreground">Inherits default</span>
                              )}
                              {clash && (
                                <Badge
                                  variant="warning"
                                  className="gap-1"
                                  title={`Switched off ${r.power_off_time}–${r.power_on_time}: the check waits for the power cycle, but a test while the room is off can't pass.`}
                                >
                                  <AlertTriangle aria-hidden="true" className="h-3 w-3" />
                                  Room switched off then
                                </Badge>
                              )}
                            </div>
                          </td>
                          <td className="px-3 py-2.5">
                            {canManage && (
                              <div className="flex items-center justify-end gap-1">
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-8"
                                  disabled={busyRoom === r.room_id || !r.effective_test_routine_id}
                                  onClick={() => handleRunNow(r)}
                                  aria-label={`Run the check now on ${r.room_name}`}
                                  title="Run this room's check now"
                                >
                                  {busyRoom === r.room_id ? (
                                    <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin" />
                                  ) : (
                                    <Play aria-hidden="true" className="h-3.5 w-3.5" />
                                  )}
                                  Run now
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-8"
                                  onClick={() => setEditing(r)}
                                  aria-label={`Customise the check for ${r.room_name}`}
                                >
                                  <Settings2 aria-hidden="true" className="h-3.5 w-3.5" />
                                  {r.has_override ? "Edit" : "Customise"}
                                </Button>
                                {r.has_override && (
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="h-8"
                                    disabled={busyRoom === r.room_id}
                                    onClick={() => handleReset(r)}
                                    aria-label={`Reset ${r.room_name} to the default check`}
                                  >
                                    <RotateCcw aria-hidden="true" className="h-3.5 w-3.5" />
                                    Reset
                                  </Button>
                                )}
                              </div>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      {editing && (
        <ReadinessCheckModal
          title={`Readiness check for "${editing.room_name}"`}
          what="this room's check"
          own={{
            routineID: editing.override_test_routine_id,
            runTime: editing.override_run_time,
            days: editing.override_days_of_week,
          }}
          inherits={{
            from: editing.tag_name ? `the ${editing.tag_name} tag` : "the default",
            routineID: editing.inherited_test_routine_id,
            routineName: editing.inherited_test_routine_name,
            runTime: editing.inherited_run_time,
            days: editing.inherited_days_of_week,
          }}
          routines={routines}
          save={(body) => api.updateReadinessRoom(editing.room_id, body)}
          savedMessage={`Saved the check for "${editing.room_name}"`}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
      {editingTag && (
        <ReadinessCheckModal
          title={`Readiness check for rooms tagged ${editingTag.name}`}
          what="this tag's check"
          own={{
            routineID: editingTag.test_routine_id,
            runTime: editingTag.run_time,
            days: editingTag.days_of_week,
          }}
          inherits={{
            from: "the default",
            routineID: loaded.test_routine_id,
            routineName: loaded.test_routine_name,
            runTime: loaded.run_time,
            days: loaded.days_of_week,
          }}
          routines={routines}
          save={(body) => api.updateReadinessTag(editingTag.tag_id, body)}
          savedMessage={`Saved the check for ${editingTag.name}`}
          onClose={() => setEditingTag(null)}
          onSaved={() => {
            setEditingTag(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

// A room's or a tag's own check: tick Customise on a field to set it;
// leave it unticked to inherit (a room from its tag or the default, a tag
// from the default).
function ReadinessCheckModal({
  title,
  what,
  own,
  inherits,
  routines,
  save,
  savedMessage,
  onClose,
  onSaved,
}: {
  title: string;
  what: string; // "this room's check", for the discard prompt
  own: { routineID?: string; runTime?: string; days?: number[] };
  inherits: { from: string; routineID?: string; routineName?: string; runTime: string; days: number[] };
  routines: NightlyRoutineRow[];
  save: (body: UpdateReadinessRoomBody) => Promise<void>;
  savedMessage: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { toast } = useToast();
  const [customRoutine, setCustomRoutine] = useState(own.routineID !== undefined);
  const [customTime, setCustomTime] = useState(own.runTime !== undefined);
  const [customDays, setCustomDays] = useState(own.days !== undefined);
  const [routineID, setRoutineID] = useState(
    own.routineID ?? inherits.routineID ?? routines[0]?.id ?? ""
  );
  const [runTime, setRunTime] = useState(own.runTime ?? inherits.runTime);
  const [days, setDays] = useState<number[]>(
    [...(own.days ?? inherits.days)].sort((a, b) => a - b)
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    const body: UpdateReadinessRoomBody = {
      test_routine_id: customRoutine && routineID !== "" ? routineID : null,
      run_time: customTime ? runTime : null,
      days_of_week: customDays ? days : null,
    };
    try {
      await save(body);
      toast({ title: savedMessage, variant: "success" });
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const row = (
    label: string,
    custom: boolean,
    toggle: () => void,
    inherit: string,
    editor: React.ReactNode
  ) => (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium">{label}</span>
        <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
          <input
            type="checkbox"
            checked={custom}
            onChange={toggle}
            className="h-3.5 w-3.5 rounded border-input"
          />
          Customise
        </label>
      </div>
      {custom ? (
        editor
      ) : (
        <div className="text-xs text-muted-foreground">
          Uses {inherits.from}: <span className="font-mono text-foreground">{inherit}</span>
        </div>
      )}
    </div>
  );

  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      wide={false}
      dirty
      dirtyPrompt={`Discard changes to ${what}?`}
    >
      <div className="space-y-5">
        {row(
          "Routine",
          customRoutine,
          () => setCustomRoutine((v) => !v),
          inherits.routineName ?? "none",
          routines.length === 0 ? (
            <div className="text-xs text-muted-foreground">
              No routines yet.{" "}
              <Link href="/nightly/routines" className="text-primary hover:underline">
                Create one
              </Link>{" "}
              first.
            </div>
          ) : (
            <RoutineSelect value={routineID} routines={routines} onChange={setRoutineID} disabled={saving} />
          )
        )}
        {row(
          "Run at",
          customTime,
          () => setCustomTime((v) => !v),
          inherits.runTime,
          <input
            type="time"
            value={runTime}
            onChange={(e) => setRunTime(e.target.value)}
            disabled={saving}
            className={inputClass}
            aria-label="Run at"
          />
        )}
        {row(
          "Days",
          customDays,
          () => setCustomDays((v) => !v),
          summariseDays(inherits.days),
          <DayButtons days={days} onChange={setDays} disabled={saving} />
        )}
        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}
        <div className="flex items-center justify-end gap-2 border-t pt-2">
          <Button variant="outline" size="sm" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving && <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin" />}
            Save
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// ReadinessTagsCard: a check per room tag, for every room with the tag
// that doesn't set its own. A room with several tags takes the first one
// here that has a check, so the order matters; move tags up and down.
function ReadinessTagsCard({
  tags,
  canManage,
  onEdit,
  onChanged,
}: {
  tags: ReadinessTagRow[];
  canManage: boolean;
  onEdit: (tag: ReadinessTagRow) => void;
  onChanged: () => void;
}) {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);

  const move = async (index: number, by: -1 | 1) => {
    const ids = tags.map((t) => t.tag_id);
    const [id] = ids.splice(index, 1);
    ids.splice(index + by, 0, id);
    setBusy(true);
    try {
      await api.setReadinessTagOrder(ids);
      onChanged();
    } catch (e) {
      toast({ title: "Couldn't reorder the tags", description: (e as Error).message, variant: "destructive" });
    } finally {
      setBusy(false);
    }
  };

  const reset = async (t: ReadinessTagRow) => {
    setBusy(true);
    try {
      await api.resetReadinessTag(t.tag_id);
      toast({ title: `Removed the check for ${t.name}`, variant: "success" });
      onChanged();
    } catch (e) {
      toast({ title: "Couldn't remove the check", description: (e as Error).message, variant: "destructive" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardContent className="p-6 space-y-4">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <Tags aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
            By room tag
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Give a tag its own check, and every room with that tag uses it instead of the default,
            unless the room is customised. A room with several tags uses the highest one in this list
            that has a check.
          </p>
        </div>
        <ol className="divide-y rounded-md border" role="list">
          {tags.map((t, i) => (
            <li key={t.tag_id} className="flex flex-wrap items-center gap-3 px-3 py-2.5">
              {canManage && (
                <span className="flex flex-col">
                  <button
                    type="button"
                    disabled={busy || i === 0}
                    onClick={() => move(i, -1)}
                    aria-label={`Move ${t.name} up`}
                    className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-30"
                  >
                    <ArrowUp className="h-3.5 w-3.5" />
                  </button>
                  <button
                    type="button"
                    disabled={busy || i === tags.length - 1}
                    onClick={() => move(i, 1)}
                    aria-label={`Move ${t.name} down`}
                    className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-30"
                  >
                    <ArrowDown className="h-3.5 w-3.5" />
                  </button>
                </span>
              )}
              <TagChip tag={{ name: t.name, colour: t.colour }} />
              <span className="text-[11px] text-muted-foreground">
                {t.room_count} room{t.room_count === 1 ? "" : "s"}
              </span>
              <span className="min-w-0 flex-1 text-xs text-muted-foreground">
                {t.configured ? (
                  <span className="text-foreground">
                    <FileCode2 aria-hidden="true" className="mr-1 inline h-3 w-3" />
                    {t.test_routine_name ?? "Default routine"}
                    <span className="ml-2 font-mono">{t.run_time ?? "default time"}</span>
                    <span className="ml-2">{t.days_of_week ? summariseDays(t.days_of_week) : "default days"}</span>
                  </span>
                ) : (
                  "Uses the default"
                )}
              </span>
              {canManage && (
                <span className="flex items-center gap-1">
                  <Button variant="ghost" size="sm" className="h-8" disabled={busy} onClick={() => onEdit(t)}>
                    <Settings2 aria-hidden="true" className="h-3.5 w-3.5" />
                    {t.configured ? "Edit" : "Set a check"}
                  </Button>
                  {t.configured && (
                    <Button variant="ghost" size="sm" className="h-8" disabled={busy} onClick={() => reset(t)}>
                      <RotateCcw aria-hidden="true" className="h-3.5 w-3.5" />
                      Remove
                    </Button>
                  )}
                </span>
              )}
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  );
}
