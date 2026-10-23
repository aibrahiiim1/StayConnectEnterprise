import { test, expect, type Page, type Route } from "@playwright/test";

// IAM-v2 credentials have no template_id; package eligibility determines available access.
const IAMV2_ACCOUNT = {
  id: "b1e57ca4-aa9c-4b97-9fc0-0b31b3e11eda",
  username: "devguest2",
  display_name: "DEVELOPMENT Guest Two",
  enabled: true,
  login_count: 0,
  active_devices: 0,
  authority: "iam_v2",
};

async function installBackend(page: Page, authority: "iam_v2", accounts: unknown[]) {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts") return route.fulfill(json(200, { data: accounts, meta: { has_more: false }, authority }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

test("guest accounts renders IAM-v2 accounts that carry no template_id", async ({ page }) => {
  const crashes: string[] = [];
  page.on("pageerror", (e) => crashes.push(String(e)));
  await installBackend(page, "iam_v2", [IAMV2_ACCOUNT]);
  await page.goto("/guest-accounts");
  await expect(page.getByText("devguest2")).toBeVisible();
  expect(crashes, "the page must not throw on an account with no template_id").toEqual([]);
});

// Match the longest navigation path so nested routes do not highlight sibling items.
test("exactly one navigation item is active, including on nested network routes", async ({ page }) => {
  await installBackend(page, "iam_v2", []);
  for (const path of ["/network/dhcp", "/network/system", "/network", "/dashboard", "/guest-accounts"]) {
    await page.goto(path);
    const active = page.locator('aside nav a[aria-current="page"]');
    await expect(active, `exactly one active item on ${path}`).toHaveCount(1);
  }
  await page.goto("/network/dhcp");
  await expect(page.locator('aside nav a[aria-current="page"]')).toHaveText(/DHCP/i);
});

// Bound the sidebar to the viewport so navigation does not reset its apparent scroll position.
test("the sidebar scrolls independently and the window does not scroll", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 700 });
  await installBackend(page, "iam_v2", []);
  await page.goto("/dashboard");
  // Wait for the permanent desktop column after whoami replaces the skeleton.
  const navEl = page.locator("aside nav").first();
  await navEl.waitFor({ state: "visible" });
  const closed = navEl.locator('h2 > button[aria-expanded="false"]');
  while (await closed.count()) await closed.first().click();
  const m = await navEl.evaluate((nav) => ({
    navScrollable: nav.scrollHeight > nav.clientHeight + 1,
    windowScrollable: document.documentElement.scrollHeight > window.innerHeight + 1,
  }));
  expect(m.navScrollable, "the sidebar must be its own scrolling column").toBe(true);
  expect(m.windowScrollable, "the window must not scroll -- that is what moved the menu").toBe(false);
});

test("a failed load says so, offers a retry, and does not claim to still be checking", async ({ page }) => {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  let fail = true;
  await page.route("**/api/edge/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts") {
      if (fail) return route.fulfill(json(500, { error: "internal", message: "list failed" }));
      return route.fulfill(json(200, { data: [IAMV2_ACCOUNT], meta: { has_more: false }, authority: "iam_v2" }));
    }
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });

  await page.goto("/guest-accounts");
  await expect(page.getByText(/Could not load client accounts/i)).toBeVisible();
  await expect(page.getByText("Loading…"), "a failed load must not also claim to be loading").toHaveCount(0);

  await page.getByRole("button", { name: /add account/i }).click();
  await expect(page.getByText(/eligibility rules on each internet package/i).first()).toBeVisible();
  const body = (await page.locator("body").innerText()).replace(/\s+/g, " ");
  for (const rx of [/bound to a Guest Access Plan/i, /create one first/i, /guest access plan/i,
                    /Checking how this site decides guest access/i]) {
    expect(body, `a failed load must not claim ${rx}`).not.toMatch(rx);
  }

  // Dismiss the modal before checking the list behind its inert overlay.
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  fail = false;
  await page.getByRole("button", { name: /try again/i }).click();
  await expect(page.getByText("devguest2")).toBeVisible();
  await expect(page.getByText(/Could not load client accounts/i)).toHaveCount(0);
  await expect(page.getByText(/list failed/i), "the previous error must not linger").toHaveCount(0);
});

test("the plan picker is absent, and there is no authority under which it returns", async ({ page }) => {
  await installBackend(page, "iam_v2", [IAMV2_ACCOUNT]);
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /add account/i }).click();
  await expect(page.locator('select[name="template_id"]')).toHaveCount(0);
  await expect(page.getByText(/eligibility rules on each internet package/i).first()).toBeVisible();
});

test("the screen never requests the removed plans resource, at any point in its load", async ({ page }) => {
  const calls: string[] = [];
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    calls.push(path);
    const json = (status: number, body: unknown) =>
      ({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts")
      return route.fulfill(json(200, { data: [IAMV2_ACCOUNT], meta: { has_more: false }, authority: "iam_v2" }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(404, { error: "not_found" }));
  });
  await page.goto("/guest-accounts");
  await expect(page.getByText("devguest2")).toBeVisible();
  await page.getByRole("button", { name: /add account/i }).click();
  await expect(page.getByText(/eligibility rules on each internet package/i).first()).toBeVisible();
  expect(calls, "the removed plans resource must never be requested")
    .not.toContain("/guest-access-plans");
});

test("no plan-bound wording or create-a-plan guidance appears anywhere, including during the load", async ({ page }) => {
  let release: () => void = () => {};
  const held = new Promise<void>((r) => { release = r; });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    const json = (status: number, body: unknown) =>
      ({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts") {
      await held;
      return route.fulfill(json(200, { data: [IAMV2_ACCOUNT], meta: { has_more: false }, authority: "iam_v2" }));
    }
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /add account/i }).click();
  const banned = /bound to a Guest Access Plan|create one first|guest access plan/i;
  await expect(page.getByText(banned)).toHaveCount(0);
  release();
  await expect(page.getByText("devguest2")).toBeVisible();
  await expect(page.getByText(banned)).toHaveCount(0);
});
