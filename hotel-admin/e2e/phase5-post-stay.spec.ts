import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for the Phase-5 (DARK) post-stay identity screen. edged is fully mocked at the network
// layer — no real backend, no database, no production data, no PMS.
//
// What a browser proves that a unit test cannot: that an operator looking at this screen can tell RESET from
// REVOKE before they click, that the destructive one costs more than a reflex, and that the one-time PIN is
// presented in a way that says out loud it will not come back. Those are properties of the rendered page, not
// of the handler.

type Mutations = { method: string; path: string; body: any }[];

const ACTIVE = {
  id: "prof-1",
  stay_id: "stay-1",
  origin_lifecycle_version: 2,
  external_reservation_id: "RES-77",
  normalized_room_number: "412",
  stay_status: "CHECKED_OUT",
  status: "ACTIVE",
  pin_generation: 1,
  issued_via: "GUEST_AUTHENTICATED_SESSION",
  issued_at: "2026-08-14T09:00:00Z",
  valid_until: "2026-08-15T09:00:00Z",
  revoked_at: null,
  revoke_reason: null,
  authenticable: true,
};

const REVOKED = {
  ...ACTIVE,
  id: "prof-2",
  external_reservation_id: "RES-78",
  normalized_room_number: "413",
  status: "REVOKED",
  authenticable: false,
  revoked_at: "2026-08-14T10:00:00Z",
  revoke_reason: "guest asked us to end it",
};

// STALE EPISODE: still ACTIVE, but the stay moved on, so it cannot authenticate. The screen has to make that
// visible — an operator who cannot see it will reset a PIN that was never going to work.
const STALE = {
  ...ACTIVE,
  id: "prof-3",
  external_reservation_id: "RES-79",
  normalized_room_number: "414",
  authenticable: false,
};

async function installBackend(page: Page, mutations: Mutations, roles = ["site_admin"], rows = [ACTIVE, REVOKED, STALE]) {
  await page.route("**/api/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^\/api\/edge\/v1/, "");
    const method = req.method();

    if (path === "/auth/whoami") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ operator_id: "op-1", email: "op@test.local", roles }),
      });
    }
    if (method === "GET" && path.startsWith("/post-stay-profiles")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ profiles: rows }),
      });
    }
    if (method === "POST" && /\/post-stay-profiles\/.+\/reset$/.test(path)) {
      mutations.push({ method, path, body: req.postDataJSON() });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          profile_id: "prof-1",
          pin: "K7M4RTQX",
          valid_until: "2026-08-15T09:00:00Z",
          notice: "This PIN is shown once.",
        }),
      });
    }
    if (method === "POST" && /\/post-stay-profiles\/.+\/revoke$/.test(path)) {
      mutations.push({ method, path, body: req.postDataJSON() });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ profile_id: "prof-2", status: "REVOKED" }),
      });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
    { name: "sc_edge_session", value: "e2e-test", url: "http://localhost:3123" },
  ]);
}

test.describe("the post-stay identity screen", () => {
  test("shows what each identity can actually do right now", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/post-stay");
    await expect(page.getByRole("heading", { name: /post-stay access/i })).toBeVisible();

    await expect(page.getByRole("cell", { name: "RES-77" })).toBeVisible();
    await expect(page.getByText("Active", { exact: true })).toBeVisible();
    await expect(page.getByText(/revoked — ended for this stay/i)).toBeVisible();
    // The stale one is the interesting row: ACTIVE, and useless. Saying only "Active" would send an operator
    // off to reset a PIN that cannot work whatever its value is.
    await expect(page.getByText(/active, not usable/i)).toBeVisible();
  });

  test("a reset needs a reason and a password, and shows the new PIN exactly once", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/post-stay");

    await page.getByRole("row", { name: /RES-77/ }).getByRole("button", { name: /reset pin/i }).click();
    const dialog = page.getByRole("dialog");
    const submit = dialog.getByRole("button", { name: "Reset PIN", exact: true });
    // Nothing filled in: the action is not available.
    await expect(submit).toBeDisabled();
    await dialog.getByLabel(/reason/i).fill("Guest lost the printout");
    await expect(submit).toBeDisabled(); // still no password
    await dialog.getByLabel(/your password/i).fill("operator-pw");
    await expect(submit).toBeEnabled();
    await submit.click();

    // The one-time reveal, and it says so.
    await expect(page.getByText("K7M4RTQX")).toBeVisible();
    await expect(page.getByText(/shown once/i)).toBeVisible();
    await expect(page.getByText(/cannot be shown again/i)).toBeVisible();

    // What the network actually received is what the screen said it would send.
    expect(mutations).toHaveLength(1);
    expect(mutations[0].path).toMatch(/\/post-stay-profiles\/prof-1\/reset$/);
    expect(mutations[0].body.reason).toBe("Guest lost the printout");
    expect(mutations[0].body.password).toBe("operator-pw");

    // Dismissing the panel loses the PIN — there is no control anywhere that brings it back.
    await page.getByRole("button", { name: /given it to the guest/i }).click();
    await expect(page.getByText("K7M4RTQX")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /show pin/i })).toHaveCount(0);
  });

  test("revoke says it is permanent and costs more than a reflex", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/post-stay");

    await page.getByRole("row", { name: /RES-77/ }).getByRole("button", { name: /end access/i }).click();
    await expect(page.getByText(/cannot be undone/i)).toBeVisible();
    await expect(page.getByText(/no replacement pin/i)).toBeVisible();
    // ...and it points the operator at the action they probably wanted instead.
    await expect(page.getByText(/only lost their pin, reset it instead/i)).toBeVisible();

    const dialog = page.getByRole("dialog");
    const submit = dialog.getByRole("button", { name: /end access permanently/i });
    await dialog.getByLabel(/reason/i).fill("Guest asked us to end it");
    await dialog.getByLabel(/your password/i).fill("operator-pw");
    // A password alone is muscle memory by the third time. The word is the deliberate extra step.
    await expect(submit).toBeDisabled();
    await dialog.getByLabel(/type revoke to confirm/i).fill("revoke");
    await expect(submit).toBeEnabled();
    await submit.click();

    expect(mutations).toHaveLength(1);
    expect(mutations[0].path).toMatch(/\/revoke$/);
    expect(mutations[0].body.reason).toBe("Guest asked us to end it");
    // A revoke must never come back carrying a PIN — that would be a resurrection wearing a rotation's
    // clothes, and the screen would happily display it.
    await expect(page.getByText(/shown once/i)).toHaveCount(0);
  });

  test("an already-revoked identity offers neither action", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/post-stay");
    const row = page.getByRole("row", { name: /RES-78/ });
    await expect(row.getByRole("button", { name: /reset pin/i })).toBeDisabled();
    await expect(row.getByRole("button", { name: /end access/i })).toBeDisabled();
  });

  test("a read-only operator sees the evidence and cannot act", async ({ page }) => {
    await installBackend(page, [], ["site_viewer"]);
    await page.goto("/post-stay");
    await expect(page.getByRole("cell", { name: "RES-77" })).toBeVisible();
    const row = page.getByRole("row", { name: /RES-77/ });
    await expect(row.getByRole("button", { name: /reset pin/i })).toBeDisabled();
    await expect(row.getByRole("button", { name: /end access/i })).toBeDisabled();
  });
});
