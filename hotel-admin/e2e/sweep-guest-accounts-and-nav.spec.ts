import { test, expect, type Page, type Route } from "@playwright/test";

// REGRESSIONS FROM THE POST-ACCEPTANCE HARDENING SWEEP (D33/T0072).
//
// The edged backend is mocked at the network layer; no real backend, DB or production data is touched.
// Every fixture below is the shape the REAL appliance returned, not an invented one.

// An IAM-v2 guest account as edged actually serves it. Note what is ABSENT: template_id.
// Under IAM-v2 a credential carries no plan at all -- what a guest may acquire is decided by package
// eligibility rules -- so the field the legacy UI treated as mandatory simply does not exist here.
const IAMV2_ACCOUNT = {
  id: "b1e57ca4-aa9c-4b97-9fc0-0b31b3e11eda",
  username: "devguest2",
  display_name: "DEVELOPMENT Guest Two",
  enabled: true,
  login_count: 0,
  active_devices: 0,
  authority: "iam_v2",
};

const PLAN = {
  id: "039666d3-118e-4e58-b687-418dddd761f1",
  code: "dev-1h", name: "DEVELOPMENT 1 Hour",
  duration_seconds: 3600, down_kbps: 2048, up_kbps: 1024,
  max_concurrent_devices: 2, is_active: true,
};

async function installBackend(page: Page, authority: "iam_v2" | "legacy", accounts: unknown[]) {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts") return route.fulfill(json(200, { data: accounts, meta: { has_more: false }, authority }));
    if (path === "/guest-access-plans") return route.fulfill(json(200, { data: [PLAN], meta: { has_more: false } }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

// THE CRASH. The screen dereferenced template_id unconditionally, so the first IAM-v2 account blanked
// the whole page with "Cannot read properties of undefined (reading 'slice')". An operator saw
// "Application error: a client-side exception has occurred" and nothing else -- no accounts, no way in.
test("guest accounts renders IAM-v2 accounts that carry no template_id", async ({ page }) => {
  const crashes: string[] = [];
  page.on("pageerror", (e) => crashes.push(String(e)));
  await installBackend(page, "iam_v2", [IAMV2_ACCOUNT]);
  await page.goto("/guest-accounts");
  await expect(page.getByText("devguest2")).toBeVisible();
  expect(crashes, "the page must not throw on an account with no template_id").toEqual([]);
});

// THE DEAD CONTROL. Under IAM-v2 the backend ignores template_id entirely, so a required "Guest access
// plan" picker let an operator believe they had chosen what the guest gets. Absence is the assertion.
test("the plan picker is absent under IAM-v2 authority and present under legacy", async ({ page }) => {
  await installBackend(page, "iam_v2", [IAMV2_ACCOUNT]);
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.locator('select[name="template_id"]')).toHaveCount(0);
  await expect(page.getByText(/package eligibility rules/i)).toBeVisible();

  // The same screen against a legacy site: the plan IS real there, and must still be offered.
  await page.unrouteAll({ behavior: "ignoreErrors" });
  await installBackend(page, "legacy", [{ ...IAMV2_ACCOUNT, authority: undefined, template_id: PLAN.id }]);
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.locator('select[name="template_id"]')).toHaveCount(1);
});

// THE DOUBLE HIGHLIGHT. `path.startsWith(href)` marked every ancestor-looking item active, and these are
// siblings rather than parent/child: "/network" is the Guest networks LEAF, so standing on "/network/dhcp"
// lit up both it and "DHCP & leases". The highlight is the only thing telling an operator where they are.
test("exactly one navigation item is active, including on nested network routes", async ({ page }) => {
  await installBackend(page, "iam_v2", []);
  for (const path of ["/network/dhcp", "/network/system", "/network", "/dashboard", "/guest-accounts"]) {
    await page.goto(path);
    const active = page.locator("aside nav a.bg-panel2");
    await expect(active, `exactly one active item on ${path}`).toHaveCount(1);
  }
  await page.goto("/network/dhcp");
  await expect(page.locator("aside nav a.bg-panel2")).toHaveText(/DHCP/i);
});

// THE "MENU JUMPS BACK TO THE TOP". The menu never moved: `min-h-screen` let the whole page grow past the
// viewport, so the sidebar had no bounded height, its overflow-y-auto never engaged, and reaching a lower
// item meant scrolling the WINDOW. Navigating then reset the window to the top, as it should, and the menu
// appeared to snap back. The fix bounds the layout to the viewport, so the window must not scroll at all.
test("the sidebar scrolls independently and the window does not scroll", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 700 });
  await installBackend(page, "iam_v2", []);
  await page.goto("/dashboard");
  // The layout renders "Loading…" until whoami resolves, so the sidebar does not exist yet; without this
  // wait the measurement below runs against a page that has no <aside> and fails for the wrong reason.
  await page.locator("aside nav").waitFor({ state: "attached" });
  const m = await page.evaluate(() => {
    const nav = document.querySelector("aside nav")!;
    return {
      navScrollable: nav.scrollHeight > nav.clientHeight + 1,
      windowScrollable: document.documentElement.scrollHeight > window.innerHeight + 1,
    };
  });
  expect(m.navScrollable, "the sidebar must be its own scrolling column").toBe(true);
  expect(m.windowScrollable, "the window must not scroll -- that is what moved the menu").toBe(false);
});

