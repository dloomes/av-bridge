// Server component. Reads the request Host header and, when it looks like a
// customer subdomain (<slug>.<env>.involvecloud.com), fetches that customer's
// branding from the cloud's public endpoint so the sign-in surface renders
// with their logo/name/accent BEFORE any credentials exist. Universal and
// reserved hostnames (app / helpdesk / apex) fall through to defaults.
//
// The client-side SignInForm receives the branding object as a prop and
// applies it inline — no useEffect, no post-mount repaint.

import { headers } from "next/headers";
import { SignInForm } from "./sign-in-form";
import type { Branding } from "@/lib/api";
import { extractSlugFromHost } from "@/lib/branding-slug";

// Fetches /public/branding server-side. The upstream URL is read from an
// env var that Next.js inlines at build time so the value survives into
// the Amplify WEB_COMPUTE Lambda's runtime bundle (plain environment_
// variables aren't automatically forwarded to SSR runtime — Amplify quirk).
// Falls back to the non-prefixed variant for local `next dev` where the
// shell populates process.env directly.
//
// `next: { revalidate: 300 }` matches the endpoint's own Cache-Control so
// hot branding lookups hit the Next server cache and cold ones hit
// CloudFront before the DB.
async function fetchBranding(slug: string | null): Promise<Branding> {
  if (!slug) return {};
  const upstream =
    process.env.NEXT_PUBLIC_AV_BRIDGE_UPSTREAM ??
    process.env.AV_BRIDGE_UPSTREAM ??
    "";
  if (!upstream) return {};
  try {
    const resp = await fetch(
      `${upstream}/public/branding?slug=${encodeURIComponent(slug)}`,
      { next: { revalidate: 300 } }
    );
    if (!resp.ok) return {};
    return (await resp.json()) as Branding;
  } catch {
    // Any failure — DNS, timeout, malformed JSON — falls back to the default
    // portal look. Branding is best-effort by design; a broken lookup must
    // never block a customer from signing in.
    return {};
  }
}

// appOriginFromHost derives the unbranded portal origin from the request
// hostname. Customer SSO's authorize URL always points at the app.<env>
// subdomain (that's where the Entra callback lands, single registered
// redirect_uri), so branded pages need to link cross-origin. On a
// branded host like "acme.uat.involvecloud.com" the slug label is
// replaced with "app"; on the app.<env> origin or when there's no host
// info, an empty string is returned so the client falls back to a
// same-origin relative link.
function appOriginFromHost(host: string | null | undefined): string {
  if (!host) return "";
  const raw = host.split(",")[0].trim();
  const [hostname, port] = raw.split(":");
  if (!hostname) return "";
  const parts = hostname.toLowerCase().split(".");
  if (parts.length < 3) return "";
  // Assume https anywhere the hostname has a public suffix pattern. Local
  // dev with a made-up 3-label host would fall through here too, but
  // customer SSO doesn't run without a proper HTTPS origin anyway.
  const suffix = parts.slice(1).join(".");
  const portPart = port ? `:${port}` : "";
  return `https://app.${suffix}${portPart}`;
}

export default async function SignInPage() {
  // Amplify SSR runs behind CloudFront, which can rewrite the direct `Host`
  // to the internal *.amplifyapp.com hostname while preserving the caller's
  // hostname in x-forwarded-host. Prefer that header so per-customer
  // subdomains route correctly; fall back to `host` for local dev + any
  // deploy where the proxy chain doesn't rewrite Host.
  const h = headers();
  const hostHeader = h.get("x-forwarded-host") ?? h.get("host");
  const slug = extractSlugFromHost(hostHeader);
  const branding = await fetchBranding(slug);
  // Vendor Entra SSO surfaces only on the unbranded portal origin
  // (app.<env>.involvecloud.com). Branded customer subdomains get their
  // own customer-SSO tile when the customer has entra_tenant_id set
  // (M2) — sso_available on the branding response gates that.
  const showVendorSSO = slug === null;
  const appOrigin = appOriginFromHost(hostHeader);
  return (
    <SignInForm
      branding={branding}
      showVendorSSO={showVendorSSO}
      slug={slug ?? undefined}
      appOrigin={appOrigin}
    />
  );
}
