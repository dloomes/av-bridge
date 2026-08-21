// Session helpers — single source of truth for the current bearer token and
// the user it represents. We store the token in localStorage and the
// resolved user as JSON alongside it, so /whoami doesn't have to round-trip
// on every page mount.
//
// Mock-JWT scaffolding: tokens look like `mock.<base64-json>`. The portal
// builds these in sign-in/page.tsx from preset users. When real Entra is
// wired in, NextAuth (or similar) will replace the construction step but
// the rest of the app keeps reading `getToken()` exactly as it does today.

export interface SessionUser {
  user_id?: string;
  email?: string;
  name?: string;
  customer_id?: string;
  role: string;
  is_vendor?: boolean;
  // Effective permissions returned by /whoami. For vendor callers the
  // backend expands the "all permissions" bypass into the concrete set,
  // so UI code can just probe hasPermission() without a separate branch.
  // Missing / empty means "no permissions loaded yet" — the sidebar
  // treats gated items as hidden until whoami hydrates them.
  permissions?: string[];
}

const TOKEN_KEY = "av-bridge.session.token";
const USER_KEY = "av-bridge.session.user";
const SCOPE_KEY = "av-bridge.session.scope";

// Listeners get notified on any session change (sign-in, sign-out, scope
// switch). React components subscribe via useSession; non-React code (the
// API client) just reads the latest values lazily.
type Listener = () => void;
const listeners = new Set<Listener>();
function notify() {
  listeners.forEach((l) => l());
}

export function subscribe(l: Listener): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(TOKEN_KEY);
}

export function getUser(): SessionUser | null {
  if (typeof window === "undefined") return null;
  const raw = window.localStorage.getItem(USER_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as SessionUser;
  } catch {
    return null;
  }
}

// Vendor-only: the customer the helpdesk user is currently acting as. Sent
// as X-Customer-Scope header by the API client. Non-vendor sessions ignore
// this entirely.
export function getScope(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(SCOPE_KEY);
}

export function signIn(token: string, user: SessionUser): void {
  window.localStorage.setItem(TOKEN_KEY, token);
  window.localStorage.setItem(USER_KEY, JSON.stringify(user));
  window.localStorage.removeItem(SCOPE_KEY);
  notify();
}

export function signOut(): void {
  window.localStorage.removeItem(TOKEN_KEY);
  window.localStorage.removeItem(USER_KEY);
  window.localStorage.removeItem(SCOPE_KEY);
  notify();
}

export function setScope(customerID: string | null): void {
  if (customerID) {
    window.localStorage.setItem(SCOPE_KEY, customerID);
  } else {
    window.localStorage.removeItem(SCOPE_KEY);
  }
  notify();
}

// Role capability helpers. Mirror the wrapAdmin / wrapOperator gates the
// cloud applies at the route layer — UI uses these to hide buttons the
// backend would 403 anyway. Don't rely on these for security; they're for
// not-showing-broken-UI, not for keeping anyone out.
export function isAdmin(role?: string | null): boolean {
  return role === "admin";
}
export function canOperate(role?: string | null): boolean {
  return role === "admin" || role === "operator";
}

// hasPermission reports whether the current session's effective permission
// list contains the given key. This is the mechanism the sidebar and
// action buttons use to hide UI a custom role can't act on — falls
// through to the backend for the actual authorisation.
//
// While permissions are still loading (whoami not yet returned) this
// returns false — better to briefly hide an item and reveal it than
// flash a button the user isn't allowed to press.
export function hasPermission(user: SessionUser | null | undefined, key: string): boolean {
  if (!user?.permissions) return false;
  return user.permissions.includes(key);
}

// buildMockToken assembles a `mock.<base64-json>` token from claims. Used by
// the sign-in page's preset buttons. Exposed so a custom-paste UI can also
// validate that what the user pasted is a recognisable shape.
export function buildMockToken(claims: {
  sub: string;
  email: string;
  name: string;
  tid: string;
  groups: string[];
}): string {
  const json = JSON.stringify(claims);
  // btoa works on strings — fine for ASCII claims. Non-ASCII names would need
  // a TextEncoder pass, which we don't yet care about.
  const b64 = window.btoa(json);
  return `mock.${b64}`;
}
