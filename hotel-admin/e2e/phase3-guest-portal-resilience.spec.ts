import { test, expect, type Page, type Route } from "@playwright/test";
import { portalHTML as renderLanding } from "./portal-page";

// Give each deliberate submission a new request ID; disable the button in flight to prevent duplicate taps.
// The server allows only one live entitlement per stay, even if retries create another Auth Context.

const UNIFORM_MESSAGE =
  "We could not verify your stay. Please check your details or contact the site team for assistance.";

const TRANSPORT_MESSAGE = "Something went wrong. Please try again.";

type Call = { path: string; body: Record<string, unknown> };

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
  await page.locator("#pms-room").fill(room);
  await page.locator("#pms-secondary").fill(secondary);
  await page.getByRole("button", { name: "Submit" }).click();
}

const FAIL = { ok: false, message: UNIFORM_MESSAGE };

test("tapping Connect again after a refusal is a new attempt with a new request id", async ({ page }) => {
  // A retry after a refusal is a new attempt; an in-flight double tap is still one submission.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? FAIL : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await page.getByRole("button", { name: "Submit" }).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(String(calls[0].body.request_id ?? "")).not.toBe("");
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

test("a dropped connection leaves the page able to try again", async ({ page }) => {
  // No server response reveals no guest facts, so a network failure can show a technical retry message.
  const calls: Call[] = [];
  await serve(page, calls, (n) => (n === 0 ? null : { ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(TRANSPORT_MESSAGE);
  await expect(page.locator("#pms-err")).not.toHaveText(UNIFORM_MESSAGE);

  await page.getByRole("button", { name: "Submit" }).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();

  expect(calls).toHaveLength(2);
  expect(calls[1].body.request_id).not.toBe(calls[0].body.request_id);
});

test("correcting a mistyped room is a NEW attempt with a new request id", async ({ page }) => {
  // Changed details need a new ID or the server replays the old answer.
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
  const calls: Call[] = [];
  let release: () => void = () => {};
  const held = new Promise<void>((r) => { release = r; });
  await serve(page, calls, async (n) => {
    if (n === 0) { await held; return { ok: true, session_id: "s", redirect_to: "/success" }; }
    return FAIL;
  });

  await page.goto("http://localhost/portal");
  await page.locator("#pms-room").fill("412");
  await page.locator("#pms-secondary").fill("Okonkwo");
  const btn = page.getByRole("button", { name: "Submit" });
  await btn.click();

  await expect(btn).toBeDisabled();
  await btn.click({ force: true }); // the impatient second tap
  release();

  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls).toHaveLength(1);
});

test("a fresh page after connecting starts a fresh attempt", async ({ page }) => {
  // Do not reuse a spent request ID for a later guest on the same device.
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

test("a server that takes its whole budget to refuse still says only the one thing", async ({ page }) => {
  // Keep the form usable through the uniform server response delay without naming a refusal cause.
  const calls: Call[] = [];
  await serve(page, calls, async () => {
    await new Promise((r) => setTimeout(r, 1200)); // the response-time budget
    return FAIL;
  });

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");

  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  await expect(page.getByRole("button", { name: "Submit" })).toBeEnabled();
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
  await expect(page.getByRole("button", { name: "Submit" })).toBeEnabled();
});

test("the package choice is reachable and operable from the keyboard alone", async ({ page }) => {
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
  // Group labels and live refusals keep the choice step understandable to screen readers.
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
  // role="alert" already implies assertive live announcements.
  await expect(err).toHaveAttribute("role", "alert");
  await expect(err).not.toHaveAttribute("aria-live", "off");

  await submitStay(page, "412", "Okonkwo");
  const group = page.locator("#pms-choices");
  await expect(group).toHaveAttribute("role", "group");
  await expect(group).toHaveAttribute("aria-label", "Choose your internet package");
  for (const b of await group.locator("button.choice").all()) {
    expect((await b.innerText()).trim().length).toBeGreaterThan(0);
  }
});

test("the choice step discloses no price and no money of any kind", async ({ page }) => {
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
  expect(shown).not.toContain("p1");
});

test("a refused choice takes the offers down and returns the guest to sign-in", async ({ page }) => {
  // Uniform grant refusals cannot reveal whether an Auth Context was spent; return to sign-in before retrying.
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

  await expect(choices).toHaveCount(0);
  await expect(page.locator("#pms-choices")).toBeHidden();
  await expect(page.locator("#form-pms")).toBeVisible();
  await expect(page.locator("#pms-room")).toBeEditable();
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);

  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(2);
  await page.locator("#pms-choices button.choice").nth(1).click();
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls[3].body.package_revision_id).toBe("p2");
});

test("signing in again after a refused choice is a new attempt, and reaches a grantable package", async ({ page }) => {
  // The server's one-live-entitlement rule prevents duplicate grants across Auth Contexts.
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
  await expect(page.locator("#pms-choices button.choice")).toHaveCount(1);
});

test("an empty choice list is the uniform message, not an empty screen", async ({ page }) => {
  const calls: Call[] = [];
  await serve(page, calls, () => ({ ok: true, needs_choice: true, auth_context_id: "ctx", choices: [] }));
  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.locator("#pms-err")).toHaveText(UNIFORM_MESSAGE);
  await expect(page.getByRole("button", { name: "Submit" })).toBeVisible();
});

test("the flow works on a phone-sized viewport without horizontal scrolling", async ({ page }) => {
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
  // Browser Back must not resubmit a spent request ID.
  const calls: Call[] = [];
  await serve(page, calls, () => ({ ok: true, session_id: "s", redirect_to: "/success" }));

  await page.goto("http://localhost/portal");
  await submitStay(page, "412", "Okonkwo");
  await expect(page.getByRole("heading", { name: "You are online" })).toBeVisible();
  expect(calls).toHaveLength(1);

  await page.goBack();
  await expect(page.getByRole("button", { name: "Submit" })).toBeVisible();
  expect(calls).toHaveLength(1); // nothing was re-sent
});

test("the form never sends identity, and never sends a package the server did not offer", async ({ page }) => {
  // Derive device identity from the appliance network view, not guest-supplied claims.
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
  expect(["p1", "p2"]).toContain(calls[1].body.package_revision_id);
});
