import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const DESKTOP = { width: 1440, height: 900 };
const MOBILE = { width: 390, height: 844 };

async function installBackend(page: Page) {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
  ]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami")
      return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await installCapabilities(page); // a fully licensed hotel appliance (e2e/capabilities.ts)
}

const collapseBtn = (page: Page) => page.getByRole("button", { name: "Collapse sidebar" });
const expandBtn = (page: Page) => page.getByRole("button", { name: "Expand sidebar" });
/** Target the desktop column; the drawer renders a second aside. */
const desktopAside = (page: Page) => page.locator("aside").first();

test.beforeEach(async ({ page }) => {
  await installBackend(page);
  await page.setViewportSize(DESKTOP);
});

test("expanded is the first-time default, and the control names the action it performs", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(desktopAside(page).getByRole("link", { name: "Overview" })).toBeVisible();
  await expect(desktopAside(page).getByText("Hotel", { exact: true })).toBeVisible();

  const btn = collapseBtn(page);
  await expect(btn).toBeVisible();
  // Name the action; aria-expanded reports the state separately.
  await expect(btn).toHaveAttribute("aria-expanded", "true");
});

test("collapsing hides labels and headings, keeps every destination, and flips the control's name", async ({ page }) => {
  await page.goto("/dashboard");
  // Wait for whoami to replace the skeleton before counting sidebar links.
  await expect(collapseBtn(page)).toBeVisible();
  const closed = desktopAside(page).locator('h2 > button[aria-expanded="false"]');
  while (await closed.count()) await closed.first().click();
  const before = await desktopAside(page).getByRole("link").count();

  await collapseBtn(page).click();

  await expect(expandBtn(page)).toBeVisible();
  await expect(expandBtn(page)).toHaveAttribute("aria-expanded", "false");
  await expect(desktopAside(page).getByText("Hotel", { exact: true })).toHaveClass(/sr-only/);
  expect(await desktopAside(page).getByRole("link").count()).toBe(before);
  await expect(desktopAside(page).getByRole("link", { name: "Internet packages" })).toHaveCount(1);
});

test("every icon-only item exposes its label on hover AND on keyboard focus", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();

  const link = page.getByRole("link", { name: "Guest sign-in checks" });
  await link.hover();
  await expect(page.getByRole("tooltip")).toContainText("Guest sign-in checks");

  // Move the pointer away before testing keyboard focus; hover can race Radix tooltip dismissal.
  await page.keyboard.press("Escape");
  await page.mouse.move(900, 400);
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await link.focus();
  await expect(page.getByRole("tooltip")).toContainText("Guest sign-in checks");
});

test("the choice survives navigation, a reload, and a later session", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();
  await expect(expandBtn(page)).toBeVisible();

  await page.getByRole("link", { name: "Client networks" }).click();
  await expect(page).toHaveURL(/\/network$/);
  await expect(expandBtn(page)).toBeVisible();

  await page.goto("/network/dhcp");
  await expect(expandBtn(page)).toBeVisible();

  expect(await page.evaluate(() => localStorage.getItem("stayconnect-admin-sidebar"))).toBe("collapsed");

  await expandBtn(page).click();
  await expect(collapseBtn(page)).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("stayconnect-admin-sidebar"))).toBeNull();
});

test("the width is applied before paint, so a collapsed operator sees no jump", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();
  await expect(desktopAside(page)).toHaveCSS("width", "60px");   // --sidebar-width-rail (3.75rem), settled
  const railWidth = await desktopAside(page).evaluate((el) => el.getBoundingClientRect().width);

  // Check the first frame so React state cannot mask an expanded server render.
  await page.goto("/sessions");
  expect(await page.evaluate(() => document.documentElement.getAttribute("data-sidebar"))).toBe("collapsed");
  await expect(desktopAside(page)).toHaveCSS("width", "60px");
  expect(await desktopAside(page).evaluate((el) => el.getBoundingClientRect().width)).toBeCloseTo(railWidth, 0);
});

