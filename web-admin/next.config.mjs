/** @type {import('next').NextConfig} */
const API_BASE = process.env.API_UPSTREAM || "http://127.0.0.1:8080";

const nextConfig = {
  reactStrictMode: true,
  // Standalone output (node server.js) so the Central server runs a prebuilt
  // bundle — no npm/webpack build on the production Control Plane.
  output: "standalone",
  async rewrites() {
    return [
      { source: "/api/:path*", destination: `${API_BASE}/:path*` },
    ];
  },
};
export default nextConfig;
