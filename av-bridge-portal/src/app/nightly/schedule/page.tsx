"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { CircleSlash, Loader2, Moon, RotateCcw, Settings2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import type {
  NightlyRoomRow,
  NightlySchedule,
  UpdateNightlyScheduleBody,
  UpdateRoomOverrideBody,
} from "@/lib/api";

// Room Readiness — customer-level schedule editor + per-room overrides.
//
// Slice 2C shipped the customer default; slice 2A adds the per-room
// override list + editor modal below. Recipe editor + runs heatmap still
// live in later slices; the placeholder card at the bottom lists what's
// coming next.

// Curated timezone options. The customer's stored value is added
// dynamically if it doesn't appear in this list, so bespoke IANA names set
// via API remain editable without silently dropping.
const COMMON_TIMEZONES = [
  "Europe/London",
  "Europe/Dublin",
  "Europe/Paris",
  "Europe/Berlin",
  "Europe/Amsterdam",
  "Europe/Madrid",
  "UTC",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
];

// ISO weekday order: 1 = Mon … 7 = Sun. Rendered left-to-right.
const DAY_BUTTONS: { iso: number; label: string; full: string }[] = [
  { iso: 1, label: "Mon", full: "Monday" },
  { iso: 2, label: "Tue", full: "Tuesday" },
  { iso: 3, label: "Wed", full: "Wednesday" },
  { iso: 4, label: "Thu", full: "Thursday" },
  { iso: 5, label: "Fri", full: "Friday" },
  { iso: 6, label: "Sat", full: "Saturday" },
  { iso: 7, label: "Sun", full: "Sunday" },
];

// Shorthand ISO-day-to-label lookup used in the compact per-row summary.
const DAY_LABELS_SHORT = ["", "Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"];

function summariseDays(days: number[]): string {
  const s = [...days].sort((a, b) => a - b);
  // Recognise Mon-Fri as the common weekday shorthand.
  if (
    s.length === 5 &&
    s[0] === 1 &&
    s[1] === 2 &&
    s[2] === 3 &&
    s[3] === 4 &&
    s[4] === 5
  ) {
    return "Weekdays";
  }
  if (s.length === 7) return "Every day";
  return s.map((d) => DAY_LABELS_SHORT[d]).join(" · ");
}

// isFutureDate — true if the YYYY-MM-DD string is today or later. Used to
// decide whether the excluded_until date is still active. Backend keeps
// past values but the UI treats them as "not excluded" for display.
function isFutureDate(iso: string): boolean {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const d = new Date(iso + "T00:00:00");
  return d.getTime() >= today.getTime();
}

