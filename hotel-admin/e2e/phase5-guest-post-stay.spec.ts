import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// THE POST-STAY GUEST PANEL, in a real browser, running the REAL client JS.
//
// The landing page is served by the Go `portald`, so the template is read from the source it is served from
// (no second copy that can drift) and the script executes exactly as a departing guest's phone would run it.
//
// What only a browser can prove here: that the page does not put an identity in the request. The server's
// strict decoder refuses one, but a refusal is a safety net — this asserts the page never reaches for the net
// in the first place, on a real DOM, with the real event handlers.

const templatesGo = join(__dirname, "../../data-plane/cmd/portald/templates.go");

const UNIFORM_MESSAGE =
  "We could not verify your stay. Please check your details or contact reception.";

function renderLanding(): string {
  const src = readFileSync(templatesGo, "utf8");
  const marker = "const landingHTML = `";
  const start = src.indexOf(marker) + marker.length;
  const end = src.indexOf("`", start);
  if (start < marker.length || end < 0) throw new Error("landingHTML not found in templates.go");
  return src.slice(start, end).replace(/\{\{[^}]*\}\}/g, "");
}

type Call = { path: string; body: Record<string, unknown> };

async function serve(page: Page, answers: unknown[], calls: Call[]) {
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: renderLanding() }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true }),
    }));
  let n = 0;
  await page.route("**/auth/post-stay-pin", async (route: Route) => {
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
  test("asks for the PIN and nothing else", async ({ page }) => {
    await serve(page, [{}], []);
    await page.goto("/portal");
    await page.getByRole("button", { name: "Post-stay" }).click().catch(async () => {
      await page.locator('[data-tab="poststay"]').click();
    });

    const panel = page.locator("#panel-poststay");
    await expect(panel.getByLabel(/post-stay pin/i)).toBeVisible();
    // The fields a first-time proof needs and a SECOND proof must not: there is no room, no last name and no
    // reservation number on this panel, because the appliance already knows which stay this device was on.
    await expect(panel.locator('input[name="room"]')).toHaveCount(0);
    await expect(panel.locator('input[name="last_name"]')).toHaveCount(0);
    await expect(panel.locator('input[name="reservation_number"]')).toHaveCount(0);
    await expect(panel.locator("input")).toHaveCount(1);
  });

  test("sends the PIN alone — no stay, room, profile or device", async ({ page }) => {
    const calls: Call[] = [];
    await serve(page, [{ ok: true, auth_context_id: "ctx-1" }, { ok: true, session_id: "sess-1" }], calls);
    await page.goto("/portal");
    await page.locator('[data-tab="poststay"]').click();
    await page.locator("#ps-pin").fill("K7M4RTQX");
    await page.locator("#form-poststay button[type=submit]").click();

    await expect.poll(() => calls.length).toBeGreaterThan(0);
    // The FIRST call is the whole assertion: exactly one key, and it is the PIN.
    expect(Object.keys(calls[0].body).sort()).toEqual(["pin"]);
    expect(calls[0].body.pin).toBe("K7M4RTQX");
    for (const forbidden of ["stay", "stay_id", "room", "room_number", "profile", "profile_id",
      "pms_interface_id", "last_name", "reservation_number", "device"]) {
      expect(calls[0].body).not.toHaveProperty(forbidden);
    }
    // The second call carries the context the SERVER issued, and still no subject the page chose.
    await expect.poll(() => calls.length).toBe(2);
    expect(Object.keys(calls[1].body).sort()).toEqual(["auth_context_id"]);
  });

  test("every failure is the same message and reveals nothing", async ({ page }) => {
    // A server that leaks detail must not become a page that displays it. These are the shapes a broken or
    // hostile upstream could return; all of them render as the one message.
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
      await page.locator("#ps-pin").fill("WRONGPIN");
      await page.locator("#form-poststay button[type=submit]").click();
      await expect(page.locator("#ps-err")).toHaveText(UNIFORM_MESSAGE);
      const body = await page.content();
      for (const leak of ["7777aaaa", "eeee0000", "lifecycle_version", "revoked at"]) {
        expect(body).not.toContain(leak);
      }
    }
  });
});
