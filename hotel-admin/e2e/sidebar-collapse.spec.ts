import { test, expect, type Page, type Route } from "@playwright/test";

// THE COLLAPSIBLE DESKTOP SIDEBAR.
//
// The edged backend is mocked at the network layer; no real backend, DB, appliance or guest data is touched.
//
// What these specs are actually defending. A collapsed sidebar is easy to build in a way that LOOKS right and
// is unusable: labels that vanish with no tooltip, a toggle whose accessible name never changes, a preference
// that resets on the next navigation, a rail that appears on a phone where the labels are unguessable, or a
// width that snaps on every page load because it is decided by React instead of before paint.

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
}

const collapseBtn = (page: Page) => page.getByRole("button", { name: "Collapse sidebar" });
const expandBtn = (page: Page) => page.getByRole("button", { name: "Expand sidebar" });
/** The DESKTOP column, not the drawer: the drawer renders a second <aside> with the same content. */
const desktopAside = (page: Page) => page.locator("aside").first();

test.beforeEach(async ({ page }) => {
  await installBackend(page);
  await page.setViewportSize(DESKTOP);
});

test("expanded is the first-time default, and the control names the action it performs", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(page.getByRole("link", { name: "Internet packages" })).toBeVisible();
  // The section headings are part of the expanded design and must survive.
  await expect(desktopAside(page).getByText("Property management system", { exact: true })).toBeVisible();

  const btn = collapseBtn(page);
  await expect(btn).toBeVisible();
  // The NAME states what activating it does; the STATE is carried by aria-expanded. Naming the button after
  // the current state is the common mistake and leaves a screen-reader user guessing what will happen.
  await expect(btn).toHaveAttribute("aria-expanded", "true");
});

test("collapsing hides labels and headings, keeps every destination, and flips the control's name", async ({ page }) => {
  await page.goto("/dashboard");
  // Wait for the column to exist. The layout renders a loading skeleton with NO <aside> while it fetches
  // whoami, so counting immediately after goto() counts zero and the comparison is meaningless.
  await expect(collapseBtn(page)).toBeVisible();
  // Count the links IN THE COLUMN. Counting page-wide folds in the dashboard's own links, which
  // have nothing to do with whether collapsing dropped a destination.
  const before = await desktopAside(page).getByRole("link").count();

  await collapseBtn(page).click();

  await expect(expandBtn(page)).toBeVisible();
  await expect(expandBtn(page)).toHaveAttribute("aria-expanded", "false");
  // Section headings are gone from the column entirely -- not merely hidden, not relocated.
  await expect(desktopAside(page).getByText("Property management system", { exact: true })).toHaveCount(0);
  // But no destination was removed: the labels are still the accessible names, merely not painted.
  expect(await desktopAside(page).getByRole("link").count()).toBe(before);
  await expect(desktopAside(page).getByRole("link", { name: "Internet packages" })).toHaveCount(1);
});

test("every icon-only item exposes its label on hover AND on keyboard focus", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();

  const link = page.getByRole("link", { name: "Guest sign-in checks" });
  await link.hover();
  await expect(page.getByRole("tooltip")).toContainText("Guest sign-in checks");

  // Keyboard focus, not just hover. A rail whose labels are mouse-only is not navigable.
  await page.keyboard.press("Escape");
  await link.focus();
  await expect(page.getByRole("tooltip")).toContainText("Guest sign-in checks");
});

test("the choice survives navigation, a reload, and a later session", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();
  await expect(expandBtn(page)).toBeVisible();

  // Across a client-side navigation into a NESTED route.
  await page.getByRole("link", { name: "Guest networks" }).click();
  await expect(page).toHaveURL(/\/network$/);
  await expect(expandBtn(page)).toBeVisible();

  // Across a full reload, on a nested path.
  await page.goto("/network/dhcp");
  await expect(expandBtn(page)).toBeVisible();

  // And it is real persistence, not in-memory state.
  expect(await page.evaluate(() => localStorage.getItem("stayconnect-admin-sidebar"))).toBe("collapsed");

  // Expanding returns to the default and leaves no residue behind.
  await expandBtn(page).click();
  await expect(collapseBtn(page)).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("stayconnect-admin-sidebar"))).toBeNull();
});

