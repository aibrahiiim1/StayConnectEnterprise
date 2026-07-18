import { defineConfig, devices } from "@playwright/test";

// Browser-level E2E for the Hotel Admin surface. Uses a locally-installed Chrome (channel) so no
// Playwright browser download is required. The web server runs the already-built Next app (built with
// NEXT_PUBLIC_PHASE2_ADMIN=1 for the flag-ON flow); the edged backend is mocked per-test via page.route,
// so no real backend, no production data and no disposable DB are touched by these specs.
const PORT = 3123;

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  expect: { timeout: 25_000 },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    ...devices["Desktop Chrome"],
    channel: "chrome",
    headless: true,
    navigationTimeout: 90_000,
    actionTimeout: 25_000,
  },
  // next dev (not start): dev mode compiles on demand and avoids the whole-app static-prerender memory
  // spike. NEXT_PUBLIC_PHASE2_ADMIN=1 makes this the flag-ON profile — a TEST-only server, never deployed.
  webServer: {
    command: `npx next dev -p ${PORT} -H 127.0.0.1`,
    env: { NEXT_PUBLIC_PHASE2_ADMIN: "1", NEXT_TELEMETRY_DISABLED: "1" },
    url: `http://127.0.0.1:${PORT}/login`,
    reuseExistingServer: true,
    timeout: 180_000,
  },
});
