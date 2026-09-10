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
  // The infra reporter runs ALONGSIDE list; it adds a verdict, it never replaces the per-test output and
  // never changes the exit status. See e2e-infra-reporter.ts for the failure it exists to make legible.
  reporter: [["list"], ["./e2e-infra-reporter.ts"]],
  globalSetup: "./e2e-global-setup.ts",
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
    env: { NEXT_PUBLIC_PHASE2_ADMIN: "1", NEXT_PUBLIC_PHASE3_ADMIN: "1", NEXT_PUBLIC_PHASE4_ADMIN: "1", NEXT_PUBLIC_PHASE5_ADMIN: "1", NEXT_TELEMETRY_DISABLED: "1" },
    url: `http://127.0.0.1:${PORT}/login`,
    // Locally, reusing a server that is already up saves a compile on every run. In CI it is a hazard: an
    // adopted process is one Playwright did not configure (the `env` above applies only to a server it
    // spawns itself) and does not tear down, so a stray server could silently serve a different build than
    // the one under test.
    reuseExistingServer: !process.env.CI,
    // Surface the dev server's own output. Without this its crash -- the thing that turns one infrastructure
    // fault into a screenful of connection errors -- is written nowhere the report can show.
    stdout: "pipe",
    stderr: "pipe",
    timeout: 180_000,
  },
});
