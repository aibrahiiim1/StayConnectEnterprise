import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML } from "./portal-page";

// PORTAL SETTINGS, IN A BROWSER.
//
// The component tests assert the behaviour; these assert the things only a real browser can answer — that the
// four sections lay out, that an image the operator picks reaches the field and appears, and that the preview
// really renders the guest portal rather than a drawing of it. The last of those is the whole reason the
// preview exists, and no jsdom test can see it: it is an iframe running the portal's own script.
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
    if (path === "/portal-branding/settings") {
      design = (req.postDataJSON() as { design: Record<string, unknown> }).design;
      return route.fulfill(json(200, { saved: true }));
    }
    if (path === "/portal-branding/preview") return route.fulfill(json(200, { html: PORTAL_HTML }));
    if (path === "/portal-assets" && method === "POST")
      return route.fulfill(json(200, { name: "aaaa1111bbbb2222.png", url: "/assets/aaaa1111bbbb2222.png", size_bytes: PNG.length }));
    if (path === "/portal-assets") return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
    if (path.endsWith("/raw")) return route.fulfill({ status: 200, contentType: "image/png", body: PNG });
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

test.beforeEach(() => {
  design = { hotel_name: "Coral Sea Holiday Resort", brand_color: "#0f6b63" };
});

test("the settings page is four sections and a preview, with no release vocabulary", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  for (const s of ["General", "Branding", "Languages", "Advanced"]) {
    await expect(page.getByRole("tab", { name: new RegExp(s) })).toBeVisible();
  }
  // The words a hotel does not have. Asserted on the rendered page rather than the source, because what
  // matters is what an operator reads.
  const text = (await page.locator("body").innerText()).toLowerCase();
  for (const word of ["draft", "publish", "rollback", "live version"]) {
    expect(text, `the page still says "${word}"`).not.toContain(word);
  }
  await expect(page.getByRole("button", { name: /Save changes/ })).toBeDisabled();
});

test("the preview renders the real guest portal, at both sizes", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");

  const frame = page.frameLocator('iframe[title*="Guest portal"]');
  // The portal's OWN markup, inside the frame: its card, its brand slot, its sign-in tabs.
  await expect(frame.locator(".card")).toBeVisible();
  await expect(frame.locator("#brand-name")).toHaveText("Coral Sea Holiday Resort");
  // And its script ran: the tabs are generated from the auth-methods answer, not present in the template.
  await expect(frame.locator('.tab[data-group="guest"]')).toBeVisible();
  await expect(frame.locator('.tab[data-group="account"]')).toBeVisible();

  // A change in the form reaches the preview without a save.
  await page.getByLabel("Hotel name", { exact: true }).fill("Blue Bay Resort");
  await expect(frame.locator("#brand-name")).toHaveText("Blue Bay Resort");

  await page.getByRole("button", { name: /Mobile/ }).click();
  await expect(frame.locator(".card")).toBeVisible();
});

test("a logo is uploaded, previewed, saved and kept", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Branding/ }).click();

  await page.getByLabel("Upload logo").setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: PNG });

  // It is shown back through the operator API, not through the guest-network path that resolves to nothing
  // from this origin.
  const shown = page.getByAltText("Logo currently set");
  await expect(shown).toBeVisible();
  await expect(shown).toHaveAttribute("src", /\/api\/edge\/v1\/portal-assets\/.*\/raw$/);

  // And it reaches the guest portal preview, as an image that actually loaded.
  const frame = page.frameLocator('iframe[title*="Guest portal"]');
  await expect(frame.locator("#brand-logo")).toBeVisible();

  await page.getByRole("button", { name: /Save changes/ }).click();
  await expect(page.getByRole("status")).toContainText(/Saved/);
  // Reloading shows what was saved, with no step in between.
  await page.reload();
  await page.getByRole("tab", { name: /Branding/ }).click();
  await expect(page.getByAltText("Logo currently set")).toBeVisible();
});

test("a background is uploaded and removed cleanly", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Branding/ }).click();

  await page.getByLabel("Upload background photograph")
    .setInputFiles({ name: "bg.png", mimeType: "image/png", buffer: PNG });
  await expect(page.getByAltText("Background photograph currently set")).toBeVisible();

  await page.getByRole("button", { name: /Remove background photograph/ }).click();
  await expect(page.getByAltText("Background photograph currently set")).toHaveCount(0);
  await expect(page.getByLabel("Upload background photograph")).toBeAttached();
});

test("languages are chosen for guests and edited one at a time", async ({ page }) => {
  await installBackend(page);
  await page.goto("/portal-branding");
  await page.getByRole("tab", { name: /Languages/ }).click();

  await expect(page.getByLabel("Offer العربية to guests")).toBeVisible();
  await expect(page.getByLabel("Offer English to guests")).toBeDisabled();

  await page.getByRole("tab", { name: /Italiano/ }).click();
  // Fifty strings are grouped by where they appear on the page; a shipped language opens on the first group
  // because most properties change nothing here at all.
  await page.getByText("Room sign-in", { exact: true }).click();
  await expect(page.getByLabel(/Room Number in Italiano/)).toBeVisible();
  await expect(page.getByLabel(/Room Number in Deutsch/)).toHaveCount(0);

  // Turning one off removes it from what a guest is offered, which the preview's own selector shows.
  await page.getByLabel("Offer Русский to guests").uncheck();
  await page.getByRole("button", { name: /Save changes/ }).click();
  await expect(page.getByRole("status")).toContainText(/Saved/);
  const frame = page.frameLocator('iframe[title*="Guest portal"]');
  await expect(frame.locator("#lang option")).not.toContainText(["Русский"]);
});
