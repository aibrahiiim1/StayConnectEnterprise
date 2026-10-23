import { test, expect, type Route } from "@playwright/test";
import { portalHTML as renderLanding } from "./portal-page";

// Captive portals use HTTP, where crypto.randomUUID may be absent; fallback IDs must still satisfy scd's UUID format.

// Keep this format rule independent of scd's validator so drift is caught.
const CANONICAL_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

test("the request id is a canonical UUID even without crypto.randomUUID", async ({ page }) => {
  // Override the property; delete can silently fail on a navigated page.
  await page.addInitScript(() => {
    Object.defineProperty(window.crypto, "randomUUID", { value: undefined, configurable: true });
  });

  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200, contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true }),
    }));

  let sent: Record<string, unknown> | null = null;
  await page.route("**/auth/pms/phase3", async (route: Route) => {
    sent = JSON.parse(route.request().postData() || "{}");
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: false }) });
  });

  await page.goto("/portal");
  await page.locator("#pms-room").fill("14332");
  await page.locator("#pms-secondary").fill("Example");
  await page.locator("#form-pms button[type=submit]").click();

  await expect.poll(() => sent).not.toBeNull();
  const id = String((sent as any).request_id ?? "");
  expect(id).toHaveLength(36);
  expect(id).toMatch(CANONICAL_UUID);
  expect(id[14]).toBe("4");
  expect("89ab").toContain(id[19].toLowerCase());
});

// Generate IDs per submission, even for unchanged details, so the server does not replay a prior refusal.
async function collectRequestIDs(page: import("@playwright/test").Page, mode: string, values: string[]) {
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200, contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode }, phase3_pms: true }),
    }));

  const sent: Array<Record<string, unknown>> = [];
  await page.route("**/auth/pms/phase3", async (route: Route) => {
    sent.push(JSON.parse(route.request().postData() || "{}"));
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: false }) });
  });

  await page.goto("/portal");
  await page.locator("#pms-room").fill("412");
  for (const [i, value] of values.entries()) {
    await page.locator("#pms-secondary").fill(value);
    await page.locator("#form-pms button[type=submit]").click();
    await expect.poll(() => sent.length).toBe(i + 1);
  }
  return sent;
}

test("a corrected value on the same page is submitted under a NEW request id", async ({ page }) => {
  const sent = await collectRequestIDs(page, "room_any", ["Nottheguest", "Okonkwo"]);

  expect(sent[0].verification).toBe("Nottheguest");
  expect(sent[1].verification).toBe("Okonkwo");

  const ids = sent.map((b) => String(b.request_id ?? ""));
  ids.forEach((id) => expect(id).toMatch(CANONICAL_UUID));
  expect(ids[1]).not.toBe(ids[0]);
});

test("resubmitting the identical value still mints a new request id", async ({ page }) => {
  const sent = await collectRequestIDs(page, "room_any", ["Okonkwo", "Okonkwo", "Okonkwo"]);
  const ids = sent.map((b) => String(b.request_id ?? ""));
  ids.forEach((id) => expect(id).toMatch(CANONICAL_UUID));
  expect(new Set(ids).size).toBe(3);
});

test("the single-field modes mint a new id per submission too", async ({ page }) => {
  const sent = await collectRequestIDs(page, "room_lastname", ["Nottheguest", "Okonkwo"]);
  expect(sent[0].last_name).toBe("Nottheguest");
  expect(sent[1].last_name).toBe("Okonkwo");
  const ids = sent.map((b) => String(b.request_id ?? ""));
  expect(ids[1]).not.toBe(ids[0]);
});