// ZERO PLANS, IAM-v2: the screen must not depend on the legacy resource at all.
//
// The plans call used to sit in the same Promise.all as the accounts, so a site with no plans -- or an
// operator whose role cannot read that resource, or any failure of the legacy endpoint -- rendered the whole
// Guest Accounts page as "Failed to load", with the accounts already fetched and sitting unused. Under
// IAM-v2 the answer is not merely optional, it is irrelevant: a credential carries no plan.
test("IAM-v2 guest accounts work with zero plans, and never call the legacy plans endpoint", async ({ page }) => {
  const calls: string[] = [];
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    calls.push(path);
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts")
      return route.fulfill(json(200, { data: [IAMV2_ACCOUNT], meta: { has_more: false }, authority: "iam_v2" }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    // If the screen asks for plans anyway, answer the way a plan-less site does -- and the assertion below
    // still fails, because asking at all is the defect.
    if (path === "/guest-access-plans") return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await page.goto("/guest-accounts");
  await expect(page.getByText("devguest2")).toBeVisible();
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.getByRole("button", { name: /create account/i })).toBeEnabled();
  expect(calls, "the IAM-v2 screen must not request the legacy plans resource")
    .not.toContain("/guest-access-plans");
});

// ...and the page must survive that endpoint FAILING, on a site where plans do apply.
test("a failing plans endpoint degrades the plan column, not the whole screen", async ({ page }) => {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts")
      return route.fulfill(json(200, {
        data: [{ ...IAMV2_ACCOUNT, authority: undefined, template_id: PLAN.id }],
        meta: { has_more: false }, authority: "legacy",
      }));
    if (path === "/guest-access-plans") return route.fulfill(json(500, { error: "internal", message: "boom" }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await page.goto("/guest-accounts");
  await expect(page.getByText("devguest2"), "the accounts loaded and must still be shown").toBeVisible();
});

// LEGACY WITH ZERO PLANS: a `required` <select> with no options cannot be satisfied, so the form refused to
// submit and said nothing. On a legacy site the plan really is a prerequisite, so name it.
test("legacy with no plans names the missing prerequisite instead of offering a dead form", async ({ page }) => {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "a@t.local", roles: ["site_admin"] }));
    if (path === "/guest-accounts")
      return route.fulfill(json(200, { data: [], meta: { has_more: false }, authority: "legacy" }));
    if (path === "/guest-access-plans") return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: false }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.getByText(/No active guest access plan exists yet/i)).toBeVisible();
  await expect(page.getByRole("button", { name: /create account/i })).toBeDisabled();
});
