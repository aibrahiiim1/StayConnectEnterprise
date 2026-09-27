/** @type {import('next').NextConfig} */
const API_BASE = process.env.API_UPSTREAM || "http://127.0.0.1:8080";

/**
 * Every address the console had before the redesign, and where it lives now (docs/CENTRAL_CONTROL_PLANE.md §2).
 * Answered with a real 308 before anything renders, so bookmarks and old links keep working. Exported for the
 * test that keeps this map complete.
 */
export const RETIRED_ROUTES = [
  ["/dashboard", "/overview"],
  ["/tenants", "/customers"],
  ["/sites", "/customers"],
  ["/onboarding", "/appliances?activation=waiting"],
  ["/operators", "/system/team"],
  ["/security", "/system/security-alerts"],
  ["/certificates", "/system/trust"],
  ["/assignment-keys", "/system/trust"],
  ["/backup-health", "/system/backup-health"],
  ["/audit", "/system/audit"],
  ["/commercial", "/licenses"],
  ["/subscription", "/licenses"],
];

const nextConfig = {
  reactStrictMode: true,
  // Standalone output bundles a minimal server (.next/standalone/server.js) so
  // the app ships as a self-contained release and runs via `node server.js`
  // with no npm install / webpack build on the target host.
  output: "standalone",
  async redirects() {
    return [
      { source: "/", destination: "/overview", permanent: false },
      { source: "/system", destination: "/system/security-alerts", permanent: false },
      ...RETIRED_ROUTES.flatMap(([from, to]) => [
        { source: from, destination: to, permanent: true },
        // A deeper old address (/sites/…, /audit/…) lands on the same replacement.
        { source: `${from}/:rest*`, destination: to, permanent: true },
      ]),
    ];
  },
  async rewrites() {
    // Only the two API families ctrlapi serves to the console are proxied; nothing else under /api reaches it.
    return [
      { source: "/api/v1/:path*", destination: `${API_BASE}/v1/:path*` },
      { source: "/api/cloud/:path*", destination: `${API_BASE}/cloud/:path*` },
    ];
  },
};
export default nextConfig;
