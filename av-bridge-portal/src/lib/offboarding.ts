// Offboarding helpers shared by the helpdesk page and the Deletions page.

// Whole days left until deletion, rounded UP: with 29 days and 3 hours left
// it says 30 days, and it says 1 day until the moment of deletion passes
// (then 0). Rounding up means the countdown never promises more time than
// is left.
export function daysUntil(deleteAfter: string, now: number = Date.now()): number {
  const ms = new Date(deleteAfter).getTime() - now;
  if (!Number.isFinite(ms)) return 0;
  return Math.max(0, Math.ceil(ms / 86_400_000));
}

export function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}

// Matches the cloud's own check: the typed name, with spaces trimmed from the
// ends, must equal the customer name exactly (capital letters count).
export const sameCustomerName = (typed: string, name: string) =>
  name.length > 0 && typed.trim() === name;
