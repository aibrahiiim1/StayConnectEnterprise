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

// installBackend no longer takes an authority. There is one, and the envelope still carries it so an older
// client can tell what served it, but the screen has no second personality to select.
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
  await expect(page.getByText(/Could not load guest accounts/i)).toBeVisible();
  await expect(page.getByText("Loading…"), "a failed load must not also claim to be loading").toHaveCount(0);

  // The form must still be usable and must still imply no plan prerequisite. The "checking how this site
  // decides guest access" states are gone with the authority branch they described -- there is nothing left
  // to check -- so what is asserted here is that no wording came back with the failure.
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.getByText(/package eligibility rules/i).first()).toBeVisible();
  const body = (await page.locator("body").innerText()).replace(/\s+/g, " ");
  for (const rx of [/bound to a Guest Access Plan/i, /create one first/i, /guest access plan/i,
                    /Checking how this site decides guest access/i]) {
    expect(body, `a failed load must not claim ${rx}`).not.toMatch(rx);
  }

  // Retrying recovers, and the stale failure banner does not survive the recovery.
  fail = false;
  await page.getByRole("button", { name: /try again/i }).click();
  await expect(page.getByText("devguest2")).toBeVisible();
  await expect(page.getByText(/Could not load guest accounts/i)).toHaveCount(0);
  await expect(page.getByText(/list failed/i), "the previous error must not linger").toHaveCount(0);
});

// THE LEGACY DIMENSION NO LONGER EXISTS, AND THAT IS WHAT THESE ASSERT.
//
// Twelve tests used to live here covering the legacy "guest access plans" lifecycle: picker present under
// legacy, absent under IAM-v2, four distinguishable states for the plans request, prose that differed by
// authority, and a window before the authority resolved in which neither could be claimed. Every one of them
// described a superseded implementation that has been removed from the product.
//
// They are replaced rather than deleted, because "the branch is gone" is a property worth pinning: a
// reintroduced plan control, a reintroduced plans request, or a reintroduced authority-specific sentence
// would each fail one of these.

test("the plan picker is absent, and there is no authority under which it returns", async ({ page }) => {
  await installBackend(page, "iam_v2", [IAMV2_ACCOUNT]);
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.locator('select[name="template_id"]')).toHaveCount(0);
  await expect(page.getByText(/package eligibility rules/i).first()).toBeVisible();
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
  await page.getByRole("button", { name: /new account/i }).click();
  await expect(page.getByText(/package eligibility rules/i).first()).toBeVisible();
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
      // Held open on purpose: the old screen rendered the LEGACY prose during exactly this window, because
      // its structural default was the legacy answer until the first list returned.
      await held;
      return route.fulfill(json(200, { data: [IAMV2_ACCOUNT], meta: { has_more: false }, authority: "iam_v2" }));
    }
    if (path === "/guest-accounts/portal") return route.fulfill(json(200, { enabled: true }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await page.goto("/guest-accounts");
  await page.getByRole("button", { name: /new account/i }).click();
  const banned = /bound to a Guest Access Plan|create one first|guest access plan/i;
  await expect(page.getByText(banned)).toHaveCount(0);
  release();
  await expect(page.getByText("devguest2")).toBeVisible();
  await expect(page.getByText(banned)).toHaveCount(0);
});
