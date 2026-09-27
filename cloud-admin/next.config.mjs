/** @type {import('next').NextConfig} */
const API_BASE = process.env.API_UPSTREAM || "http://127.0.0.1:8080";

const nextConfig = {
  reactStrictMode: true,
  // Standalone output bundles a minimal server (.next/standalone/server.js) so
  // the app ships as a self-contained release and runs via `node server.js`
  // with no npm install / webpack build on the target host.
  output: "standalone",
  // Retired screens answer with a real 308 before anything renders; a permanentRedirect() inside a page under
  // the client-rendered (app) layout arrives inside a streamed 200. The page files stay as a fallback.
  async redirects() {
    return [
      { source: "/commercial", destination: "/licenses", permanent: true },
      { source: "/subscription", destination: "/licenses", permanent: true },
    ];
  },
  async rewrites() {
    return [
      { source: "/api/:path*", destination: `${API_BASE}/:path*` },
    ];
  },
};
export default nextConfig;
