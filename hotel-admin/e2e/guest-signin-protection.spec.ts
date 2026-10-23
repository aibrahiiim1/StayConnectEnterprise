import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

type Mutations = { method: string; path: string; body: any }[];

const POLICY = {
  max_failed_attempts: 5,
  observation_window_seconds: 60,
  restriction_seconds: 60,
  is_default: true,
  limits: {
    min_failed_attempts: 3,
    max_failed_attempts: 20,
    min_observation_window_seconds: 30,
    max_observation_window_seconds: 3600,
    min_restriction_seconds: 30,
    max_restriction_seconds: 3600,
  },
  last_change: null as any,
};

const RESTRICTION = {
  id: "restr-1",
  device_mac: "02:00:00:aa:bb:01",
  guest_network: "Guest WiFi",
  last_submitted_room: "412",
  failure_count: 5,
  reason: "FAILED_CREDENTIAL_THRESHOLD",
  restricted_at: "2026-09-12T10:00:00Z",
  expires_at: "2099-01-01T00:00:00Z",
  remaining_seconds: 47,
};

async function installBackend(
  page: Page,
  mutations: Mutations,
  roles = ["site_admin"],
  opts: { policy?: any; restrictions?: any[] } = {},
) {
  const policy = opts.policy ?? POLICY;
  const restrictions = opts.restrictions ?? [RESTRICTION];

  await page.route("**/api/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^\/api\/edge\/v1/, "");
    const method = req.method();

    if (path === "/auth/whoami") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ operator_id: "op-1", email: "op@test.local", roles }),
      });
    }
    if (method === "GET" && path === "/guest-signin-protection") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(policy) });
    }
    if (method === "PUT" && path === "/guest-signin-protection") {
      const body = req.postDataJSON();
      mutations.push({ method, path, body });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...policy, ...body, is_default: false }),
      });
    }
    if (method === "GET" && path === "/guest-signin-restrictions") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ restrictions }),
      });
    }
    if (method === "POST" && /\/guest-signin-restrictions\/.+\/release$/.test(path)) {
      mutations.push({ method, path, body: req.postDataJSON() });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          released: true,
          note: "The device may attempt to sign in again. It has not been granted access.",
        }),
      });
    }
    if (method === "GET" && path.startsWith("/guest-signin-attempts")) {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: [] }) });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  await installCapabilities(page); // a fully licensed hotel appliance (e2e/capabilities.ts)
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
    { name: "sc_edge_session", value: "e2e-test", url: "http://localhost:3123" },
  ]);
}

test.describe("guest sign-in protection settings", () => {
  test("each value arrives with its unit, its standard and its allowed range", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/sign-in-methods");

    const card = page.locator("div").filter({ hasText: /client sign-in protection/i }).first();
    await expect(page.getByText(/client sign-in protection/i).first()).toBeVisible();

    await expect(page.getByLabel(/maximum failed attempts/i)).toBeVisible();
    await expect(page.getByLabel(/observation window/i)).toBeVisible();
    await expect(page.getByLabel(/wait after too many attempts/i)).toBeVisible();
    await expect(page.getByText("attempts", { exact: true })).toBeVisible();
    await expect(page.getByText("seconds", { exact: true }).first()).toBeVisible();

    await expect(page.getByText(/default 5 attempts; allowed 3–20/i)).toBeVisible();
    await expect(page.getByText(/default 60 seconds; allowed 30–3600/i).first()).toBeVisible();

    await expect(page.getByText(/using the standard settings/i)).toBeVisible();
    await expect(page.getByText(/it is always on/i)).toBeVisible();
  });

  test("the screen says a change applies to what happens next, and names the action that does not", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/sign-in-methods");

    await expect(page.getByText(/new settings apply to\s*what happens next/i)).toBeVisible();
    await expect(page.getByText(/shortening the wait here does not end a wait already running/i)).toBeVisible();
    await expect(page.getByText(/releasing allows another attempt; it does not sign anyone in/i)).toBeVisible();
  });

  test("a value outside the allowed range cannot be saved", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/sign-in-methods");

    const attempts = page.getByLabel(/maximum failed attempts/i);
    await attempts.fill("1");
    await expect(page.getByText(/enter a whole number between 3 and 20/i)).toBeVisible();

    await attempts.fill("8");
    await page.getByRole("button", { name: /save protection settings/i }).click();
    const confirm = page.getByRole("dialog", { name: /change client sign-in protection/i });
    await confirm.getByLabel(/reason/i).fill("conference floor, frequent typos");
    await confirm.getByLabel(/confirm your password/i).fill("operatorpw");
    await confirm.getByRole("button", { name: /save protection settings/i }).click();
    await expect.poll(() => mutations.length).toBeGreaterThan(0);
    expect(mutations[0].body.max_failed_attempts).toBe(8);
    expect(mutations[0].body.reason).toBe("conference floor, frequent typos");
    expect(mutations[0].body.password).toBe("operatorpw");
    await expect(page.getByText(/nothing needs restarting/i)).toBeVisible();
  });

  test("a role that may only read the policy is offered no way to change it", async ({ page }) => {
    await installBackend(page, [], ["front_office_operator"]);
    await page.goto("/sign-in-methods");

    await expect(page.getByText(/your role can see these settings but not change them/i)).toBeVisible();
    await expect(page.getByRole("button", { name: /save protection settings/i })).toHaveCount(0);
  });
});

