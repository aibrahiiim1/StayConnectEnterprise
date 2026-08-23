import { test, expect, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// THE REQUEST ID MUST BE A CANONICAL UUID, INCLUDING WITHOUT crypto.randomUUID.
//
// scd rejects anything that is not 36 characters with dashes at 8/13/18/23, answering malformed_request_id
// before it looks at the room or the name. The portal's fallback returned 32 undashed hex characters, so
// every attempt was refused on format alone.
//
// That is not a corner case. crypto.randomUUID exists only in a SECURE CONTEXT and a captive portal is
// served over plain HTTP by definition, so the fallback is the path every real guest takes — Room sign-in
// failed for everyone, and the uniform failure message made it look like a wrong surname.
//
// The test runs with randomUUID deleted, which is the real guest's browser, and asserts the shape the server
// actually enforces.

const templatesGo = join(__dirname, "..", "..", "data-plane", "cmd", "portald", "templates.go");

function renderLanding(): string {
  const src = readFileSync(templatesGo, "utf8");
  const marker = "const landingHTML = `";
  const start = src.indexOf(marker) + marker.length;
  const end = src.indexOf("`", start);
  if (start < marker.length || end < 0) throw new Error("landingHTML not found in templates.go");
  return src.slice(start, end).replace(/\{\{[^}]*\}\}/g, "");
}

// The server's rule, restated here so the two cannot drift apart unnoticed.
const CANONICAL_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

test("the request id is a canonical UUID even without crypto.randomUUID", async ({ page }) => {
  // Exactly the guest's situation on an http:// captive portal.
  //
  // defineProperty, not delete: `delete window.crypto.randomUUID` silently fails on a real navigated page
  // (it succeeds on about:blank, which is what made an earlier version of this test pass against the broken
  // code and therefore prove nothing). Overriding the property is deterministic.
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
  // v4 + RFC-4122 variant, so it is a real UUID rather than dashed random hex.
  expect(id[14]).toBe("4");
  expect("89ab").toContain(id[19].toLowerCase());
});
