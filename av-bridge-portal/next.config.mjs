/** @type {import('next').NextConfig} */
const AV_BRIDGE = process.env.AV_BRIDGE_UPSTREAM ?? "http://localhost:8080";

// Security headers on every response, proxied /api, /public and /pub
// included. The CSP only covers framing, plugins and <base>; a script-src /
// connect-src policy needs testing against Mapbox and the device-events
// WebSocket before it can be enforced. Nothing embeds the portal, so
// framing is refused outright (clickjacking).
const securityHeaders = [
  { key: "Strict-Transport-Security", value: "max-age=63072000; includeSubDomains" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Content-Security-Policy", value: "frame-ancestors 'none'; base-uri 'self'; object-src 'none'" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
];

const nextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
  // Lint runs separately in CI / pre-commit; a lint error in a JSX string
  // shouldn't block a UAT deploy. Cosmetic errors get cleaned up in the
  // normal review cycle, not during build.
  eslint: { ignoreDuringBuilds: true },
  async rewrites() {
    return [
      { source: "/healthz", destination: `${AV_BRIDGE}/healthz` },
      { source: "/metrics", destination: `${AV_BRIDGE}/metrics` },
      { source: "/api/v1/:path*", destination: `${AV_BRIDGE}/api/v1/:path*` },
      // Pre-login endpoints. Reachable via the app origin so client-side
      // calls from /sign-in, /forgot-password and /reset-password stay
      // same-origin (no CORS preflight). Same forwarding pattern as
      // /api/v1/*; auth is enforced at the cloud, not the proxy.
      { source: "/public/:path*", destination: `${AV_BRIDGE}/public/:path*` },
      // Public integration API. Proxied through the portal origin so
      // an operator can click through to Swagger UI at /pub/v1/docs
      // from Settings → API tokens without a separate hostname. Auth
      // is enforced at the cloud; the docs + spec endpoints are
      // unauthenticated by design.
      { source: "/pub/:path*", destination: `${AV_BRIDGE}/pub/:path*` },
    ];
  },
};

export default nextConfig;
