import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML, shippedWording } from "./portal-page";
import { installCapabilities } from "./capabilities";

/** Use a decodable PNG; valid magic bytes alone cannot prove the browser renders it. */
const PNG = Buffer.from(
  "89504e470d0a1a0a0000000d4948445200000008000000080806000000c40fbe" +
  "8b000000124944415478da63e0cd4efe8f0f338c0c0500c4cc768141e7381700" +
  "00000049454e44ae426082", "hex");

const PORTAL_HTML = portalHTML();

let design: Record<string, unknown> = {};
let lastSave: { design: Record<string, unknown>; password?: string } | null = null;

const HOSTILE_HTML = '<p class="amenity">Pool 08:00–20:00</p><img src=x onerror="steal()">';
const CLEANED_HTML = '<p class="amenity">Pool 08:00–20:00</p><img src="x">';

async function installBackend(page: Page) {
  const json = (status: number, body: unknown) =>
    ({ status, contentType: "application/json", body: JSON.stringify(body) });

  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
  ]);

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");

    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/portal-branding" && method === "GET")
      return route.fulfill(json(200, { design, draft: {}, revisions: [], published: true }));
    if (path === "/portal-branding/draft") return route.fulfill(json(200, { saved: true }));
    if (path === "/portal-branding/validate") {
      const d = (req.postDataJSON() as { design: Record<string, string> }).design ?? {};
      const bad = d.custom_html === HOSTILE_HTML;
      return route.fulfill(json(200, {
        ok: !bad,
        issues: bad ? [{ field: "custom_html", severity: "error", message: "removed the onerror attribute on <img>: inline script is not allowed" }] : [],
        sanitized: { custom_css: d.custom_css ?? "", custom_html: bad ? CLEANED_HTML : (d.custom_html ?? "") },
      }));
    }
    if (path === "/portal-branding/settings") {
      lastSave = req.postDataJSON() as { design: Record<string, unknown>; password?: string };
      design = lastSave.design;
      return route.fulfill(json(200, { saved: true }));
    }
    if (path === "/portal-branding/preview") return route.fulfill(json(200, { html: PORTAL_HTML }));
    if (path === "/auth-methods")
      return route.fulfill(json(200, {
        pms: { enabled: true, mode: "ROOM_AND_LAST_NAME" }, phase3_pms: true,
        voucher: { enabled: true }, guest_account: { enabled: true },
      }));
    if (path === "/portal-branding/languages") return route.fulfill(json(200, shippedWording()));
    if (path === "/portal-assets" && method === "POST")
      return route.fulfill(json(200, { name: "aaaa1111bbbb2222.png", url: "/assets/aaaa1111bbbb2222.png", size_bytes: PNG.length }));
    if (path === "/portal-assets") return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
    if (path.endsWith("/raw")) return route.fulfill({ status: 200, contentType: "image/png", body: PNG });
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await installCapabilities(page); // a fully licensed hotel appliance (e2e/capabilities.ts)
}

const preview = (page: Page) => page.frameLocator('iframe[title^="Client Portal"]');
const saved = (page: Page) => expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();

test.beforeEach(() => {
  design = { hotel_name: "Semantics Demo Hotel", brand_color: "#0f6b63" };
  lastSave = null;
});

test("the designer is sections and a live preview, with no release vocabulary", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  for (const s of ["Template", "Brand", "Content", "Sign-in page text", "Languages", "Advanced HTML & CSS", "History"]) {
    await expect(page.getByRole("tab", { name: new RegExp(s) })).toBeVisible();
  }
  const text = (await page.locator("main").innerText()).toLowerCase();
  for (const word of ["draft", "publish", "rollback", "live version"]) {
    expect(text, `the page still says "${word}"`).not.toContain(word);
  }
  await expect(page.getByRole("button", { name: /Save changes/ })).toBeDisabled();
});

test("the preview renders the real guest portal, at every size", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  const frame = preview(page);
  await expect(frame.locator(".card")).toBeVisible();
  await expect(frame.locator("#brand-name")).toHaveText("Semantics Demo Hotel");
  await expect(frame.locator('#alt-methods button[data-tab="pms"]')).toBeVisible();
  await expect(frame.locator('#alt-methods button:is([data-tab="account"],[data-tab="voucher"])')).toBeVisible();

  await page.getByRole("tab", { name: /Content/ }).click();
  await page.getByLabel("Site name", { exact: true }).fill("Blue Bay Resort");
  await expect(frame.locator("#brand-name")).toHaveText("Blue Bay Resort");

  for (const size of ["Tablet", "Mobile"]) {
    await page.getByRole("radio", { name: new RegExp(size) }).click();
    await expect(frame.locator(".card")).toBeVisible();
  }
});

