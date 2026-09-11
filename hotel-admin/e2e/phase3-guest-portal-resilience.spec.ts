import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// PHASE-3 GUEST PORTAL — the scenarios a lobby actually produces.
//
// phase3-guest-portal.spec.ts covers the happy shapes and the uniform-failure contract. This file covers what
// goes wrong on a guest's phone, in a real browser, running the real client script:
//
//   * a guest taps Connect twice because the first tap seemed to do nothing;
//   * the connection drops mid-request and they tap again with the same details;
//   * they mistyped their room and correct it;
//   * the server takes its full response-time budget to say no;
//   * they pick a package on a keyboard, or with a screen reader, or on a phone-sized viewport;
//   * they hit Back after connecting.
//
// The request-id rule is the through-line for the first three, and it is where this page was wrong.
//
// The id used to be DERIVED from the typed details, so that "the same details" meant "the same attempt". The
// intent was to stop a flaky lobby connection turning one tap into two resolutions. The cost was that any
// field the derivation forgot froze the page — and it forgot `verification`, the field room_any puts the
// guest's one typed value in, so a corrected surname was answered by the original typo until a reload.
//
// The rule now is simply: ONE DELIBERATE SUBMISSION, ONE REQUEST ID. What the derivation was protecting is
// protected where it belongs and always was — the submit button is disabled for the duration of a request so
// one tap makes one call, and a Stay may hold exactly one live Entitlement, so a second Auth Context cannot
// become a second grant (asserted against a real database in cmd/scd's Phase-3 integration suite).

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

/** answerFn decides what the server says to the nth call, so a test can vary it mid-flow. */
type AnswerFn = (n: number, body: Record<string, unknown>) => Promise<unknown> | unknown;

async function serve(page: Page, calls: Call[], answer: AnswerFn, phase3 = true) {
  const html = renderLanding();
  await page.route("**/portal", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/auth-methods", (r: Route) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ pms: { enabled: true, mode: "room_lastname" }, phase3_pms: phase3 }),
    }));

  let n = 0;
  await page.route("**/auth/pms/**", async (route: Route) => {
    const req = route.request();
    let body: Record<string, unknown> = {};
    try { body = req.postDataJSON() ?? {}; } catch { /* no body */ }
    calls.push({ path: new URL(req.url()).pathname, body });
    const a = await answer(n++, body);
    if (a === null) return route.abort("failed"); // the connection dropped
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(a) });
  });

  await page.route("**/success*", (r: Route) =>
    r.fulfill({ status: 200, contentType: "text/html", body: "<h1>You are online</h1>" }));
}

async function submitStay(page: Page, room: string, secondary: string) {
  await page.getByLabel("Room number").fill(room);
  await page.locator("#pms-secondary").fill(secondary);
  await page.getByRole("button", { name: "Connect" }).click();
}

const FAIL = { ok: false, message: UNIFORM_MESSAGE };

// ---------------------------------------------------------------- the request-id rule

