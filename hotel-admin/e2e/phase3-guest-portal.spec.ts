import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// PHASE-3 GUEST PORTAL, in a real browser, running the REAL client JS.
//
// The landing page is served by the Go `portald`, so the template is extracted from the source it is served
// from (no second copy that can drift) and the client script executes exactly as a guest's phone would run
// it. What is under test is the part of the contract that lives in the browser:
//
//   * every non-success renders the SAME message, whatever the server said;
//   * a single offer connects the guest without a second decision;
//   * several offers become a real choice, and choosing one connects them;
//   * the page never renders a stay, a room, an interface, a reason code or a server error string.
//
// The SERVER half — resolution, the one-time Auth Context, the atomic grant, the uniform scd response — is
// proven separately against a real PostgreSQL in data-plane/cmd/scd/phase3_auth_integration_test.go and
// data-plane/cmd/portald/pms_phase3_handlers_test.go. Neither half proves the other, which is why both exist.

const templatesGo = join(__dirname, "../../data-plane/cmd/portald/templates.go");

const UNIFORM_MESSAGE =
  "We could not verify your stay. Please check your details or contact reception.";

function renderLanding(): string {
  const src = readFileSync(templatesGo, "utf8");
  const marker = "const landingHTML = `";
  const start = src.indexOf(marker) + marker.length;
  const end = src.indexOf("`", start);
  if (start < marker.length || end < 0) throw new Error("landingHTML not found in templates.go");
  let html = src.slice(start, end);
  // The landing template has no Go actions in the PMS path; strip any that exist elsewhere so the page
  // parses as plain HTML.
  html = html.replace(/\{\{[^}]*\}\}/g, "");
  return html;
}

type Call = { path: string; body: Record<string, unknown> };

/** serve renders the real page and answers the two endpoints its script talks to. */
async function serve(
  page: Page,
  opts: {
    phase3: boolean;
    /** answers for POST /auth/pms/phase3, in order */
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

  // the guest never leaves the origin in these tests; stub the destination so a redirect is observable
  await page.route("**/success*", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html", body: "<h1>You are online</h1>" }));
}

async function submitStay(page: Page, room: string, secondary: string) {
  await page.getByLabel("Room number").fill(room);
  await page.locator("#pms-secondary").fill(secondary);
  await page.getByRole("button", { name: "Connect" }).click();
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
  // the page must send a request id, or a double tap would record two resolutions
  expect(String(calls[0].body.request_id ?? "")).not.toBe("");
  // and it must NOT send any identity — that is derived by the server
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
  // the choice step must not disclose anything about the stay it belongs to
  await expect(page.locator("#pms-choices")).not.toContainText("412");
  await expect(page.locator("#pms-choices")).not.toContainText("Okonkwo");

  await choices.nth(1).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.auth_context_id).toBe("ctx-1");
  expect(calls[1].body.package_revision_id).toBe("pkg-prem");
});

test("every failure looks identical to the guest", async ({ page }) => {
  // Four different server realities. The page must render one message for all of them — and must not render
  // any of the detail the server happened to include.
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
    // still on the form: nothing was granted
    await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
    // and nothing leaked
    // The forbidden list is the set of words that can only appear if server detail leaked. "stay" and
    // "reservation" are deliberately NOT on it: they are part of the legitimate copy the guest is meant to
    // read, and listing them would make this assertion fail on correct behaviour.
    const shown = (await page.locator("#panel-pms").innerText()).toLowerCase();
    for (const forbidden of [
      "ambiguous", "indeterminate", "unavailable", "interface", "protel", "opera", "fias",
      "no_match", "not_verified", "candidate", "throttl", "entitlement", "package", "error",
    ]) {
      expect(shown).not.toContain(forbidden);
    }
  }
});

test("a server that answers with nothing at all is still the same message", async ({ page }) => {
  const calls: Call[] = [];
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({ status: 200, contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode: "room_lastname" }, phase3_pms: true }) }));
  // the request simply fails — the network dropped, or portald is not there
  await page.route("**/auth/pms/**", (r: Route) => r.abort("failed"));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  expect(calls).toHaveLength(0);
});

test("with Phase 3 off the page keeps using the legacy endpoint", async ({ page }) => {
  // A dark site must behave exactly as it does today. If the page could be talked into the Phase-3 endpoint
  // by anything other than the server's own flag, "dark" would depend on the client.
  const calls: Call[] = [];
  await serve(page, {
    phase3: false,
    calls,
    answers: [{ session_id: "legacy-1", duration_seconds: 3600 }],
  });
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe("/auth/pms/verify");
  expect(calls[0].body).not.toHaveProperty("request_id");
});