test("the main content reclaims the width, with one gutter and no horizontal scroll", async ({ page }) => {
  await page.goto("/dashboard");
  const wide = await page.locator("main > div").evaluate((el) => el.getBoundingClientRect().width);

  await collapseBtn(page).click();
  await expect(desktopAside(page)).toHaveCSS("width", "60px");
  const wider = await page.locator("main > div").evaluate((el) => el.getBoundingClientRect().width);
  expect(wider).toBeGreaterThan(wide);

  const pad = await page.locator("main > div").evaluate((el) => getComputedStyle(el).paddingLeft);
  expect(parseFloat(pad)).toBeGreaterThan(0);
  expect(await page.evaluate(() =>
    document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)).toBe(false);
});

test("the filter stays reachable: using it from the rail expands and focuses the input", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();

  await page.getByRole("button", { name: "Find a screen" }).click();

  await expect(collapseBtn(page)).toBeVisible();               // it expanded
  const filter = page.getByRole("textbox", { name: "Filter navigation" });
  await expect(filter).toBeFocused();                          // and focus landed in it
  await filter.fill("routing");
  await expect(page.getByRole("link", { name: "PMS routing" })).toBeVisible();
});

test("profile and Sign out remain reachable in both modes", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  await expect(page.getByText("admin@test.local")).toBeVisible();

  await collapseBtn(page).click();
  const signOut = page.getByRole("button", { name: "Sign out" });
  await expect(signOut).toBeVisible();
  await signOut.hover();
  await expect(page.getByRole("tooltip")).toContainText("Sign out");
  await expect(page.getByRole("img", { name: /Signed in as admin@test\.local/ })).toBeVisible();
});

test("the active page stays visually clear in the rail", async ({ page }) => {
  await page.goto("/sessions");
  await collapseBtn(page).click();
  const active = page.getByRole("link", { name: "Active sessions" });
  await expect(active).toHaveAttribute("aria-current", "page");
  const bg = (l: ReturnType<typeof page.getByRole>) =>
    l.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(await bg(active)).not.toBe(await bg(page.getByRole("link", { name: "Stays" })));
});

test("an operator who asked for reduced motion gets none", async ({ page }) => {
  // emulateMedia reaches this project's page where the reducedMotion fixture option does not.
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/dashboard");
  await expect(collapseBtn(page)).toBeVisible();

  const duration = await desktopAside(page).evaluate((el) => getComputedStyle(el).transitionDuration);
  expect(parseFloat(duration)).toBeLessThan(0.01);

  await collapseBtn(page).click();
  await expect(expandBtn(page)).toBeVisible();
  await expect(desktopAside(page)).toHaveCSS("width", "60px");
});

test("motion is restrained by default, not absent", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(collapseBtn(page)).toBeVisible();
  const duration = await desktopAside(page).evaluate((el) => getComputedStyle(el).transitionDuration);
  expect(parseFloat(duration)).toBeGreaterThan(0);
  expect(parseFloat(duration)).toBeLessThanOrEqual(0.3);
});

test("mobile keeps the labelled drawer and never shows the desktop rail", async ({ page }) => {
  await page.setViewportSize(MOBILE);
  await page.goto("/dashboard");

  await expect(collapseBtn(page)).toHaveCount(0);
  await expect(expandBtn(page)).toHaveCount(0);

  await page.getByRole("button", { name: "Open navigation" }).click();
  const drawer = page.getByLabel("Navigation", { exact: true });
  await drawer.getByRole("button", { name: "Hotel" }).click();
  await expect(drawer.getByRole("link", { name: "Duplicate sources" })).toBeVisible();
  await expect(drawer.getByText("Hotel", { exact: true })).toBeVisible();
});

test("a stored collapse does not leak into the mobile drawer", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();

  await page.setViewportSize(MOBILE);
  await page.goto("/dashboard");
  await page.getByRole("button", { name: "Open navigation" }).click();
  const drawer = page.getByLabel("Navigation", { exact: true });
  await drawer.getByRole("button", { name: "Hotel" }).click();
  await expect(drawer.getByRole("link", { name: "Grace Period" })).toBeVisible();
});
