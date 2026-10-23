import { defineConfig, devices } from "@playwright/test";

// Browser E2E for the Hotel Admin using a local Chrome; edged is mocked per-test via page.route, so no real backend or
// data.
const PORT = 3123;

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  fullyParallel: false,
  workers: 1,
  // The infra reporter runs alongside list; it adds a verdict and never changes output or exit status.
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
  // next dev avoids the whole-app static-prerender memory spike; NEXT_PUBLIC_PHASE2_ADMIN=1 is the flag-ON profile
  // (test only).
  webServer: {
    command: `npx next dev -p ${PORT} -H 127.0.0.1`,
    env: { NEXT_PUBLIC_PHASE2_ADMIN: "1", NEXT_PUBLIC_PHASE3_ADMIN: "1", NEXT_PUBLIC_PHASE4_ADMIN: "1", NEXT_PUBLIC_PHASE5_ADMIN: "1", NEXT_TELEMETRY_DISABLED: "1" },
    url: `http://127.0.0.1:${PORT}/login`,
    // Reuse locally; in CI an adopted server was not configured by Playwright and could serve a different build.
    reuseExistingServer: !process.env.CI,
    // Surface the dev server's output so its crash is visible in the report.
    stdout: "pipe",
    stderr: "pipe",
    timeout: 180_000,
  },
});