test("tapping Connect again after a refusal is a new attempt with a new request id", async ({ page }) => {
  // The realistic sequence: the guest sees the uniform message and taps Connect again without changing
  // anything. That is a DELIBERATE second submission and it must be evaluated, so it carries an id of its own.
  //
  // This used to assert the opposite — that the second call reused the first id — on the reasoning that one
  // guest tapping one button twice should be one resolution. The reasoning conflated two different events:
  // an impatient double tap during a request in flight (which the disabled button already prevents, see "a
  // double tap sends one request, not two") and a considered retry after an answer has been rendered. Only
  // the first is the same attempt. Treating the second as a replay is what made a stored refusal permanent.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? FAIL : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(String(calls[0].body.request_id ?? "")).not.toBe("");
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

test("a dropped connection leaves the page able to try again", async ({ page }) => {
  // The fetch itself throws — no response at all. The guest cannot tell this from a wrong room, and must not
  // be able to.
  //
  // The old contract retried under the SAME id, so that a resolution recorded just before the drop would be
  // replayed rather than duplicated. That is the case the correction gives up, deliberately: the client
  // cannot distinguish "recorded then lost" from "never evaluated", and paying for the first case with a
  // page that freezes in the second is the wrong trade. What it costs is one extra recorded resolution on a
  // dropped connection; what the old behaviour cost was a guest who could not correct a typo.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? null : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

test("correcting a mistyped room is a NEW attempt with a new request id", async ({ page }) => {
  // The other direction. If the id survived a change of details the server would keep answering for the old
  // attempt, and a guest who typed 41 instead of 412 could never get in no matter what they did next.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? FAIL : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "41", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
  expect(calls[1].body.room).toBe("412");
});

test("correcting only the name is also a new attempt", async ({ page }) => {
  // The room is the field a guest is most likely to get right and the name the one they are most likely to
  // spell differently from the PMS. Keying the attempt on the room alone would strand exactly that guest.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? FAIL : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkw");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

test("a double tap sends one request, not two", async ({ page }) => {
  // The guest taps, nothing visibly happens, they tap again. The submit button is disabled for the duration
  // of the request precisely so the second tap lands on nothing.
  const calls: Call[] = [];
  let release: () => void = () => {};
  const held = new Promise<void>((r) => { release = r; });
  await serve(page, calls, async (n) => {
    if (n === 0) { await held; return { ok: true, session_id: "s", redirect_to: "/success" }; }
    return FAIL;
  });

  await page.goto("http://localhost/portal");
  await page.getByLabel("Room number").fill("412");
  await page.locator("#pms-secondary").fill("Okonkwo");
  const btn = page.getByRole("button", { name: "Connect" });
  await btn.click();

  await expect(btn).toBeDisabled();
  await btn.click({ force: true }); // the impatient second tap
  release();

  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls).toHaveLength(1);
});

test("a fresh page after connecting starts a fresh attempt", async ({ page }) => {
  // A spent request id must not be replayed by the next guest on the same device — a shared lobby tablet, or
  // the same guest reconnecting a day later.
  const calls: Call[] = [];
  await serve(page, calls, () => ({ ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

// ---------------------------------------------------------------- what a slow server looks like

test("a server that takes its whole budget to refuse still says only the one thing", async ({ page }) => {
  // The server-side budget makes every non-success arrive at the same offset. The page's job is to stay
  // usable across it: no spinner text that names a cause, no timeout message, and the form still there to
  // try again with.
  const calls: Call[] = [];
  await serve(page, calls, async () => {
    await new Promise((r) => setTimeout(r, 1200)); // the response-time budget
    return FAIL;
  });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  await expect(page.getByRole("button", { name: "Connect" })).toBeEnabled();
  const shown = (await page.locator("#panel-pms").innerText()).toLowerCase();
  for (const forbidden of ["timeout", "timed out", "slow", "unavailable", "try later", "server"]) {
    expect(shown).not.toContain(forbidden);
  }
});

test("the button is re-enabled after a failure so the guest can try again", async ({ page }) => {
  const calls: Call[] = [];
  await serve(page, calls, () => FAIL);
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  await expect(page.getByRole("button", { name: "Connect" })).toBeEnabled();
});

// ---------------------------------------------------------------- the choice step, for everyone

test("the package choice is reachable and operable from the keyboard alone", async ({ page }) => {
  // A guest using a switch device, an external keyboard or a screen reader has to be able to complete this.
  // The choices are real <button>s for that reason; this test is what stops them becoming styled <div>s.
  const calls: Call[] = [];
  await serve(page, calls, (n) =>
    n === 0
      ? {
          ok: true, needs_choice: true, auth_context_id: "ctx",
          choices: [
            { package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
            { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 },
          ],
        }
      : { ok: true, session_id: "s", redirect_to: "/success" });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(2);

  const first = page.locator("#pms-choices button.choice").first();
  await first.focus();
  await expect(first).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls[1].body.package_revision_id).toBe("p1");
});

test("the choice step is announced as a labelled group and the error as a live region", async ({ page }) => {
  // Without the group label a screen reader reads two unrelated buttons after a form vanished; without the
  // live region the uniform failure is silent, and a guest who cannot see the page has no idea they failed.
  const calls: Call[] = [];
  await serve(page, calls, () => ({
    ok: true, needs_choice: true, auth_context_id: "ctx",
    choices: [
      { package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
      { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 },
    ],
  }));

  await page.goto("http://localhost/portal");
  const err = page.locator("#pms-err");
  await expect(err).toHaveAttribute("role", "alert");
  await expect(err).toHaveAttribute("aria-live", "polite");

  await submitStay(page, "412", "Okonkwo");
  const group = page.locator("#pms-choices");
  await expect(group).toHaveAttribute("role", "group");
  await expect(group).toHaveAttribute("aria-label", "Internet packages");
  // every choice is a real, named control
  for (const b of await group.locator("button.choice").all()) {
    expect((await b.innerText()).trim().length).toBeGreaterThan(0);
  }
});

test("the choice step discloses no price and no money of any kind", async ({ page }) => {
  // Paid access is out of scope for Phase 3. A currency symbol on this screen would be the first visible
  // sign of a scope breach, and it would reach guests before it reached anyone reviewing the code.
  const calls: Call[] = [];
  await serve(page, calls, () => ({
    ok: true, needs_choice: true, auth_context_id: "ctx",
    choices: [
      { package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
      { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 },
    ],
  }));
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(2);

  const shown = await page.locator("#pms-choices").innerText();
  for (const token of ["$", "€", "£", "USD", "EUR", "price", "Price", "charge", "per night", "free"]) {
    expect(shown).not.toContain(token);
  }
  // and the package revision id — an internal identifier — is not printed for the guest to read
  expect(shown).not.toContain("p1");
});

test("a refused choice takes the offers down and returns the guest to sign-in", async ({ page }) => {
  // THIS REPLACED THE OPPOSITE BEHAVIOUR, and the change is the product decision -- not an implementation
  // detail. The buttons used to be re-enabled and left on screen after a refusal, so they read as still
  // valid; a real guest pressed three times in fourteen seconds, each attempt refused identically, because
  // nothing indicated the offer set was spent.
  //
  // The portal cannot tell WHY a grant was refused -- the guest-facing envelope is uniform by design -- so it
  // cannot know whether pressing another button is a safe retry or an attempt to spend a consumed Auth
  // Context. Going back to sign-in is correct under every reading, and costs the guest nothing: the request
  // id is retained, so signing in again returns the SAME Auth Context rather than minting a second one.
  const calls: Call[] = [];
  await serve(page, calls, (n) =>
    n === 0 || n === 2
      ? {
          ok: true, needs_choice: true, auth_context_id: "ctx",
          choices: [
            { package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
            { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 },
          ],
        }
      : n === 1
        ? FAIL
        : { ok: true, session_id: "s", redirect_to: "/success" });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  const choices = page.locator("#pms-choices button.choice");
  await expect(choices).toHaveCount(2);

  await choices.first().click();

  // The offer set is GONE, not merely disabled: a disabled button still says "this is your package".
  await expect(choices).toHaveCount(0);
  await expect(page.locator("#pms-choices")).toBeHidden();
  // ...the sign-in form is back and usable...
  await expect(page.locator("#form-pms")).toBeVisible();
  await expect(page.locator("#pms-room")).toBeEditable();
  // ...and the guest is told the same one thing as every other failure.
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  // Signing in again is safe and reaches a package the guest can actually take.
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(2);
  await page.locator("#pms-choices button.choice").nth(1).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls[3].body.package_revision_id).toBe("p2");
});

test("signing in again after a refused choice is a new attempt, and reaches a grantable package", async ({ page }) => {
  // Returning to sign-in must not become a route to two grants — and it does not, but the reason is a SERVER
  // invariant rather than anything this page remembers. A Stay holds exactly one live Entitlement, so the
  // second Auth Context cannot become a second grant whatever id produced it. (Proved against a real database
  // in cmd/scd: TestIntegration_Phase3Auth_RepeatingASuccessCannotDoubleGrant.)
  //
  // This used to assert that the page re-sent the FIRST id. That made the page's own memory load-bearing for
  // a safety property, which is the arrangement that failed: one forgotten field in the derivation and the
  // same memory silently became a trap instead of a guard.
  const calls: Call[] = [];
  await serve(page, calls, (n) =>
    n === 0 || n === 2
      ? {
          ok: true, needs_choice: true, auth_context_id: "ctx",
          choices: [{ package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 }],
        }
      : n === 1
        ? FAIL
        : { ok: true, session_id: "s", redirect_to: "/success" });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await page.locator("#pms-choices button.choice").first().click();
  await expect(page.locator("#form-pms")).toBeVisible();
  await submitStay(page, "412", "Okonkwo");

  const first = calls[0].body.request_id;
  expect(first).toBeTruthy();
  expect(calls[2].body.request_id).not.toBe(first);
  // The re-sign-in reached the offer set again, so the guest has something to press rather than a dead page.
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(1);
});

test("an empty choice list is the uniform message, not an empty screen", async ({ page }) => {
  // A verified guest with nothing on offer is a configuration problem. Rendering an empty group would leave
  // them staring at a page with no form and no explanation and nothing to press.
  const calls: Call[] = [];
  await serve(page, calls, () => ({ ok: true, needs_choice: true, auth_context_id: "ctx", choices: [] }));
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
});

// ---------------------------------------------------------------- the guest's actual device

test("the flow works on a phone-sized viewport without horizontal scrolling", async ({ page }) => {
  // Nearly every guest arrives on a phone, often the smallest one still in service. A choice list that needs
  // sideways scrolling is a choice list half the guests will not find.
  await page.setViewportSize({ width: 320, height: 568 });
  const calls: Call[] = [];
  await serve(page, calls, (n) =>
    n === 0
      ? {
          ok: true, needs_choice: true, auth_context_id: "ctx",
          choices: [
            { package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
            { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 },
          ],
        }
      : { ok: true, session_id: "s", redirect_to: "/success" });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(2);

  const overflows = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
  expect(overflows).toBe(false);

  await page.locator("#pms-choices button.choice").nth(1).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
});

test("going back after connecting does not silently re-submit the attempt", async ({ page }) => {
  // Guests press Back constantly on captive portals, usually because the success page is not the page they
  // were trying to reach. Returning to the form must not fire the spent request id at the server again.
  const calls: Call[] = [];
  await serve(page, calls, () => ({ ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls).toHaveLength(1);

  await page.goBack();
  await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
  expect(calls).toHaveLength(1); // nothing was re-sent
});

test("the form never sends identity, and never sends a package the server did not offer", async ({ page }) => {
  // Identity is derived by the appliance from the connection and its own neighbour table. Anything the page
  // could send about who or where the device is would be a claim, and a claim is what an attacker forges.
  const calls: Call[] = [];
  await serve(page, calls, (n) =>
    n === 0
      ? {
          ok: true, needs_choice: true, auth_context_id: "ctx",
          choices: [{ package_revision_id: "p1", code: "STANDARD", down_kbps: 9000 },
                    { package_revision_id: "p2", code: "PREMIUM", down_kbps: 25000 }],
        }
      : { ok: true, session_id: "s", redirect_to: "/success" });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await page.locator("#pms-choices button.choice").first().click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  for (const c of calls) {
    for (const forbidden of ["ip", "mac", "stay_id", "entitlement_id", "device", "interface_id", "tenant_id", "site_id"]) {
      expect(c.body).not.toHaveProperty(forbidden);
    }
  }
  // the granted package is one the server named in its own offer set
  expect(["p1", "p2"]).toContain(calls[1].body.package_revision_id);
});