test.describe("the active restrictions tab", () => {
  test("shows the device, the network and the room as an unverified submission", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/guest-signin-attempts");
    await page.getByRole("tab", { name: /active restrictions/i }).click();

    await expect(page.getByRole("cell", { name: "02:00:00:aa:bb:01" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "Guest WiFi" })).toBeVisible();
    // Treat the typed room as unverified input, not guest identity.
    await expect(page.getByRole("columnheader", { name: /last room typed \(unverified\)/i })).toBeVisible();
    await expect(page.getByText(/5 incorrect sign-ins/i)).toBeVisible();
    await expect(page.getByText(/4[0-9]s/)).toBeVisible();
  });

  test("release needs a reason and is presented as another attempt, not as access", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/guest-signin-attempts");
    await page.getByRole("tab", { name: /active restrictions/i }).click();

    await page.getByRole("button", { name: /^release$/i }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/it is not being given access/i)).toBeVisible();
    await expect(dialog.getByText(/releasing does not sign the guest in/i)).toBeVisible();
    await expect(dialog.getByText(/unverified — what was typed/i)).toBeVisible();

    const submit = dialog.getByRole("button", { name: "Release", exact: true });
    await expect(submit).toBeDisabled();
    await dialog.getByLabel(/reason/i).fill("ab");
    await expect(submit).toBeDisabled();
    await dialog.getByLabel(/reason/i).fill("guest confirmed at the desk");
    await expect(submit).toBeEnabled();
    await submit.click();

    await expect.poll(() => mutations.length).toBeGreaterThan(0);
    expect(mutations[0].path).toContain("/guest-signin-restrictions/restr-1/release");
    expect(mutations[0].body.reason).toBe("guest confirmed at the desk");
    await expect(page.getByText(/it has not been given access/i)).toBeVisible();
  });

  test("a role that cannot release is offered no Release button", async ({ page }) => {
    await installBackend(page, [], ["site_viewer"]);
    await page.goto("/guest-signin-attempts");
    await page.getByRole("tab", { name: /active restrictions/i }).click();

    await expect(page.getByRole("cell", { name: "02:00:00:aa:bb:01" })).toBeVisible();
    await expect(page.getByRole("button", { name: /^release$/i })).toHaveCount(0);
  });

  test("an empty list says devices leave on their own", async ({ page }) => {
    await installBackend(page, [], ["site_admin"], { restrictions: [] });
    await page.goto("/guest-signin-attempts");
    await page.getByRole("tab", { name: /active restrictions/i }).click();

    await expect(page.getByText(/no device is being asked to wait/i)).toBeVisible();
    await expect(page.getByText(/leave on their own when the wait ends/i)).toBeVisible();
  });
});
