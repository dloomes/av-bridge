// Reserved subdomains that must NEVER be treated as customer slugs — they
// have their own routing purpose (app = universal portal, api = cloud API,
// helpdesk = vendor console, etc.) and colliding a customer with any of
// these would break auth or DNS. Kept as a Set for O(1) membership.
export const RESERVED_SLUGS = new Set<string>([
  "app",
  "www",
  "api",
  "admin",
  "helpdesk",
  "staging",
  "preview",
  "localhost",
  "dev",
  "test",
  "demo",
  "support",
]);

// Same shape as the customers_slug_format CHECK in migration 0030 —
// 3-50 chars, [a-z0-9-], must start AND end alphanumeric. A hostname
// label that doesn't match can't have been a legitimate slug, so we
// bail rather than issue a lookup for it.
export const SLUG_RE = /^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$/;

// validateSlug returns a human-readable error string when the input isn't
// an acceptable slug, or null when it's fine (including the empty string,
// which means "no slug set"). Shared between the create-customer and
// edit-customer forms so both surface identical validation.
export function validateSlug(raw: string): string | null {
  const s = raw.trim().toLowerCase();
  if (s === "") return null;
  if (!SLUG_RE.test(s)) {
    return "3-50 characters, lowercase letters/digits/hyphens only, start and end alphanumeric.";
  }
  if (RESERVED_SLUGS.has(s)) {
    return `"${s}" is reserved for platform routing. Pick something else.`;
  }
  return null;
}

/**
 * Extracts a customer slug from a request hostname, or null when the
 * hostname isn't customer-scoped. Used by the sign-in server component to
 * decide whether to hit /public/branding.
 *
 * Examples:
 *   acme.uat.involvecloud.com  → "acme"
 *   app.uat.involvecloud.com   → null   (reserved)
 *   involvecloud.com           → null   (apex — no subdomain)
 *   localhost:3000             → null
 */
export function extractSlugFromHost(
  host: string | null | undefined
): string | null {
  if (!host) return null;
  // Proxied host headers can arrive comma-joined when multiple hops each
  // add their own value (Headers.get concatenates duplicates with ", ").
  // The leftmost entry is the customer-visible hostname; downstream entries
  // are the proxy chain's internal hostnames. Take the leftmost.
  const raw = host.split(",")[0].trim();
  const cleanHost = raw.split(":")[0].toLowerCase();
  const parts = cleanHost.split(".");
  // Apex + 2-label hosts (involvecloud.com, example.co) can't carry a slug.
  if (parts.length < 3) return null;
  const first = parts[0];
  if (RESERVED_SLUGS.has(first)) return null;
  if (!SLUG_RE.test(first)) return null;
  return first;
}
