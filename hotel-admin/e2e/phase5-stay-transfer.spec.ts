import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for the Phase-5 (DARK) cross-PMS transfer screen. edged is fully mocked at the network
// layer — no real backend, no database, no PMS.
//
// The properties only a browser can settle:
//
//   * the review signals are presented as signals. There is no control next to them that performs anything,
//     so an operator cannot act on an inference nobody made.
//   * the confirm controls DO NOT EXIST until a preview has succeeded. A transfer that could be submitted
//     without looking would be a transfer performed blind.
//   * a blocker is plain language, before any password is typed.

type Mutations = { path: string; body: any }[];

const SIGNALS = {
  signals: [
    { resolved_at: "2026-08-14T09:00:00Z", outcome_code: "AMBIGUOUS", guest_network_id: "gn-1", occurrences: 4 },
  ],
  notice:
    "These are ambiguous authentication outcomes, not transfers. They indicate that a guest's details " +
    "matched on more than one PMS interface and that somebody should look. They are never evidence that a " +
    "guest moved, and no transfer may be based on them.",
};

const GOOD_PREVIEW = {
  from_stay_id: "stay-a", from_external_reservation_id: "RES-A", from_room: "101", from_pms_interface_id: "if-a",
  to_stay_id: "stay-b", to_external_reservation_id: "RES-B", to_room: "201", to_pms_interface_id: "if-b",
  live_devices: 2, live_sessions: 1,
};

const ROOM_MOVE_PREVIEW = {
  ...GOOD_PREVIEW,
  to_stay_id: "stay-a2", to_external_reservation_id: "RES-A2", to_room: "102", to_pms_interface_id: "if-a",
  blocker: "transfer: both stays are on the same PMS interface; this is a room move, not a transfer",
};

async function installBackend(
  page: Page, mutations: Mutations, preview: unknown = GOOD_PREVIEW, roles = ["site_admin"],
) {
  await page.route("**/api/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^\/api\/edge\/v1/, "");
    const method = req.method();
    const json = (body: unknown) =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });

    if (path === "/auth/whoami") return json({ operator_id: "op-1", email: "op@test.local", roles });
    if (method === "GET" && path === "/stay-transfers/review-signals") return json(SIGNALS);
    if (method === "GET" && path.startsWith("/stay-transfers")) return json({ transfers: [] });
    if (method === "POST" && path === "/stay-transfers/preview") {
      mutations.push({ path, body: req.postDataJSON() });
      return json(preview);
    }
    if (method === "POST" && path === "/stay-transfers/execute") {
      mutations.push({ path, body: req.postDataJSON() });
      return json({ transfer_id: "xfer-1", sessions_rebound: 1 });
    }
    return json({});
  });
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
    { name: "sc_edge_session", value: "e2e-test", url: "http://localhost:3123" },
  ]);
}

test.describe("the cross-PMS transfer screen", () => {
  test("presents the review signals as signals, with nothing to act on", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/stay-transfers");
    await expect(page.getByRole("heading", { name: /cross-pms transfer/i })).toBeVisible();

    // The API's own words, rendered rather than paraphrased.
    await expect(page.getByTestId("signal-notice")).toContainText("not transfers");
    await expect(page.getByTestId("signal-notice")).toContainText("never evidence");
    await expect(page.getByRole("cell", { name: "AMBIGUOUS" })).toBeVisible();

    // No control INSIDE the signals section performs anything. Counting the page's buttons would count the
    // navigation too, and the claim is about this section: an operator cannot act from a signal.
    await expect(page.getByTestId("review-signals").getByRole("button")).toHaveCount(0);
    await expect(page.getByTestId("review-signals").getByRole("link")).toHaveCount(0);
  });

  test("the confirm controls do not exist until a preview succeeds", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/stay-transfers");

    await expect(page.getByRole("button", { name: /transfer access/i })).toHaveCount(0);
    await expect(page.getByLabel(/your password/i)).toHaveCount(0);

    await page.getByLabel(/from stay/i).fill("stay-a");
    await page.getByLabel(/to stay/i).fill("stay-b");
    await page.getByRole("button", { name: /preview/i }).click();

    await expect(page.getByTestId("preview-summary")).toContainText("RES-A");
    await expect(page.getByTestId("preview-summary")).toContainText("RES-B");
    // The operator is told what moves and what ends, in the same place they confirm it.
    await expect(page.getByText(/2 device\(s\) and 1 live session\(s\) will move/i)).toBeVisible();
    await expect(page.getByText(/access on the origin stay ends/i)).toBeVisible();

    const submit = page.getByRole("button", { name: /transfer access/i });
    await expect(submit).toBeDisabled();
    await page.getByLabel(/reason/i).fill("Guest moved to the sister property");
    await expect(submit).toBeDisabled();
    await page.getByLabel(/your password/i).fill("operator-pw");
    await expect(submit).toBeEnabled();
    await submit.click();

    await expect(page.getByText(/stayed connected through the change/i)).toBeVisible();
    const exec = mutations.find((m) => m.path.endsWith("/execute"));
    expect(exec).toBeTruthy();
    expect(exec!.body.from_stay_id).toBe("stay-a");
    expect(exec!.body.to_stay_id).toBe("stay-b");
    expect(exec!.body.reason).toBe("Guest moved to the sister property");
  });

  test("a room move is named as one, before anything is typed", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations, ROOM_MOVE_PREVIEW);
    await page.goto("/stay-transfers");
    await page.getByLabel(/from stay/i).fill("stay-a");
    await page.getByLabel(/to stay/i).fill("stay-a2");
    await page.getByRole("button", { name: /preview/i }).click();

    await expect(page.getByText(/cannot be performed/i)).toBeVisible();
    await expect(page.getByText(/room move, not a transfer/i)).toBeVisible();
    // ...and there is nothing to submit.
    await expect(page.getByRole("button", { name: /transfer access/i })).toHaveCount(0);
    await expect(page.getByLabel(/your password/i)).toHaveCount(0);
    expect(mutations.filter((m) => m.path.endsWith("/execute"))).toHaveLength(0);
  });

  test("a read-only operator sees the evidence and cannot preview or transfer", async ({ page }) => {
    await installBackend(page, [], GOOD_PREVIEW, ["site_viewer"]);
    await page.goto("/stay-transfers");
    await expect(page.getByRole("cell", { name: "AMBIGUOUS" })).toBeVisible();
    await expect(page.getByRole("button", { name: /preview/i })).toBeDisabled();
  });
});
