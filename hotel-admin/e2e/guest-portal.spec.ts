import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Guest Portal browser E2E for "You're online". The portal is served by the Go `portald`; to drive the REAL page
// in a real browser without running the Linux binary, data-plane/cmd/portald renders it with the real handler
// into e2e/fixtures (TestE2EPortalFixtures fails when the fixture is stale). The nonce is the inert placeholder
// "e2e-nonce".
//
// THE PACKAGE PANEL IS GONE FROM THIS PAGE (portal audit P-16). Every device that reaches it is online with its
// sign-in already spent; the panel offered purchases to a client who had just bought, labelled every quote
// "Free" and confirmed without a method, so a priced package answered "offer expired". What is pinned here is
// that the page offers nothing to buy and never calls the commerce bridge, with the Phase-2 surface on or off.
function renderSuccess(): string {
  return readFileSync(join(__dirname, "fixtures", "portal-success.html"), "utf8");
}

async function serve(page: Page, calls: string[]) {
  const html = renderSuccess();
  await page.route("**/success", (r: Route) => r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/commerce/**", async (route: Route) => {
    calls.push(new URL(route.request().url()).pathname);
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ packages: [] }) });
  });
}

test("You're online: no package panel and no commerce API call", async ({ page }) => {
  const calls: string[] = [];
  await serve(page, calls);
  page.on("request", (r) => { if (r.url().includes("/api/commerce/")) calls.push(r.url()); });
  await page.goto("http://127.0.0.1:9099/success");
  await expect(page.locator("#commerce")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Select" })).toHaveCount(0);
  await page.waitForTimeout(300);
  expect(calls).toHaveLength(0);
});

test("You're online: Disconnect carries this device's sign-out token", async ({ page }) => {
  const calls: string[] = [];
  await serve(page, calls);
  await page.goto("http://127.0.0.1:9099/success");
  const leave = page.locator('form[action="/logout"] input[name="leave"]');
  await expect(leave).toHaveCount(1);
  expect((await leave.getAttribute("value")) ?? "").not.toEqual("");
});
