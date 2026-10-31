import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML as renderLanding } from "./portal-page";

// Every server refusal produces the same guest-visible response without revealing stay or room details.

const UNIFORM_MESSAGE =
  "We could not verify your stay. Please check your details or contact the site team for assistance.";

const TRANSPORT_MESSAGE = "Something went wrong. Please try again.";

type Call = { path: string; body: Record<string, unknown> };

async function serve(
  page: Page,
  opts: {
    phase3: boolean;
    answers: unknown[];
    calls: Call[];
  },
) {
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));

  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        pms: { enabled: true, mode: "room_lastname" },
        phase3_pms: opts.phase3,
      }),
    }));

  let n = 0;
  await page.route("**/auth/pms/**", async (route: Route) => {
    const req = route.request();
    let body: Record<string, unknown> = {};
    try { body = req.postDataJSON() ?? {}; } catch { /* no body */ }
    opts.calls.push({ path: new URL(req.url()).pathname, body });
    const answer = opts.answers[Math.min(n, opts.answers.length - 1)];
    n += 1;
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(answer) });
  });

  await page.route("**/success*", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html", body: "<h1>You are online</h1>" }));
}

async function submitStay(page: Page, room: string, secondary: string) {
  await page.locator("#pms-room").fill(room);
  await page.locator("#pms-secondary").fill(secondary);
  await page.getByRole("button", { name: "Submit" }).click();
}

test("one offer connects the guest without asking a second question", async ({ page }) => {
  const calls: Call[] = [];
  await serve(page, {
    phase3: true,
    calls,
    answers: [
      { ok: true, session_id: "sess-1", redirect_to: "/success" },
    ],
  });
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe("/auth/pms/phase3");
  expect(String(calls[0].body.request_id ?? "")).not.toBe("");
  // Device identity comes from the server, never a guest-supplied claim.
  expect(calls[0].body).not.toHaveProperty("ip");
  expect(calls[0].body).not.toHaveProperty("mac");
  expect(calls[0].body).not.toHaveProperty("stay_id");
});

test("several offers become a real choice, and choosing one connects the guest", async ({ page }) => {
  const calls: Call[] = [];
  await serve(page, {
    phase3: true,
    calls,
    answers: [
      {
        ok: true,
        needs_choice: true,
        auth_context_id: "ctx-1",
        choices: [
          { package_revision_id: "pkg-std", code: "STANDARD", down_kbps: 9000 },
          { package_revision_id: "pkg-prem", code: "PREMIUM", down_kbps: 25000 },
        ],
      },
      { ok: true, session_id: "sess-2", redirect_to: "/success" },
    ],
  });
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  const choices = page.locator("#pms-choices button.choice");
  await expect(choices).toHaveCount(2);
  await expect(choices.first()).toContainText("STANDARD");
  await expect(choices.nth(1)).toContainText("PREMIUM");
  await expect(page.locator("#pms-choices")).not.toContainText("412");
  await expect(page.locator("#pms-choices")).not.toContainText("Okonkwo");

  await choices.nth(1).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.auth_context_id).toBe("ctx-1");
  expect(calls[1].body.package_revision_id).toBe("pkg-prem");
});

test("every failure looks identical to the guest", async ({ page }) => {
  // Normalize varied server details into one refusal to prevent enumeration.
  const answers = [
    { ok: false, message: UNIFORM_MESSAGE },
    { ok: false, message: UNIFORM_MESSAGE },
    { ok: true, needs_choice: true, auth_context_id: "c", choices: [] },
    { ok: true },
  ];
  for (const answer of answers) {
    const calls: Call[] = [];
    await serve(page, { phase3: true, calls, answers: [answer] });
    await page.goto("http://localhost/portal");
    await submitStay(page, "412", "Okonkwo");

    const err = page.locator("#pms-err");
    await expect(err).toHaveText(UNIFORM_MESSAGE);
    await expect(page.getByRole("button", { name: "Submit" })).toBeVisible();
    // Legitimate copy uses stay and reservation; only detail-specific words indicate leakage.
    const shown = (await page.locator("#panel-pms").innerText()).toLowerCase();
    for (const forbidden of [
      "ambiguous", "indeterminate", "unavailable", "interface", "protel", "opera", "fias",
      "no_match", "not_verified", "candidate", "throttl", "entitlement", "package", "error",
    ]) {
      expect(shown).not.toContain(forbidden);
    }
  }
});

// A failed network request reveals no guest facts, so it may show a technical retry message.
test("a request that never reaches the server shows a connection error, not credential advice", async ({ page }) => {
  const calls: Call[] = [];
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({ status: 200, contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true }) }));
  await page.route("**/auth/pms/**", (r: Route) => r.abort("failed"));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(TRANSPORT_MESSAGE);
  await expect(page.locator("#pms-err")).not.toHaveText(UNIFORM_MESSAGE);
  expect(calls).toHaveLength(0);
});

// PMS room sign-in and post-stay PIN access have independent capability gates.
test("enabling PMS does not expose the Post-Stay tab", async ({ page }) => {
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200, contentType: "application/json",
      body: JSON.stringify({ voucher: { enabled: true }, pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true }),
    }));
  await page.goto("/portal");
  await expect(page.locator("#panel-pms")).toBeAttached();
  await expect(page.locator('#alt-methods button[data-tab="pms"]')).toHaveCount(1);
  await expect(page.locator('#alt-methods button:is([data-tab="account"],[data-tab="voucher"])')).toHaveCount(1);
  await expect(page.locator('#alt-methods button[data-tab="poststay"]')).toHaveCount(0);
  await expect(page.locator('#panel-pms.active')).toHaveCount(1);
  await expect(page.locator('#alt-methods')).not.toContainText("Post-stay");
});

test("with Phase 3 off the page keeps using the legacy endpoint", async ({ page }) => {
  const calls: Call[] = [];
  await serve(page, {
    phase3: false,
    calls,
    answers: [{ session_id: "legacy-1", duration_seconds: 3600 }],
  });
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  // Wait for the request; submitStay returns before the network call completes.
  await expect.poll(() => calls.length, { timeout: 10_000 }).toBe(1);
  expect(calls[0].path).toBe("/auth/pms/verify");
  expect(calls[0].body).not.toHaveProperty("request_id");
});
