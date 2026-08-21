/** @type {import('next').NextConfig} */
const AV_BRIDGE = process.env.AV_BRIDGE_UPSTREAM ?? "http://localhost:8080";

const nextConfig = {
  reactStrictMode: true,
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
    ];
  },
};

export default nextConfig;
