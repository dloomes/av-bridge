/** @type {import('next').NextConfig} */
const AV_BRIDGE_URL =
  process.env.AV_BRIDGE_URL ??
  process.env.NEXT_PUBLIC_AV_BRIDGE_URL ??
  'http://localhost:8080';

const nextConfig = {
  reactStrictMode: true,
  async rewrites() {
    return [
      { source: '/api/v1/:path*', destination: `${AV_BRIDGE_URL}/api/v1/:path*` },
      { source: '/metrics', destination: `${AV_BRIDGE_URL}/metrics` },
    ];
  },
};

module.exports = nextConfig;