export default function NightlySchedulePage() {
  const session = useSession();
  const canManage = isAdmin(session.user?.role) || !!session.user?.is_vendor;
  const { toast } = useToast();

  // ── Customer default state ─────────────────────────────────────────────

  const [loaded, setLoaded] = useState<NightlySchedule | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [enabled, setEnabled] = useState(false);
  const [powerOff, setPowerOff] = useState("19:00");
  const [powerOn, setPowerOn] = useState("07:30");
  const [days, setDays] = useState<number[]>([1, 2, 3, 4, 5]);
  const [timezone, setTimezone] = useState("Europe/London");
  const [helpdeskEmail, setHelpdeskEmail] = useState("");
  const [retentionDays, setRetentionDays] = useState(90);
  const [saving, setSaving] = useState(false);

  // ── Room overrides state ───────────────────────────────────────────────

  const [rooms, setRooms] = useState<NightlyRoomRow[] | null>(null);
  const [roomsError, setRoomsError] = useState<string | null>(null);
  const [editingRoom, setEditingRoom] = useState<NightlyRoomRow | null>(null);
  const [resettingRoomID, setResettingRoomID] = useState<string | null>(null);

  const loadCustomerSchedule = useCallback(async (signal?: AbortSignal) => {
    try {
      const s = await api.getNightlySchedule(signal);
      if (signal?.aborted) return;
      setLoaded(s);
      setEnabled(s.enabled);
      setPowerOff(s.power_off_time);
      setPowerOn(s.power_on_time);
      setDays([...s.days_of_week].sort((a, b) => a - b));
      setTimezone(s.timezone);
      setHelpdeskEmail(s.helpdesk_email ?? "");
      setRetentionDays(s.retention_days);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  const loadRooms = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listNightlyRooms(signal);
      if (signal?.aborted) return;
      setRooms(list);
      setRoomsError(null);
    } catch (e) {
      if (!signal?.aborted) setRoomsError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void loadCustomerSchedule(ctrl.signal);
    void loadRooms(ctrl.signal);
    return () => ctrl.abort();
  }, [loadCustomerSchedule, loadRooms]);

  const toggleDay = (iso: number) => {
    setDays((prev) =>
      prev.includes(iso)
        ? prev.filter((d) => d !== iso)
        : [...prev, iso].sort((a, b) => a - b)
    );
  };

  const dirty =
    loaded !== null &&
    (enabled !== loaded.enabled ||
      powerOff !== loaded.power_off_time ||
      powerOn !== loaded.power_on_time ||
      JSON.stringify(days) !==
        JSON.stringify([...loaded.days_of_week].sort((a, b) => a - b)) ||
      timezone !== loaded.timezone ||
      helpdeskEmail !== (loaded.helpdesk_email ?? "") ||
      retentionDays !== loaded.retention_days);

  const handleSave = async () => {
    if (!loaded) return;
    const body: UpdateNightlyScheduleBody = {};
    if (enabled !== loaded.enabled) body.enabled = enabled;
    if (powerOff !== loaded.power_off_time) body.power_off_time = powerOff;
    if (powerOn !== loaded.power_on_time) body.power_on_time = powerOn;
    if (
      JSON.stringify(days) !==
      JSON.stringify([...loaded.days_of_week].sort((a, b) => a - b))
    ) {
      body.days_of_week = days;
    }
    if (timezone !== loaded.timezone) body.timezone = timezone;
    if (helpdeskEmail !== (loaded.helpdesk_email ?? "")) {
      body.helpdesk_email = helpdeskEmail;
    }
    if (retentionDays !== loaded.retention_days) {
      body.retention_days = retentionDays;
    }
    if (Object.keys(body).length === 0) return;

    setSaving(true);
    try {
      await api.updateNightlySchedule(body);
      const fresh = await api.getNightlySchedule();
      setLoaded(fresh);
      // Room list's effective values depend on the customer default, so a
      // change here can shift every inheriting room. Refresh both.
      void loadRooms();
      toast({ title: "Schedule saved", variant: "success" });
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

  // Room override — reset via DELETE. Idempotent, so no confirmation prompt.
  const handleResetOverride = async (row: NightlyRoomRow) => {
    setResettingRoomID(row.room_id);
    try {
      await api.deleteRoomOverride(row.room_id);
      await loadRooms();
      toast({
        title: `Reset "${row.room_name}"`,
        description: "The room now inherits the customer default.",
        variant: "success",
      });
    } catch (e) {
      toast({
        title: "Reset failed",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setResettingRoomID(null);
    }
  };

  // If the stored timezone is a value not in the curated select, splice it
  // in so the user isn't forced to change it just to save something else.
  const timezoneOptions = COMMON_TIMEZONES.includes(timezone)
    ? COMMON_TIMEZONES
    : [timezone, ...COMMON_TIMEZONES];

  const inputsDisabled = !canManage || saving;

  // Group rooms by building — makes a table with dozens of rows scannable.
  // Region · location · building forms the section header; rooms within a
  // building sort alphabetically.
  const roomGroups = useMemo(() => {
    if (!rooms) return [];
    const map = new Map<
      string,
      { region: string; location: string; building: string; rows: NightlyRoomRow[] }
    >();
    for (const r of rooms) {
      const key = `${r.region_name ?? ""}|${r.location_name ?? ""}|${r.building_id}`;
      if (!map.has(key)) {
        map.set(key, {
          region: r.region_name ?? "",
          location: r.location_name ?? "",
          building: r.building_name,
          rows: [],
        });
      }
      map.get(key)!.rows.push(r);
    }
    return Array.from(map.values());
  }, [rooms]);

  return (
    <div className="flex flex-col h-screen">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-2.5">
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
            <Moon aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div>
            <h1 className="text-xl font-semibold leading-tight">
              Room Readiness
            </h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Nightly power-cycle and readiness testing for every room in the
              tenant.
            </p>
          </div>
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-3xl space-y-6">
          {loadError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </div>
          )}

          {loaded === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading schedule…
            </div>
          )}

          {loaded !== null && (
            <>
              {/* ── Master enable ─────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-4">
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0">
                      <h2 className="text-sm font-semibold">Nightly lifecycle</h2>
                      <p className="mt-1 text-xs text-muted-foreground">
                        When on, rooms in this tenant will be powered off and
                        powered back on to the schedule below. Off by default —
                        turn on when you're happy with the times and retention
                        window.
                      </p>
                    </div>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={enabled}
                      disabled={inputsDisabled}
                      onClick={() => setEnabled((v) => !v)}
                      className={`shrink-0 relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${
                        enabled
                          ? "bg-[color:hsl(var(--success))]"
                          : "bg-muted"
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
                  {enabled && (
                    <div className="rounded-md border border-[color:hsl(var(--success))]/30 bg-[color:hsl(var(--success))]/5 px-3 py-2 text-xs [color:hsl(var(--success))]">
                      Lifecycle is enabled. Rooms will power cycle to the
                      schedule below.
                    </div>
                  )}
                </CardContent>
              </Card>

              {/* ── Schedule ──────────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-6">
                  <h2 className="text-sm font-semibold">Schedule</h2>

                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-1">
                      <label
                        htmlFor="power_off_time"
                        className="text-xs font-medium"
                      >
                        Power off at
                      </label>
                      <input
                        id="power_off_time"
                        type="time"
                        value={powerOff}
                        onChange={(e) => setPowerOff(e.target.value)}
                        disabled={inputsDisabled}
                        className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                      />
                    </div>
                    <div className="space-y-1">
                      <label
                        htmlFor="power_on_time"
                        className="text-xs font-medium"
                      >
                        Power on at
                      </label>
                      <input
                        id="power_on_time"
                        type="time"
                        value={powerOn}
                        onChange={(e) => setPowerOn(e.target.value)}
                        disabled={inputsDisabled}
                        className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                      />
                    </div>
                  </div>

                  <div className="space-y-2">
                    <div className="text-xs font-medium">Days of week</div>
                    <div
                      role="group"
                      aria-label="Days of week the schedule runs"
                      className="flex flex-wrap gap-1"
                    >
                      {DAY_BUTTONS.map((d) => {
                        const active = days.includes(d.iso);
                        return (
                          <button
                            key={d.iso}
                            type="button"
                            aria-pressed={active}
                            aria-label={d.full}
                            disabled={inputsDisabled}
                            onClick={() => toggleDay(d.iso)}
                            className={`w-11 rounded-md border px-2 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 ${
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
                    <p className="text-xs text-muted-foreground">
                      Selected days run the lifecycle; the room stays on
                      overnight on any day not selected. Weekend selection is
                      typically off for corporate estates.
                    </p>
                  </div>

                  <div className="space-y-1 max-w-sm">
                    <label htmlFor="timezone" className="text-xs font-medium">
                      Timezone
                    </label>
                    <select
                      id="timezone"
                      value={timezone}
                      onChange={(e) => setTimezone(e.target.value)}
                      disabled={inputsDisabled}
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                    >
                      {timezoneOptions.map((tz) => (
                        <option key={tz} value={tz}>
                          {tz}
                        </option>
                      ))}
                    </select>
                    <p className="text-xs text-muted-foreground">
                      Schedules resolve in this timezone. Per-building
                      timezones are on the roadmap.
                    </p>
                  </div>
                </CardContent>
              </Card>

              {/* ── Notifications ─────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-4">
                  <h2 className="text-sm font-semibold">Notifications</h2>
                  <div className="space-y-1 max-w-md">
                    <label
                      htmlFor="helpdesk_email"
                      className="text-xs font-medium"
                    >
                      Helpdesk email
                    </label>
                    <input
                      id="helpdesk_email"
                      type="email"
                      value={helpdeskEmail}
                      onChange={(e) => setHelpdeskEmail(e.target.value)}
                      disabled={inputsDisabled}
                      placeholder="helpdesk@involve.vc"
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                    />
                    <p className="text-xs text-muted-foreground">
                      Lifecycle-failure alerts and the morning digest are
                      cc'd here on behalf of the customer, so Involve helpdesk
                      picks up incidents proactively. Leave blank to skip.
                    </p>
                  </div>
                </CardContent>
              </Card>

              {/* ── Retention ─────────────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-4">
                  <h2 className="text-sm font-semibold">History retention</h2>
                  <div className="space-y-1 max-w-xs">
                    <label
                      htmlFor="retention_days"
                      className="text-xs font-medium"
                    >
                      Keep detailed step results for (days)
                    </label>
                    <input
                      id="retention_days"
                      type="number"
                      min={30}
                      max={365}
                      value={retentionDays}
                      onChange={(e) =>
                        setRetentionDays(
                          Math.max(
                            30,
                            Math.min(365, parseInt(e.target.value, 10) || 30)
                          )
                        )
                      }
                      disabled={inputsDisabled}
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                    />
                    <p className="text-xs text-muted-foreground">
                      Minimum 30 days. Run-level pass/fail summaries are kept
                      forever — only the per-step debug details are trimmed
                      after this window.
                    </p>
                  </div>
                </CardContent>
              </Card>

              {/* ── Per-room overrides ─────────────────────────────────── */}
              <Card>
                <CardContent className="p-6 space-y-4">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <h2 className="text-sm font-semibold">
                        Per-room overrides
                      </h2>
                      <p className="mt-1 text-xs text-muted-foreground">
                        Rooms inherit the customer default above. Override
                        times, days, or exclude a room until a specific date
                        (for a fit-out, refurb, or bank holiday closure).
                      </p>
                    </div>
                  </div>

                  {roomsError && (
                    <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs [color:hsl(var(--destructive))]">
                      {roomsError}
                    </div>
                  )}

                  {rooms === null && !roomsError && (
                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                      <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      Loading rooms…
                    </div>
                  )}

                  {rooms !== null && rooms.length === 0 && (
                    <div className="text-xs text-muted-foreground italic">
                      No rooms visible under your scope.
                    </div>
                  )}

                  {roomGroups.length > 0 && (
                    <div className="space-y-4">
                      {roomGroups.map((g) => (
                        <div key={`${g.region}|${g.location}|${g.building}`}>
                          <div className="mb-1 flex items-baseline gap-1.5 text-xs text-muted-foreground">
                            {(g.region || g.location) && (
                              <span className="uppercase tracking-wide text-[10px]">
                                {[g.region, g.location].filter(Boolean).join(" · ")}
                              </span>
                            )}
                            <span className="font-medium text-foreground">
                              {g.building}
                            </span>
                          </div>
                          <div className="overflow-x-auto rounded-md border">
                            <table className="w-full min-w-[560px] text-sm">
                              <thead>
                                <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wide text-muted-foreground">
                                  <th scope="col" className="px-3 py-2 font-medium">
                                    Room
                                  </th>
                                  <th scope="col" className="px-3 py-2 font-medium">
                                    Schedule
                                  </th>
                                  <th scope="col" className="px-3 py-2 font-medium">
                                    Status
                                  </th>
                                  <th scope="col" className="px-3 py-2 font-medium text-right">
                                    <span className="sr-only">Actions</span>
                                  </th>
                                </tr>
                              </thead>
                              <tbody>
                                {g.rows.map((r) => {
                                  const excluded =
                                    r.excluded_until &&
                                    isFutureDate(r.excluded_until);
                                  return (
                                    <tr
                                      key={r.room_id}
                                      className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                                    >
                                      <td className="px-3 py-2.5">
                                        <div className="font-medium">
                                          {r.room_name}
                                        </div>
                                      </td>
                                      <td className="px-3 py-2.5 text-xs text-muted-foreground">
                                        <span className="font-mono">
                                          {r.effective_power_off_time}
                                          {" → "}
                                          {r.effective_power_on_time}
                                        </span>
                                        <span className="ml-2">
                                          {summariseDays(r.effective_days_of_week)}
                                        </span>
                                      </td>
                                      <td className="px-3 py-2.5">
                                        {excluded ? (
                                          <Badge variant="warning">
                                            Excluded until{" "}
                                            {r.excluded_until}
                                          </Badge>
                                        ) : r.has_override ? (
                                          <Badge variant="secondary">
                                            Customised
                                          </Badge>
                                        ) : (
                                          <span className="text-xs text-muted-foreground">
                                            Inherits default
                                          </span>
                                        )}
                                      </td>
                                      <td className="px-3 py-2.5">
                                        <div className="flex items-center justify-end gap-1">
                                          {canManage && (
                                            <>
                                              <Button
                                                variant="ghost"
                                                size="sm"
                                                className="h-8"
                                                onClick={() => setEditingRoom(r)}
                                                aria-label={`Customise ${r.room_name}`}
                                              >
                                                <Settings2
                                                  aria-hidden="true"
                                                  className="h-3.5 w-3.5"
                                                />
                                                {r.has_override ? "Edit" : "Customise"}
                                              </Button>
                                              {r.has_override && (
                                                <Button
                                                  variant="ghost"
                                                  size="sm"
                                                  className="h-8"
                                                  disabled={
                                                    resettingRoomID === r.room_id
                                                  }
                                                  onClick={() =>
                                                    handleResetOverride(r)
                                                  }
                                                  aria-label={`Reset ${r.room_name} to inherit`}
                                                >
                                                  {resettingRoomID === r.room_id ? (
                                                    <Loader2
                                                      aria-hidden="true"
                                                      className="h-3.5 w-3.5 animate-spin"
                                                    />
                                                  ) : (
                                                    <RotateCcw
                                                      aria-hidden="true"
                                                      className="h-3.5 w-3.5"
                                                    />
                                                  )}
                                                  Reset
                                                </Button>
                                              )}
                                            </>
                                          )}
                                        </div>
                                      </td>
                                    </tr>
                                  );
                                })}
                              </tbody>
                            </table>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </CardContent>
              </Card>

              {/* ── Placeholder: recipe + runs come in later slices ────── */}
              <Card className="opacity-70">
                <CardContent className="p-6 space-y-2">
                  <h2 className="text-sm font-semibold text-muted-foreground">
                    Coming in the next slices
                  </h2>
                  <ul className="text-xs text-muted-foreground list-disc list-inside space-y-1">
                    <li>
                      <span className="font-medium">Test recipe editor</span> —
                      author reusable functional tests that run after power-on.
                    </li>
                    <li>
                      <span className="font-medium">Run history heatmap</span>{" "}
                      — see the last N nights across the estate, drill into
                      per-step results, export as CSV.
                    </li>
                  </ul>
                </CardContent>
              </Card>

              {/* ── Save (customer default) ──────────────────────────── */}
              {canManage && (
                <div className="sticky bottom-0 -mx-6 border-t bg-background/95 backdrop-blur px-6 py-3">
                  <div className="max-w-3xl flex items-center justify-end gap-2">
                    <div className="mr-auto text-xs text-muted-foreground">
                      {dirty
                        ? "You have unsaved changes."
                        : loaded.updated_at
                          ? `Saved ${new Date(loaded.updated_at).toLocaleString()}`
                          : ""}
                    </div>
                    <Button
                      size="sm"
                      onClick={handleSave}
                      disabled={!dirty || saving}
                    >
                      {saving && (
                        <Loader2
                          aria-hidden="true"
                          className="h-3.5 w-3.5 animate-spin"
                        />
                      )}
                      Save changes
                    </Button>
                  </div>
                </div>
              )}

              {!canManage && (
                <div className="text-xs text-muted-foreground italic">
                  Read-only — you need Manage Room Readiness to edit these
                  fields.
                </div>
              )}
            </>
          )}
        </div>
      </div>

      {editingRoom && loaded && (
        <RoomOverrideModal
          room={editingRoom}
          customerDefault={loaded}
          onClose={() => setEditingRoom(null)}
          onSaved={() => {
            setEditingRoom(null);
            void loadRooms();
          }}
        />
      )}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────
// Room override modal
// ─────────────────────────────────────────────────────────────────────────
//
// Each of the three schedule fields (power-off, power-on, days) has a
// "customise" toggle:
//   - toggle OFF → this field inherits the customer default
//   - toggle ON  → the field's own value takes effect (with a picker to set it)
//
// excluded_until is orthogonal: sets a "skip this room until date" no
// matter what the schedule fields say. Set to empty to clear.
//
// On save the modal always sends explicit values for all four fields —
// null for "inherit" (or "not excluded"), value for "custom". That gives
// atomic write semantics: the row after save exactly matches what the
// modal shows.

function RoomOverrideModal({
  room,
  customerDefault,
  onClose,
  onSaved,
}: {
  room: NightlyRoomRow;
  customerDefault: NightlySchedule;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { toast } = useToast();

  const [customPowerOff, setCustomPowerOff] = useState(
    room.override_power_off_time !== undefined
  );
  const [customPowerOn, setCustomPowerOn] = useState(
    room.override_power_on_time !== undefined
  );
  const [customDays, setCustomDays] = useState(
    room.override_days_of_week !== undefined
  );
  const [powerOff, setPowerOff] = useState(
    room.override_power_off_time ?? customerDefault.power_off_time
  );
  const [powerOn, setPowerOn] = useState(
    room.override_power_on_time ?? customerDefault.power_on_time
  );
  const [days, setDays] = useState<number[]>(
    [...(room.override_days_of_week ?? customerDefault.days_of_week)].sort(
      (a, b) => a - b
    )
  );
  const [excludedUntil, setExcludedUntil] = useState(
    room.excluded_until ?? ""
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggleDay = (iso: number) => {
    setDays((prev) =>
      prev.includes(iso)
        ? prev.filter((d) => d !== iso)
        : [...prev, iso].sort((a, b) => a - b)
    );
  };

  const dirty = true; // modal always allows save; the dirty prompt would just add friction here.

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    const body: UpdateRoomOverrideBody = {
      // Explicit null = clear the override, inherit customer default. Value
      // = set the override. Every field is always in the payload so the
      // resulting row exactly reflects the modal state.
      power_off_time: customPowerOff ? powerOff : null,
      power_on_time: customPowerOn ? powerOn : null,
      days_of_week: customDays ? days : null,
      excluded_until: excludedUntil.trim() === "" ? null : excludedUntil,
    };
    try {
      await api.updateRoomOverride(room.room_id, body);
      toast({
        title: `Saved override for "${room.room_name}"`,
        variant: "success",
      });
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={`Customise "${room.room_name}"`}
      wide={false}
      dirty={dirty}
      dirtyPrompt="Discard changes to this room override?"
    >
      <div className="space-y-5">
        <p className="text-xs text-muted-foreground">
          Anything not customised inherits the customer default (
          <span className="font-mono">
            {customerDefault.power_off_time} → {customerDefault.power_on_time}
          </span>
          , {summariseDays(customerDefault.days_of_week)}).
        </p>

        {/* Power off */}
        <OverrideRow
          label="Power off at"
          custom={customPowerOff}
          onToggle={() => setCustomPowerOff((v) => !v)}
          inheritValue={customerDefault.power_off_time}
        >
          <input
            type="time"
            value={powerOff}
            onChange={(e) => setPowerOff(e.target.value)}
            disabled={saving}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
          />
        </OverrideRow>

        {/* Power on */}
        <OverrideRow
          label="Power on at"
          custom={customPowerOn}
          onToggle={() => setCustomPowerOn((v) => !v)}
          inheritValue={customerDefault.power_on_time}
        >
          <input
            type="time"
            value={powerOn}
            onChange={(e) => setPowerOn(e.target.value)}
            disabled={saving}
            className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
          />
        </OverrideRow>

        {/* Days */}
        <OverrideRow
          label="Days of week"
          custom={customDays}
          onToggle={() => setCustomDays((v) => !v)}
          inheritValue={summariseDays(customerDefault.days_of_week)}
        >
          <div className="flex flex-wrap gap-1">
            {DAY_BUTTONS.map((d) => {
              const active = days.includes(d.iso);
              return (
                <button
                  key={d.iso}
                  type="button"
                  aria-pressed={active}
                  aria-label={d.full}
                  disabled={saving}
                  onClick={() => toggleDay(d.iso)}
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
        </OverrideRow>

        {/* Exclusion — orthogonal to the schedule fields. */}
        <div className="space-y-2 rounded-md border p-3">
          <div className="flex items-center gap-2 text-sm font-medium">
            <CircleSlash
              aria-hidden="true"
              className="h-4 w-4 text-muted-foreground"
            />
            Exclude this room
          </div>
          <p className="text-xs text-muted-foreground">
            Skip this room from the nightly lifecycle until (and including)
            the chosen date. Leave blank to keep the room in the schedule.
          </p>
          <div className="flex items-center gap-2">
            <input
              type="date"
              value={excludedUntil}
              onChange={(e) => setExcludedUntil(e.target.value)}
              disabled={saving}
              className="rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
            />
            {excludedUntil && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setExcludedUntil("")}
                disabled={saving}
              >
                Clear
              </Button>
            )}
          </div>
        </div>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
            {error}
          </div>
        )}

        <div className="flex items-center justify-end gap-2 pt-2 border-t">
          <Button variant="outline" size="sm" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            Save override
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// OverrideRow — a single "custom vs inherit" field row used inside the
// override modal. When the checkbox is off the inherited value is shown as
// a subtle muted label; when it's on the caller-provided editor renders.
function OverrideRow({
  label,
  custom,
  onToggle,
  inheritValue,
  children,
}: {
  label: string;
  custom: boolean;
  onToggle: () => void;
  inheritValue: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium">{label}</span>
        <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
          <input
            type="checkbox"
            checked={custom}
            onChange={onToggle}
            className="h-3.5 w-3.5 rounded border-input"
          />
          Customise
        </label>
      </div>
      {custom ? (
        children
      ) : (
        <div className="text-xs text-muted-foreground">
          Inherits customer default:{" "}
          <span className="font-mono text-foreground">{inheritValue}</span>
        </div>
      )}
    </div>
  );
}
