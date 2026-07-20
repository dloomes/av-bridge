"use client";

import { useEffect, useState } from "react";
import { Loader2, Moon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import type { NightlySchedule, UpdateNightlyScheduleBody } from "@/lib/api";

// Room Readiness — customer-level schedule editor.
//
// Slice 2C: this page consumes GET/PATCH /api/v1/nightly/schedule. Per-room
// overrides + recipe editor + runs heatmap live in later slices; the
// "coming next" placeholders exist here to reserve their visual space so
// the layout doesn't shift when they land.

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

export default function NightlySchedulePage() {
  const session = useSession();
  const canManage = isAdmin(session.user?.role) || !!session.user?.is_vendor;
  const { toast } = useToast();

  // Loaded state (what the server currently has). Local draft state below
  // tracks user edits; the save handler diffs draft vs loaded to build the
  // PATCH body.
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

  useEffect(() => {
    const ctrl = new AbortController();
    api
      .getNightlySchedule(ctrl.signal)
      .then((s) => {
        if (ctrl.signal.aborted) return;
        setLoaded(s);
        setEnabled(s.enabled);
        setPowerOff(s.power_off_time);
        setPowerOn(s.power_on_time);
        setDays([...s.days_of_week].sort((a, b) => a - b));
        setTimezone(s.timezone);
        setHelpdeskEmail(s.helpdesk_email ?? "");
        setRetentionDays(s.retention_days);
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setLoadError((e as Error).message);
      });
    return () => ctrl.abort();
  }, []);

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
    // Diff loaded vs draft; only send changed fields. Empty helpdesk_email
    // sends "" to clear the value; unchanged omits the field entirely.
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
      // Refetch so updated_at and any server-side normalisation land in
      // local state; keeps dirty tracking honest.
      const fresh = await api.getNightlySchedule();
      setLoaded(fresh);
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

  // If the stored timezone is a value not in the curated select, splice it
  // in so the user isn't forced to change it just to save something else.
  const timezoneOptions = COMMON_TIMEZONES.includes(timezone)
    ? COMMON_TIMEZONES
    : [timezone, ...COMMON_TIMEZONES];

  const inputsDisabled = !canManage || saving;

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

              {/* ── Placeholder: recipe + room overrides come in later slices */}
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
                      <span className="font-medium">Per-room overrides</span> —
                      customise the schedule / exclude a room until a date
                      per-room.
                    </li>
                    <li>
                      <span className="font-medium">Run history heatmap</span>{" "}
                      — see the last N nights across the estate, drill into
                      per-step results, export as CSV.
                    </li>
                  </ul>
                </CardContent>
              </Card>

              {/* ── Save ──────────────────────────────────────────────── */}
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
    </div>
  );
}
