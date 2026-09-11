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

// EVERY DELIBERATE CONNECT SUBMISSION GETS ITS OWN REQUEST ID.
//
// The id used to be derived from the typed details, so that "the same details" meant "the same attempt". The
// derivation read last_name / first_name / reservation_number, and room_any — the combined mode, where the
// guest types ONE value and is never asked what kind of identifier it is — puts that value in `verification`,
// which the key never looked at. So every tap on one page carried the SAME id, the server replayed the
// resolution already recorded under it, and a guest who mistyped and then corrected their surname was
// answered by their own typo until they reloaded the page.
//
// This is the browser half of the correction, and it has to be tested here: the id is generated in the
// guest's browser and nowhere else, so no Go test can observe what a second tap actually sends. The server
// half — a recorded refusal is re-evaluated, never replayed — is pinned in the resolver and scd suites.
//
// It asserts per-SUBMISSION, not per-CHANGE: submitting the identical value twice must still produce two ids.
// A test that only varied the value would pass against a smarter derivation, and a smarter derivation is the
// same defect waiting for the next field somebody forgets to add to the key.
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
    // The uniform non-success, which is what a wrong value really gets. The page must stay usable after it.
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
  // room_any is the mode the defect lived in, and the one a property actually runs.
  const sent = await collectRequestIDs(page, "room_any", ["Nottheguest", "Okonkwo"]);

  // The page really did send the correction — the freeze was never about the value not being typed.
  expect(sent[0].verification).toBe("Nottheguest");
  expect(sent[1].verification).toBe("Okonkwo");

  const ids = sent.map((b) => String(b.request_id ?? ""));
  ids.forEach((id) => expect(id).toMatch(CANONICAL_UUID));
  expect(ids[1]).not.toBe(ids[0]);
});

test("resubmitting the identical value still mints a new request id", async ({ page }) => {
  // Nothing the guest typed changed, and neither did the room. A derivation-based id would return the same
  // value here, which is exactly the state that froze the page.
  const sent = await collectRequestIDs(page, "room_any", ["Okonkwo", "Okonkwo", "Okonkwo"]);
  const ids = sent.map((b) => String(b.request_id ?? ""));
  ids.forEach((id) => expect(id).toMatch(CANONICAL_UUID));
  expect(new Set(ids).size).toBe(3);
});

test("the single-field modes mint a new id per submission too", async ({ page }) => {
  // room_lastname was never frozen — its field WAS in the old key — so this is the regression guard on the
  // other side: the correction must not have made the explicit modes reuse an id instead.
  const sent = await collectRequestIDs(page, "room_lastname", ["Nottheguest", "Okonkwo"]);
  expect(sent[0].last_name).toBe("Nottheguest");
  expect(sent[1].last_name).toBe("Okonkwo");
  const ids = sent.map((b) => String(b.request_id ?? ""));
  expect(ids[1]).not.toBe(ids[0]);
});
