import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML as renderLanding } from "./portal-page";

// The guest page must not send identity fields; the server's strict decoder is a second safeguard.

const UNIFORM_MESSAGE =
  "We could not verify your stay. Please check your details or contact the site team for assistance.";

type Call = { path: string; body: Record<string, unknown> };

async function serve(page: Page, answers: unknown[], calls: Call[]) {
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: renderLanding() }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      // Post-stay needs its own capability gate; PMS availability alone is insufficient.
      body: JSON.stringify({
        pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true, phase5_poststay: true,
      }),
    }));
  let n = 0;
  await page.route("**/auth/post-stay", async (route: Route) => {
    let body: Record<string, unknown> = {};
    try { body = route.request().postDataJSON() ?? {}; } catch { /* no body */ }
    calls.push({ path: new URL(route.request().url()).pathname, body });
    const answer = answers[Math.min(n, answers.length - 1)];
    n += 1;
    return route.fulfill({
      status: 200, contentType: "application/json", body: JSON.stringify(answer ?? {}),
    });
  });
}

test.describe("the post-stay guest panel", () => {
  test("asks for the room number and the reservation number and nothing else", async ({ page }) => {
    await serve(page, [{}], []);
    await page.goto("/portal");
    await page.getByRole("button", { name: "Post-stay" }).click().catch(async () => {
      await page.locator('[data-tab="poststay"]').click();
    });

    const panel = page.locator("#panel-poststay");
    await expect(panel.locator("#ps-room")).toBeVisible();
    await expect(panel.locator("#ps-res")).toBeVisible();
    // After Check-out Access: no surname, no first name, no PIN and no one-time code.
    await expect(panel.locator('input[name="last_name"]')).toHaveCount(0);
    await expect(panel.locator('input[name="first_name"]')).toHaveCount(0);
    await expect(panel.getByLabel(/pin|one-time|code/i)).toHaveCount(0);
    await expect(panel.locator("input")).toHaveCount(2);
  });

  test("sends the room and reservation alone and connects without a second step", async ({ page }) => {
    const calls: Call[] = [];
    await serve(page, [{ ok: true, session_id: "sess-1" }], calls);
    await page.goto("/portal");
    await page.locator('[data-tab="poststay"]').click();
    await page.locator("#ps-room").fill("101");
    await page.locator("#ps-res").fill("R1");
    await page.locator("#form-poststay button[type=submit]").click();

    await expect.poll(() => calls.length).toBeGreaterThan(0);
    // Exactly what the guest typed, and nothing that names a stay, a profile, a device or a package.
    expect(Object.keys(calls[0].body).sort()).toEqual(["reservation", "room"]);
    expect(calls[0].body.room).toBe("101");
    expect(calls[0].body.reservation).toBe("R1");
    for (const forbidden of ["last_name", "stay", "stay_id", "room_number", "profile", "profile_id",
      "pms_interface_id", "reservation_number", "device", "pin", "package_revision_id", "auth_context_id"]) {
      expect(calls[0].body).not.toHaveProperty(forbidden);
    }
    // One entitlement is granted at checkout: there is no package choice to make, so exactly one call is made.
    expect(calls.length).toBe(1);
  });

  test("every failure is the same message and reveals nothing", async ({ page }) => {
    // Collapse upstream refusal details to one guest-visible message to prevent enumeration.
    for (const answer of [
      { ok: false },
      { ok: false, message: "profile 7777aaaa revoked at 10:00" },
      { ok: false, error: "stay eeee0000 is at lifecycle_version 3" },
      {},
    ]) {
      const calls: Call[] = [];
      await serve(page, [answer], calls);
      await page.goto("/portal");
      await page.locator('[data-tab="poststay"]').click();
      await page.locator("#ps-room").fill("999");
      await page.locator("#ps-res").fill("WRONG");
      await page.locator("#form-poststay button[type=submit]").click();
      await expect(page.locator("#ps-err")).toHaveText(UNIFORM_MESSAGE);
      const body = await page.content();
      for (const leak of ["7777aaaa", "eeee0000", "lifecycle_version", "revoked at"]) {
        expect(body).not.toContain(leak);
      }
    }
  });
});