test("the template gallery shows the real page in each layout, and choosing one changes the preview", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  const gallery = page.getByRole("radiogroup", { name: "Page template" });
  await expect(gallery.getByRole("radio")).toHaveCount(6);
  const thumb = page.frameLocator('iframe[title="Template thumbnail: Split"]');
  await expect(thumb.locator("html")).toHaveAttribute("data-template", "split");
  await expect(thumb.locator(".sc-hero-name")).toHaveText("Semantics Demo Hotel");

  await gallery.getByText("Editorial", { exact: true }).click();
  await expect(preview(page).locator("html")).toHaveAttribute("data-template", "editorial");
  await page.getByRole("radiogroup", { name: "Banner height" }).getByRole("radio", { name: "Tall" }).click();
  await expect(preview(page).locator("html")).toHaveAttribute("data-hero", "tall");

  await page.getByRole("button", { name: /Save changes/ }).click();
  await saved(page);
  expect(lastSave?.design.template_id).toBe("editorial");
  expect(lastSave?.password).toBeUndefined();
});

test("custom HTML: the server's findings are shown, the preview shows the cleaned version, and saving asks for the password", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Advanced/ }).click();

  await page.getByLabel("Custom HTML").fill(HOSTILE_HTML);
  await expect(page.getByText(/removed the onerror attribute on <img>/).first()).toBeVisible();
  await expect(page.getByRole("button", { name: /Save changes/ })).toBeDisabled();
  await expect(preview(page).locator("#custom-html .amenity")).toHaveText("Pool 08:00–20:00");
  await expect(preview(page).locator("#custom-html img")).not.toHaveAttribute("onerror", /.*/);

  await page.getByRole("button", { name: /Use the cleaned version/ }).click();
  await expect(page.getByLabel("Custom HTML")).toHaveValue(CLEANED_HTML);
  await page.getByRole("button", { name: /Save changes/ }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Confirm your password").fill("hunter2");
  await dialog.getByRole("button", { name: /Save changes/ }).click();
  await saved(page);
  expect(lastSave?.password).toBe("hunter2");
  expect(lastSave?.design.custom_html).toBe(CLEANED_HTML);
});

test("a logo is uploaded, previewed, saved and kept", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Brand/ }).click();

  await page.getByLabel("Upload logo").setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: PNG });

  // Read uploaded images through the operator API; the guest-network path is unavailable here.
  const shown = page.getByAltText("Logo currently set");
  await expect(shown).toBeVisible();
  await expect(shown).toHaveAttribute("src", /\/api\/edge\/v1\/portal-assets\/.*\/raw$/);

  await expect(preview(page).locator("#brand-logo")).toBeVisible();

  await page.getByRole("button", { name: /Save changes/ }).click();
  await saved(page);
  await page.reload();
  await page.getByRole("tab", { name: /Brand/ }).click();
  await expect(page.getByAltText("Logo currently set")).toBeVisible();
});

test("a background is uploaded and removed cleanly", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Brand/ }).click();

  await page.getByLabel("Upload background photograph")
    .setInputFiles({ name: "bg.png", mimeType: "image/png", buffer: PNG });
  await expect(page.getByAltText("Background photograph currently set")).toBeVisible();

  await page.getByRole("button", { name: /Remove background photograph/ }).click();
  await expect(page.getByAltText("Background photograph currently set")).toHaveCount(0);
  await expect(page.getByLabel("Upload background photograph")).toBeAttached();
});

test("languages are chosen for guests, and their wording edited one at a time", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /^Languages/ }).click();

  await expect(page.getByLabel("Offer العربية to clients")).toBeVisible();
  await expect(page.getByLabel("Offer English to clients")).toBeDisabled();
  await page.getByLabel("Offer Русский to clients").uncheck();

  await page.getByRole("tab", { name: /Sign-in page text/ }).click();
  await page.getByRole("tab", { name: /Italiano/ }).click();
  // Select the group summary rather than an inner field with the same label.
  await page.locator("summary").filter({ hasText: "Client Login" }).click();
  const shipped = shippedWording().strings.it["pms.room"];
  await expect(page.getByRole("textbox", { name: "Room Number in Italiano" })).toHaveValue(shipped);
  await expect(page.getByRole("textbox", { name: "Room Number in Deutsch" })).toHaveCount(0);

  await page.getByRole("button", { name: /Save changes/ }).click();
  await saved(page);
  await expect(preview(page).locator("#lang option")).not.toContainText(["Русский"]);
});

test("the designer works on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await installBackend(page);
  await page.goto("/portal-branding");
  await expect(page.getByRole("tab", { name: /Template/ })).toBeVisible();
  await expect(preview(page).locator(".card")).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow, "the page scrolls sideways at 390px").toBeLessThanOrEqual(1);
});
