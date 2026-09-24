import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML } from "./portal-page";

// THE PORTAL'S TEMPLATES, IN A REAL BROWSER.
//
// A template is presentation over ONE page: the same forms, ids and script under every layout. What only a
// browser can prove is that each layout actually leaves those forms VISIBLE and SUBMITTABLE -- at desktop width
// and on a 390px phone -- and that the cascade guard holds: a hotel stylesheet that tries to hide the sign-in
// (form{display:none!important} and friends) restyles nothing that matters.
//
// The page is portald's real template (./portal-page renders templates.go), served through page.route with
// the appliance's answers stubbed. No appliance, no database, no guest data.

const TEMPLATES = ["classic", "split", "immersive", "headerbar", "editorial", "kiosk"] as const;

const VIEWPORTS = [
  { name: "desktop", width: 1280, height: 900 },
  { name: "phone", width: 390, height: 844 },
] as const;

/** A stylesheet written to break the sign-in. The portal removes !important on the server; the page removes it
 *  again and wraps the rest in the hotel's layer, under the unlayered guard. */
const HOSTILE_CSS = `
form { display: none !important; }
.panels, .sc-signin { visibility: hidden !important; height: 0 !important; overflow: hidden !important; }
#tabs, .tab { display: none !important; opacity: 0 !important; }
input { opacity: 0 !important; pointer-events: none !important; }
button.primary { pointer-events: none !important; transform: scale(0) !important; }
main.card { display: none !important; }
body::after { content: ""; position: fixed; inset: 0; background: #fff; z-index: 2147483647; }
`;

type Posted = { path: string; body: string };

async function serve(page: Page, template: string, design: Record<string, unknown>, posted: Posted[]) {
  const html = portalHTML(template);
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/branding", (r: Route) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ design: { template_id: template, ...design } }) }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        pms: { enabled: true, mode: "room_any" }, phase3_pms: true,
        voucher: { enabled: true }, guest_account: { enabled: true }, internet_packages_available: true,
      }),
    }));
  await page.route("**/access/status", (r: Route) =>
    r.fulfill({ status: 200, contentType: "application/json", body: "{}" }));
  await page.route("**/auth/pms/phase3", (r: Route) => {
    posted.push({ path: "/auth/pms/phase3", body: r.request().postData() ?? "" });
    return r.fulfill({ status: 200, contentType: "application/json",
      body: JSON.stringify({ ok: false, message: "We could not verify your stay. Please check your details or contact reception." }) });
  });
  await page.route("**/auth/voucher", (r: Route) => {
    posted.push({ path: "/auth/voucher", body: r.request().postData() ?? "" });
    return r.fulfill({ status: 200, contentType: "text/html", body: "<h1 id=done>voucher received</h1>" });
  });
}

/** The guest's two doors, each filled in and submitted the way a guest would. */
async function signInBothWays(page: Page, posted: Posted[]) {
  // Guest Login (room) is the first group and opens by default.
  const room = page.locator("#pms-room");
  await expect(room).toBeVisible();
  await expect(page.locator("#form-pms button[type=submit]")).toBeVisible();
  await room.fill("412");
  await page.locator("#pms-secondary").fill("Okonkwo");
  await page.locator("#form-pms button[type=submit]").click();
  await expect(page.locator("#pms-err")).toContainText("could not verify");
  expect(posted.some((p) => p.path === "/auth/pms/phase3" && p.body.includes('"room":"412"'))).toBe(true);

  // Account Login: the voucher form, a plain HTML POST.
  await page.locator('.tab[data-group="account"]').click();
  const voucher = page.locator("#voucher");
  await expect(voucher).toBeVisible();
  await voucher.fill("ABCD-1234");
  await page.locator("#form-voucher button[type=submit]").click();
  await expect(page.locator("#done")).toHaveText("voucher received");
  expect(posted.some((p) => p.path === "/auth/voucher" && p.body.includes("code=ABCD-1234"))).toBe(true);
}

