/** @type {import('next').NextConfig} */
const AV_BRIDGE = process.env.AV_BRIDGE_UPSTREAM ?? "http://localhost:8080";

const nextConfig = {
  reactStrictMode: true,
  async rewrites() {
    return [
      { source: "/healthz", destination: `${AV_BRIDGE}/healthz` },
      { source: "/metrics", destination: `${AV_BRIDGE}/metrics` },
      { source: "/api/v1/:path*", destination: `${AV_BRIDGE}/api/v1/:path*` },
    ];
  },
};

export default nextConfig;
