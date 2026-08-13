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
    ];
  },
};

export default nextConfig;
