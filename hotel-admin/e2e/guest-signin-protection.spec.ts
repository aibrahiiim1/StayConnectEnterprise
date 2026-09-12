import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for guest sign-in protection: the three settings, and the Active restrictions tab.
// edged is fully mocked at the network layer — no real backend, no database, no production data, no PMS.
//
// What a browser proves that a unit test cannot. Three things, all of them properties of the rendered page:
//
//   * that each setting arrives with its UNIT and its allowed range visible, because "60" has been read as
//     minutes and a threshold a property cannot interpret is a threshold it will set wrongly;
//   * that the screen says out loud that a change applies to what happens NEXT — somebody shortening the
//     wait to help the guest in front of them will otherwise watch nothing happen and call it broken;
//   * that Release is presented as "may try again", not as "let them in", and costs a typed reason.
//
// NO REAL GUEST DATA APPEARS HERE. Every room, address and network name is invented.

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
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
    { name: "sc_edge_session", value: "e2e-test", url: "http://localhost:3123" },
  ]);
}

test.describe("guest sign-in protection settings", () => {
  test("each value arrives with its unit, its standard and its allowed range", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/sign-in-methods");

    const card = page.locator("div").filter({ hasText: /guest sign-in protection/i }).first();
    await expect(page.getByText(/guest sign-in protection/i).first()).toBeVisible();

    // The units are in the LABELS, not only in the help text.
    await expect(page.getByText(/maximum failed attempts.*\(attempts\)/i)).toBeVisible();
    await expect(page.getByText(/observation window.*\(seconds\)/i)).toBeVisible();
    await expect(page.getByText(/wait after too many attempts.*\(seconds\)/i)).toBeVisible();

    // The standard value and the range an operator may choose within.
    await expect(page.getByText(/standard: 5\. allowed: 3–20/i)).toBeVisible();
    await expect(page.getByText(/standard: 60\. allowed: 30–3600/i).first()).toBeVisible();

    // "Using the standard settings" must not read as "protection is off".
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

    // Correcting it makes the save available again, and the value that leaves the browser is the one typed.
    await attempts.fill("8");
    await page.getByRole("button", { name: /save protection settings/i }).click();
    await expect.poll(() => mutations.length).toBeGreaterThan(0);
    expect(mutations[0].body.max_failed_attempts).toBe(8);
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
    // The heading itself says the room is unverified. A column called "Room" would read as an identity.
    await expect(page.getByRole("columnheader", { name: /last room typed \(unverified\)/i })).toBeVisible();
    await expect(page.getByText(/5 incorrect sign-ins/i)).toBeVisible();
    // The countdown is present and is the server's number.
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
