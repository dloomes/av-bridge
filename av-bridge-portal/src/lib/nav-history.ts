// In-portal navigation trail, so a Back link can return to the page the
// user came from rather than a fixed parent. Pathnames only: filters live
// in the query string and come back with the browser history entry.
//
// Kept in sessionStorage so a reload keeps it; per tab, so a link opened
// in a new tab starts with no trail (and its Back goes to the parent).

const KEY = "nav-trail";
const MAX = 50;

function read(): string[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) ?? "[]");
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

function write(trail: string[]) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(trail.slice(-MAX)));
  } catch {
    /* storage blocked: Back falls back to the parent link */
  }
}

// arrive is the trail after landing on pathname. Arriving at the page
// before the current one is taken as going back.
function arrive(trail: string[], pathname: string): string[] {
  const last = trail[trail.length - 1];
  if (last === pathname) return trail;
  if (trail.length >= 2 && trail[trail.length - 2] === pathname) {
    return trail.slice(0, -1);
  }
  return [...trail, pathname];
}

// trackPath records a page change (AppShell, on every pathname change).
export function trackPath(pathname: string) {
  write(arrive(read(), pathname));
}

// canGoBack says whether there's a portal page to return to in this tab.
// It counts the current page itself: a page's effects run before the
// shell's, so the trail may not have it yet.
export function canGoBack(): boolean {
  if (typeof window === "undefined" || window.history.length < 2) return false;
  return arrive(read(), window.location.pathname).length >= 2;
}
