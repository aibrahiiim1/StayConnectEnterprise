import { test, expect, type Page, type Route } from "@playwright/test";

// VOUCHERS, IN A BROWSER.
//
// The component tests assert what the screen sends; these assert what only a real browser can: that a
// step-up dialog opens IN VIEW rather than below a long table (the reported "the button does nothing"), that
// the page fits a 390px phone without sideways scrolling, and that printing lays out cards and nothing else.
//
// edged is mocked at the network layer. No appliance, no database, no guest data.

const PKG = "11111111-1111-4111-8111-111111111111";
const BATCH = "22222222-2222-4222-8222-222222222222";
const DAY = 86_400_000;
const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");

function rows(n: number) {
  const now = Date.now();
  return Array.from({ length: n }, (_, i) => ({
    id: `a0000000-0000-4000-8000-${String(i).padStart(12, "0")}`,
    code_last4: `C${String(i).padStart(3, "0")}`,
    state: "UNUSED",
    effective_state: i % 7 === 3 ? "expired" : "available",
    package_revision_id: PKG,
    package_name: "One day",
    package_revision_no: 2,
    batch_id: BATCH,
    created_at: iso(now - 2 * DAY),
    redemption_valid_from: null,
    redemption_valid_until: i % 7 === 3 ? iso(now - DAY) : null,
    notes: null,
    issued_by: null,
  }));
}

export async function installBackend(page: Page, baseURL = "http://127.0.0.1:3123") {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: baseURL }]);
  // Record print() instead of opening the browser dialog, and capture what would have been printed.
  await page.addInitScript(() => {
    (window as any).__printed = [];
    window.print = () => {
      const root = document.querySelector(".voucher-print-root");
      (window as any).__printed.push({
        cards: root ? root.querySelectorAll(".vp-card").length : 0,
        text: root ? (root as HTMLElement).innerText : "",
      });
    };
  });

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    const method = req.method();
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/vouchers/summary")
      return route.fulfill(json(200, { unused: 200, redeemed: 0, revoked: 0, redemption_expired: 0, available: 171, expired_unused: 29, not_yet_valid: 0, total: 200, issued_last_7_days: 200, batches: 1 }));
    if (path === "/vouchers/") return route.fulfill(json(200, { vouchers: rows(50), total: 200 }));
    if (path === "/vouchers/batches")
      return route.fulfill(json(200, { batches: [{ batch_id: BATCH, package_revision_id: PKG, package_name: "One day", count: 200, redeemed: 0, cancelled: 0, available: 171, expired: 29, not_yet_valid: 0, unused: 200, created_at: iso(Date.now() - 2 * DAY), issued_by: null, notes: null, redemption_valid_from: null, redemption_valid_until: null }], total: 1, unbatched_vouchers: 0 }));
    if (path === "/vouchers/grantable")
      return route.fulfill(json(200, { revisions: [{ id: PKG, package_code: "DAY", revision_no: 2, package_type: "ONE_DAY", name: "One day", price_minor: 1500, currency: "EUR", currency_exponent: 2 }] }));
    if (path.endsWith("/history")) return route.fulfill(json(200, { voucher_id: "x", entitlements: [], redemption_available: true, cancelled: null, cancellation_available: true }));
    if (path === "/vouchers/issue" && method === "POST")
      return route.fulfill(json(201, { count: 3, batch_id: BATCH, codes: ["48273962", "62394827", "73962482"] }));
    if (path === "/voucher-codes/reveals") return route.fulfill(json(200, { reveals: [] }));
    if (path === "/voucher-code-settings/") return route.fulfill(json(200, { code_mode: "numbers", code_length: 8, config_version: 0 }));
    if (path === "/voucher-code-settings/changes") return route.fulfill(json(200, { changes: [] }));
    if (path === "/voucher-code-settings/key-generations") return route.fulfill(json(200, { generations: [] }));
    if (path === "/portal-branding") return route.fulfill(json(200, { design: { hotel_name: "Semantics Demo Hotel" }, draft: {} }));
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

test("a card's cancel confirmation opens in view, even at the bottom of a long list", async ({ page, baseURL }) => {
  await installBackend(page, baseURL);
  await page.goto("/vouchers");
  const last = page.getByRole("row", { name: /C049/ });
  await last.scrollIntoViewIfNeeded();
  await last.click();
  const sheet = page.getByRole("dialog", { name: /C049/ });
  await expect(sheet).toBeInViewport();
  await sheet.getByRole("button", { name: /Cancel card/ }).click();
  const confirm = page.getByRole("dialog", { name: /Cancel card •••• C049/ });
  await expect(confirm).toBeInViewport();
  await expect(confirm.getByLabel(/password/i)).toHaveAttribute("type", "password");
});

test("issuing shows the codes once and prints only the cards", async ({ page, baseURL }) => {
  await installBackend(page, baseURL);
  await page.goto("/vouchers");
  await page.getByRole("button", { name: /Issue vouchers/ }).first().click();
  const dlg = page.getByRole("dialog", { name: "Issue vouchers" });
  await dlg.getByText("One day").click();
  await dlg.getByRole("button", { name: "Next" }).click();
  await dlg.getByLabel(/How many cards/).fill("3");
  await dlg.getByRole("button", { name: "Review", exact: true }).click();
  await dlg.getByRole("button", { name: "Issue 3 vouchers" }).click();

  await expect(page.getByTestId("held-code")).toHaveCount(3);
  await page.getByRole("button", { name: /Print cards/ }).click();
  await expect.poll(() => page.evaluate(() => (window as any).__printed.length)).toBe(1);
  const printed = await page.evaluate(() => (window as any).__printed[0]);
  expect(printed.cards).toBe(3);
  expect(printed.text).toContain("48273962");
  expect(printed.text).toContain("Semantics Demo Hotel");
  // The print root is removed after printing: nothing of the codes is left in the document outside the dialog.
  await expect(page.locator(".voucher-print-root")).toHaveCount(0);
});

test("the voucher page fits a 390px phone without sideways scrolling", async ({ page, baseURL }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await installBackend(page, baseURL);
  await page.goto("/vouchers");
  await expect(page.getByRole("heading", { name: "Vouchers" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