for (const vp of VIEWPORTS) {
  for (const tpl of TEMPLATES) {
    test(`${tpl} at ${vp.name}: both sign-in forms are visible and submit`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      const posted: Posted[] = [];
      await serve(page, tpl, {
        hotel_name: "Coral Sea Holiday Resort",
        welcome_text: "Welcome — connect to our Wi-Fi",
        help_text: "Ask reception if you need a code.",
        custom_html: '<section><h3>Pool</h3><p>08:00–20:00</p></section><section><h3>Spa</h3><p>10:00–22:00</p></section>',
      }, posted);
      await page.goto("http://localhost/portal");
      await expect(page.locator("html")).toHaveAttribute("data-template", tpl);

      // No horizontal scroll on a phone: the page is exactly as wide as the screen.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(overflow, "the page scrolls sideways").toBeLessThanOrEqual(1);

      if (process.env.PORTAL_SHOTS) {
        await page.waitForTimeout(300);
        await page.screenshot({ path: `${process.env.PORTAL_SHOTS}/${tpl}-${vp.name}.png`, fullPage: true });
      }
      await signInBothWays(page, posted);
    });

    test(`${tpl} at ${vp.name}: a hostile stylesheet cannot hide the sign-in`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      const posted: Posted[] = [];
      await serve(page, tpl, { hotel_name: "Coral Sea", custom_css: HOSTILE_CSS }, posted);
      await page.goto("http://localhost/portal");
      // The hotel sheet really was applied -- in its layer -- so the assertion below is about the guard.
      await expect(page.locator("style#sc-hotel")).toHaveCount(1);
      const css = await page.locator("style#sc-hotel").textContent();
      expect(css).toContain("@layer hotel");
      expect(css).not.toMatch(/!\s*important/i);
      await expect(page.locator(".tab").first()).toBeVisible();
      await signInBothWays(page, posted);
    });
  }
}

const OPTION_VARIANTS: { tpl: string; options: Record<string, unknown> }[] = [
  { tpl: "split", options: { panel_position: "start", overlay: 60 } },
  { tpl: "immersive", options: { panel_position: "start" } },
  { tpl: "immersive", options: { panel_position: "center", surface: "solid" } },
  { tpl: "editorial", options: { hero_height: "tall", heading_font: "Georgia, serif" } },
  { tpl: "headerbar", options: { density: "compact" } },
  { tpl: "kiosk", options: { density: "spacious" } },
];

for (const v of OPTION_VARIANTS) {
  test(`${v.tpl} with ${JSON.stringify(v.options)}: the sign-in still works`, async ({ page }) => {
    for (const vp of VIEWPORTS) {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      const posted: Posted[] = [];
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await serve(page, v.tpl, { hotel_name: "Coral Sea Holiday Resort", welcome_text: "Welcome", template_options: v.options }, posted);
      await page.goto("http://localhost/portal");
      if (process.env.PORTAL_SHOTS) {
        const tag = Object.values(v.options).join("-").replace(/[^a-z0-9-]/gi, "");
        await page.screenshot({ path: `${process.env.PORTAL_SHOTS}/${v.tpl}-${tag}-${vp.name}.png`, fullPage: true });
      }
      await signInBothWays(page, posted);
    }
  });
}

for (const tpl of TEMPLATES) {
  test(`${tpl} in Arabic: the page reads right to left and the forms stay on screen`, async ({ page, context }) => {
    await context.addCookies([{ name: "sc-lang", value: "ar", url: "http://localhost" }]);
    for (const vp of VIEWPORTS) {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      const posted: Posted[] = [];
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await serve(page, tpl, { hotel_name: "Coral Sea", welcome_text: "مرحبا" }, posted);
      await page.goto("http://localhost/portal");
      await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
      const box = await page.locator("#pms-room").boundingBox();
      expect(box, "the room field has no box").not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(vp.width + 1);
      if (process.env.PORTAL_SHOTS) {
        await page.screenshot({ path: `${process.env.PORTAL_SHOTS}/${tpl}-${vp.name}-rtl.png`, fullPage: true });
      }
    }
  });
}

test("the guard does not fight the page's own switches", async ({ page }) => {
  // The guard is unlayered but NOT !important, so the sign-in script's inline show/hide still wins: flipping
  // to the personal account hides the voucher form and shows the credentials form, under every template.
  for (const tpl of TEMPLATES) {
    const posted: Posted[] = [];
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await serve(page, tpl, { custom_css: HOSTILE_CSS }, posted);
    await page.goto("http://localhost/portal");
    await page.locator('.tab[data-group="account"]').click();
    await expect(page.locator("#form-voucher")).toBeVisible();
    await page.locator("#use-personal").check();
    await expect(page.locator("#form-voucher")).toBeHidden();
    await expect(page.locator("#ga-username")).toBeVisible();
  }
});