test("the width is applied before paint, so a collapsed operator sees no jump", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();
  await expect(desktopAside(page)).toHaveCSS("width", "56px");   // --sidebar-width-rail, settled
  const railWidth = await desktopAside(page).evaluate((el) => el.getBoundingClientRect().width);

  // A fresh load with the preference already stored. If the width were React state the server HTML would be
  // the expanded column and this attribute would be absent on the first frame.
  await page.goto("/sessions");
  expect(await page.evaluate(() => document.documentElement.getAttribute("data-sidebar"))).toBe("collapsed");
  await expect(desktopAside(page)).toHaveCSS("width", "56px");
  expect(await desktopAside(page).evaluate((el) => el.getBoundingClientRect().width)).toBeCloseTo(railWidth, 0);
});

test("the main content reclaims the width, with one gutter and no horizontal scroll", async ({ page }) => {
  await page.goto("/dashboard");
  const wide = await page.locator("main > div").evaluate((el) => el.getBoundingClientRect().width);

  await collapseBtn(page).click();
  await expect(desktopAside(page)).toHaveCSS("width", "56px");
  const wider = await page.locator("main > div").evaluate((el) => el.getBoundingClientRect().width);
  expect(wider).toBeGreaterThan(wide);

  // The shared gutter is preserved -- the content must not become flush with the rail.
  const pad = await page.locator("main > div").evaluate((el) => getComputedStyle(el).paddingLeft);
  expect(parseFloat(pad)).toBeGreaterThan(0);
  // The page itself never scrolls sideways.
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
  await expect(page.getByRole("link", { name: "Network routing" })).toBeVisible();
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
  // The identity is still announced, and still reachable from the keyboard.
  await expect(page.getByRole("img", { name: /Signed in as admin@test\.local/ })).toBeVisible();
});

test("the active page stays visually clear in the rail", async ({ page }) => {
  await page.goto("/sessions");
  await collapseBtn(page).click();
  const active = page.getByRole("link", { name: "Active sessions" });
  await expect(active).toHaveAttribute("aria-current", "page");
  // aria-current is the semantic half; the visible half is the accent surface, which must differ from a
  // neighbour that is not current -- otherwise the rail says nothing about where you are.
  const bg = (l: ReturnType<typeof page.getByRole>) =>
    l.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(await bg(active)).not.toBe(await bg(page.getByRole("link", { name: "Stays" })));
});

test("an operator who asked for reduced motion gets none", async ({ page }) => {
  // page.emulateMedia, NOT the `reducedMotion` fixture option: with this project's config the fixture option
  // did not reach the page (matchMedia reported false), so a spec written that way would have asserted
  // against an un-emulated browser and passed or failed for the wrong reason.
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/dashboard");
  await expect(collapseBtn(page)).toBeVisible();

  // Effectively zero, not merely shorter: the setting means do not animate.
  const duration = await desktopAside(page).evaluate((el) => getComputedStyle(el).transitionDuration);
  expect(parseFloat(duration)).toBeLessThan(0.01);

  // And it still WORKS -- removing the animation must not remove the behaviour.
  await collapseBtn(page).click();
  await expect(expandBtn(page)).toBeVisible();
  await expect(desktopAside(page)).toHaveCSS("width", "56px");
});

test("motion is restrained by default, not absent", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(collapseBtn(page)).toBeVisible();
  const duration = await desktopAside(page).evaluate((el) => getComputedStyle(el).transitionDuration);
  // Long enough to read as a state change, short enough not to be an effect.
  expect(parseFloat(duration)).toBeGreaterThan(0);
  expect(parseFloat(duration)).toBeLessThanOrEqual(0.3);
});

test("mobile keeps the labelled drawer and never shows the desktop rail", async ({ page }) => {
  await page.setViewportSize(MOBILE);
  await page.goto("/dashboard");

  // No collapse control on a phone: there is no desktop column whose width could be reclaimed.
  await expect(collapseBtn(page)).toHaveCount(0);
  await expect(expandBtn(page)).toHaveCount(0);

  await page.getByRole("button", { name: "Open navigation" }).click();
  // The drawer is the LABELLED list, exactly as before this feature existed.
  const drawer = page.getByLabel("Navigation", { exact: true });
  await expect(drawer.getByRole("link", { name: "Duplicate sources" })).toBeVisible();
  await expect(drawer.getByText("Property management system", { exact: true })).toBeVisible();
});

test("a stored collapse does not leak into the mobile drawer", async ({ page }) => {
  await page.goto("/dashboard");
  await collapseBtn(page).click();

  await page.setViewportSize(MOBILE);
  await page.goto("/dashboard");
  await page.getByRole("button", { name: "Open navigation" }).click();
  // Still labelled: the rail is a desktop affordance and the preference must not follow it to a phone, where
  // these labels are not guessable from an icon.
  await expect(page.getByRole("link", { name: "Checkout grace" })).toBeVisible();
});
