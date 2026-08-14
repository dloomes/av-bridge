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

// Fetches /public/branding server-side. AV_BRIDGE_UPSTREAM is set by
// Terraform (envs/uat/main.tf) to the cloud API base URL — that's the same
// value the Next.js /api/v1/* rewrite uses, so both surfaces route through
// the same endpoint. `next: { revalidate: 300 }` matches the endpoint's own
// Cache-Control, so hot branding lookups hit the Next server cache and
// cold ones hit CloudFront before the DB.
async function fetchBranding(slug: string | null): Promise<Branding> {
  if (!slug) return {};
  const upstream = process.env.AV_BRIDGE_UPSTREAM;
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
  return <SignInForm branding={branding} />;
}
