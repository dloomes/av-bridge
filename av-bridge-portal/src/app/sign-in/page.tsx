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
// Result carries both the resolved branding and diagnostic breadcrumbs so
// the sign-in server component can render the branding while separately
// echoing why it decided what it decided. The diag half is temporary until
// wildcard routing + fetch behaviour are confirmed.
async function fetchBranding(slug: string | null): Promise<{
  branding: Branding;
  diag: Record<string, unknown>;
}> {
  const upstream = process.env.AV_BRIDGE_UPSTREAM ?? "";
  const url = slug && upstream
    ? `${upstream}/public/branding?slug=${encodeURIComponent(slug)}`
    : null;
  if (!url) {
    return {
      branding: {},
      diag: { skip_reason: !slug ? "no_slug" : "no_upstream", upstream_set: Boolean(upstream) },
    };
  }
  try {
    const resp = await fetch(url, { next: { revalidate: 300 } });
    const bodyText = await resp.text();
    let parsed: Branding = {};
    try {
      parsed = bodyText ? (JSON.parse(bodyText) as Branding) : {};
    } catch (e) {
      return {
        branding: {},
        diag: {
          upstream_set: true,
          fetch_url: url,
          fetch_status: resp.status,
          parse_error: (e as Error).message,
          body_len: bodyText.length,
        },
      };
    }
    return {
      branding: resp.ok ? parsed : {},
      diag: {
        upstream_set: true,
        fetch_url: url,
        fetch_status: resp.status,
        body_len: bodyText.length,
        parsed_keys: Object.keys(parsed),
      },
    };
  } catch (e) {
    // Any failure — DNS, timeout, TLS — falls back to defaults. Branding is
    // best-effort by design; a broken lookup must never block sign-in.
    return {
      branding: {},
      diag: {
        upstream_set: true,
        fetch_url: url,
        fetch_error: (e as Error).message,
      },
    };
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
  const { branding, diag: fetchDiag } = await fetchBranding(slug);

  // TEMP diagnostic — captured host headers + fetch mechanics so we can
  // pinpoint why acme.uat sign-in returns branding_keys: []. Remove once
  // customer-subdomain hydration is confirmed.
  const diag = JSON.stringify({
    host: h.get("host") ?? null,
    x_forwarded_host: h.get("x-forwarded-host") ?? null,
    resolved_slug: slug,
    branding_keys: Object.keys(branding),
    fetch: fetchDiag,
  });

  return (
    <>
      <SignInForm branding={branding} />
      <script
        id="av-diag-headers"
        type="application/json"
        // eslint-disable-next-line react/no-danger
        dangerouslySetInnerHTML={{ __html: diag }}
      />
    </>
  );
}
