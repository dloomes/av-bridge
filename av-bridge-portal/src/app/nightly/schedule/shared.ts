// Helpers shared by the Room Readiness tabs (page.tsx and
// readiness-panel.tsx). A page file can only export the page itself.

// ISO weekday order: 1 = Mon … 7 = Sun. Rendered left-to-right.
export const DAY_BUTTONS: { iso: number; label: string; full: string }[] = [
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

export function summariseDays(days: number[]): string {
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
export function isFutureDate(iso: string): boolean {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const d = new Date(iso + "T00:00:00");
  return d.getTime() >= today.getTime();
}

// duringPowerOff — true when an HH:MM time falls while the room is
// switched off: from power-off to power-on, across midnight.
export function duringPowerOff(time: string, off: string, on: string): boolean {
  if (off === on) return false;
  return off < on ? time >= off && time < on : time >= off || time < on;
}

// toggleDay adds or removes a weekday, keeping the list in order.
export function toggleDay(days: number[], iso: number): number[] {
  return days.includes(iso)
    ? days.filter((d) => d !== iso)
    : [...days, iso].sort((a, b) => a - b);
}
