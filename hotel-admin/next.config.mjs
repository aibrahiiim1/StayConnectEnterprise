/** @type {import('next').NextConfig} */
// Hotel Admin runs ON the appliance and talks to edged (the Edge API) on
// loopback. Caddy fronts this app on the management IP — never guest/WAN.
const EDGE_BASE = process.env.EDGE_UPSTREAM || "http://127.0.0.1:8090";

const nextConfig = {
  reactStrictMode: true,
  // Standalone output bundles a minimal server (.next/standalone/server.js)
  // with only the runtime deps it needs. The appliance runs `node server.js`
  // — NO npm install and NO build on the VM — which is what prevents a
  // repeat of the /root build that exhausted the pilot's memory.
  output: "standalone",
  async rewrites() {
    return [
      { source: "/api/:path*", destination: `${EDGE_BASE}/:path*` },
    ];
  },
};
export default nextConfig;
