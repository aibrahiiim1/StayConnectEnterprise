import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

// After Check-out Access: the one place for the policy (docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md section 6).

type Mutations = { method: string; path: string; body: any }[];
const GB = 1024 ** 3;
const policy = {
  duration_hours: 12, duration_seconds: 43200, down_kbps: 5000, up_kbps: 2000,
  data_quota_bytes: GB, device_limit: 5, device_policy: "REJECT_NEW_DEVICE",
};
const bounds = {
  duration_hours: { min: 1, max: 72, default: 12, unit: "hours" },
  down_kbps: { min: 64, max: 1000000, default: 5000, unit: "kbps" },
  up_kbps: { min: 64, max: 1000000, default: 2000, unit: "kbps" },
  data_quota_bytes: { min: 10485760, max: 107374182400, default: GB, unit: "bytes" },
  device_limit: { min: 1, max: 20, default: 5, unit: "devices" },
};
const published = {
  enabled: true, configured: true, config_version: 3, policy, bounds,
  effective: { source: "PUBLISHED", policy }, supported_device_policies: ["REJECT_NEW_DEVICE"],
  emergency_history: { count: 0 },
};
const unconfigured = {
  ...published, configured: false, config_version: 0, policy: null,
  effective: { source: "EMERGENCY_FALLBACK", policy: { ...policy, duration_hours: 1, duration_seconds: 3600 } },
};

async function installBackend(page: Page, opts: { state?: unknown; putStatus?: number; mutations: Mutations }) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    let body: unknown;
    try { body = req.postDataJSON(); } catch { /* none */ }
    if (method !== "GET") opts.mutations.push({ method, path, body });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    if (path === "/after-checkout-access" && method === "PUT") {
      const st = opts.putStatus ?? 200;
      if (st === 409) return route.fulfill(json(409, { error: "version_conflict", message: "current version is 9" }));
      if (st !== 200) return route.fulfill(json(st, { error: "validation", message: "refused" }));
      return route.fulfill(json(200, { config_version: 4 }));
    }
    if (path === "/after-checkout-access/history") {
      return route.fulfill(json(200, { available: true, data: [
        { config_version: 3, published_at: "2026-10-02T10:00:00Z", actor: "Ali", reason_code: "POLICY_CHANGE",
          policy: { grace_duration_seconds: 43200, grace_down_kbps: 5000, grace_up_kbps: 2000, grace_data_quota_bytes: GB, grace_device_limit: 5, grace_device_limit_policy: "REJECT_NEW_DEVICE" } },
      ] }));
    }
    if (path === "/after-checkout-access") return route.fulfill(json(200, opts.state ?? published));
    if (path.startsWith("/post-stay-profiles")) return route.fulfill(json(200, { profiles: [] }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await installCapabilities(page);
}

async function openEditor(page: Page) {
  await page.getByRole("button", { name: /^Edit settings$/ }).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
}

async function reviewAndSave(page: Page, password = "operator-pw") {
  await page.getByRole("button", { name: /Review changes/ }).click();
  await expect(page.getByLabel("Changes", { exact: true })).toBeVisible();
  await page.getByLabel("Confirm your password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /^Save settings$/ }).click();
}

test("the page states the contract and the effective source", async ({ page }) => {
  await installBackend(page, { mutations: [] });
  await page.goto("/after-checkout-access");
  await expect(page.getByRole("heading", { level: 1, name: "After Check-out Access" })).toBeVisible();
  await expect(page.getByTestId("aca-sentence")).toContainText("12 hours after the verified check-out");
  await expect(page.getByTestId("aca-sentence")).toContainText("share 1 GB of data in total");
  await expect(page.getByTestId("aca-source")).toHaveAttribute("data-source", "PUBLISHED");
});

test("saving sends the whole policy with the viewed version, a reason code and the password", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/after-checkout-access");
  await openEditor(page);
  await page.getByLabel(/Duration after verified check-out/).fill("24");
  await reviewAndSave(page);
  await expect.poll(() => mutations.find((m) => m.method === "PUT")).toBeTruthy();
  const sent = mutations.find((m) => m.method === "PUT")!.body;
  expect(sent).toMatchObject({ duration_hours: 24, down_kbps: 5000, up_kbps: 2000, data_quota_bytes: GB, device_limit: 5,
    device_policy: "REJECT_NEW_DEVICE", expected_config_version: 3, password: "operator-pw" });
  expect(String(sent.reason_code)).toMatch(/^[A-Z][A-Z0-9_]{0,63}$/);
  await expect(page.getByText("Version 4 saved")).toBeVisible();
});

test("an out-of-range duration is refused before anything is sent", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/after-checkout-access");
  await openEditor(page);
  await page.getByLabel(/Duration after verified check-out/).fill("100");
  await page.getByRole("button", { name: /Review changes/ }).click();
  await expect(page.getByText(/between 1 and 72/)).toBeVisible();
  expect(mutations.filter((m) => m.method === "PUT")).toHaveLength(0);
});

test("a refused change is surfaced and a version conflict keeps the draft", async ({ page }) => {
  await installBackend(page, { putStatus: 409, mutations: [] });
  await page.goto("/after-checkout-access");
  await openEditor(page);
  await page.getByLabel(/Duration after verified check-out/).fill("30");
  await reviewAndSave(page);
  await expect(page.getByText(/Someone else changed this setting/)).toBeVisible();
  await expect(page.getByLabel(/Duration after verified check-out/)).toHaveValue("30");
});

test("a site with nothing published shows the emergency fallback, not an error", async ({ page }) => {
  await installBackend(page, { state: unconfigured, mutations: [] });
  await page.goto("/after-checkout-access");
  await expect(page.getByText(/Departing guests are on the emergency fallback/)).toBeVisible();
  await expect(page.getByTestId("aca-source")).toHaveAttribute("data-source", "EMERGENCY_FALLBACK");
});

test("the retired Grace Period and Post-stay addresses lead here", async ({ page }) => {
  await installBackend(page, { mutations: [] });
  for (const old of ["/checkout-grace", "/post-stay"]) {
    await page.goto(old);
    await expect(page).toHaveURL(/\/after-checkout-access$/);
  }
});

test("it fits a 390px phone without horizontal scrolling", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await installBackend(page, { mutations: [] });
  await page.goto("/after-checkout-access");
  await expect(page.getByTestId("aca-sentence")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});
