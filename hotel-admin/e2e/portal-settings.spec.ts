import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML, shippedWording } from "./portal-page";

// PORTAL SETTINGS, IN A BROWSER.
//
// The component tests assert the behaviour; these assert the things only a real browser can answer — that the
// designer's sections lay out, that an image the operator picks reaches the field and appears, that the preview
// and the template gallery really render the guest portal rather than a drawing of it, and that choosing a
// template visibly changes the layout. The preview exists for that last reason, and no jsdom test can see it:
// it is an iframe running the portal's own script.
//
// edged is mocked at the network layer. No appliance, no database, no guest data.

/** An 8×8 teal PNG, byte for byte, and a VALID one.
 *
 *  The first version of this fixture had the right magic bytes and a corrupt IDAT. Everything passed except
 *  the preview, where the browser could not decode it and the portal's own onerror hid the logo — which is
 *  the correct behaviour for a broken image and made the test look like a product defect for twenty minutes.
 *  A fixture that only has to fool a sniffer proves nothing about a page that has to DRAW it. */
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
      // The server's verdict, as internal/portaldesign would give it for the one hostile fragment used below.
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
    if (path === "/portal-branding/languages") return route.fulfill(json(200, shippedWording()));
    if (path === "/portal-assets" && method === "POST")
      return route.fulfill(json(200, { name: "aaaa1111bbbb2222.png", url: "/assets/aaaa1111bbbb2222.png", size_bytes: PNG.length }));
    if (path === "/portal-assets") return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
    if (path.endsWith("/raw")) return route.fulfill({ status: 200, contentType: "image/png", body: PNG });
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

const preview = (page: Page) => page.frameLocator('iframe[title^="Guest portal"]');
const saved = (page: Page) => expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();

test.beforeEach(() => {
  design = { hotel_name: "Coral Sea Holiday Resort", brand_color: "#0f6b63" };
  lastSave = null;
});

test("the designer is sections and a live preview, with no release vocabulary", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  for (const s of ["Template", "Brand", "Content", "Sign-in page text", "Languages", "Advanced HTML & CSS", "History"]) {
    await expect(page.getByRole("tab", { name: new RegExp(s) })).toBeVisible();
  }
  // The words a hotel does not have. Asserted on the rendered page rather than the source, because what
  // matters is what an operator reads.
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
  // The portal's OWN markup, inside the frame: its card, its brand slot, its sign-in tabs.
  await expect(frame.locator(".card")).toBeVisible();
  await expect(frame.locator("#brand-name")).toHaveText("Coral Sea Holiday Resort");
  // And its script ran: the tabs are generated from the auth-methods answer, not present in the template.
  await expect(frame.locator('.tab[data-group="guest"]')).toBeVisible();
  await expect(frame.locator('.tab[data-group="account"]')).toBeVisible();

  // A change in the form reaches the preview without a save.
  await page.getByRole("tab", { name: /Content/ }).click();
  await page.getByLabel("Hotel name", { exact: true }).fill("Blue Bay Resort");
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
  // Each thumbnail is the portal itself, rendered with this hotel's name in that layout.
  const thumb = page.frameLocator('iframe[title="Template thumbnail: Split"]');
  await expect(thumb.locator("html")).toHaveAttribute("data-template", "split");
  await expect(thumb.locator(".sc-hero-name")).toHaveText("Coral Sea Holiday Resort");

  await gallery.getByText("Resort", { exact: true }).click();
  await expect(preview(page).locator("html")).toHaveAttribute("data-template", "editorial");
  // The Resort layout has a banner height; the other options follow the layout too.
  await page.getByRole("radiogroup", { name: "Banner height" }).getByRole("radio", { name: "Tall" }).click();
  await expect(preview(page).locator("html")).toHaveAttribute("data-hero", "tall");

  // A layout is not markup: it saves with no password.
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
  // The preview renders what a guest would receive: the paragraph, without the handler.
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

  // It is shown back through the operator API, not through the guest-network path that resolves to nothing
  // from this origin.
  const shown = page.getByAltText("Logo currently set");
  await expect(shown).toBeVisible();
  await expect(shown).toHaveAttribute("src", /\/api\/edge\/v1\/portal-assets\/.*\/raw$/);

  // And it reaches the guest portal preview, as an image that actually loaded.
  await expect(preview(page).locator("#brand-logo")).toBeVisible();

  await page.getByRole("button", { name: /Save changes/ }).click();
  await saved(page);
  // Reloading shows what was saved, with no step in between.
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

  await expect(page.getByLabel("Offer العربية to guests")).toBeVisible();
  await expect(page.getByLabel("Offer English to guests")).toBeDisabled();
  // Turning one off removes it from what a guest is offered, which the preview's own selector shows.
  await page.getByLabel("Offer Русский to guests").uncheck();

  await page.getByRole("tab", { name: /Sign-in page text/ }).click();
  await page.getByRole("tab", { name: /Italiano/ }).click();
  // Fifty-one strings are grouped by where they appear on the page; a shipped language opens on the first
  // group. The group's <summary>, not the field labelled "Guest Login" that lives inside Navigation -- the
  // string and the group share a name, which is correct for an operator and ambiguous for a text locator.
  await page.locator("summary").filter({ hasText: "Guest Login" }).click();
  // AND THE FIELD HOLDS THE REAL ITALIAN, which is the whole point: an empty box with English behind it is
  // what made selecting a language look like it had done nothing.
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
