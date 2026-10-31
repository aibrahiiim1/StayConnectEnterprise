import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

type Mutations = { method: string; path: string; body: unknown }[];

async function installBackend(
  page: Page,
  opts: {
    stays?: unknown[];
    stayDetail?: unknown;
    events?: unknown[];
    alerts?: unknown[];
    alertActionStatus?: number;
    mutations: Mutations;
  }
) {
  const list = (data: unknown[]) => JSON.stringify({ data, meta: { has_more: false } });
  const json = (status: number, body: unknown) => ({
    status,
    contentType: "application/json",
    body: typeof body === "string" ? body : JSON.stringify(body),
  });

  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
  ]);

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    let body: unknown;
    try {
      body = req.postDataJSON();
    } catch {
      /* none */
    }
    if (method !== "GET") opts.mutations.push({ method, path, body });

    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));

    if (path.startsWith("/pms-stays/")) return route.fulfill(json(200, opts.stayDetail ?? {}));
    if (path.startsWith("/pms-stays")) return route.fulfill(json(200, list(opts.stays ?? [])));
    if (path.startsWith("/pms-events")) {
      const ev = (opts.events ?? []) as { processing_status?: string; stay_id?: string; received_at?: string }[];
      const n = (s: string) => ev.filter((e) => e.processing_status === s).length;
      return route.fulfill(json(200, {
        data: ev, meta: { has_more: false }, page: 1, page_size: 50, total: ev.length,
        summary: {
          applied: n("APPLIED"), pending: n("PENDING"), manual_review: n("MANUAL_REVIEW"), rejected: n("REJECTED"),
          unmatched: ev.filter((e) => !e.stay_id).length, newest_received_at: ev[0]?.received_at,
        },
      }));
    }
    if (path.startsWith("/operational-alerts") && method === "POST") {
      const st = opts.alertActionStatus ?? 200;
      if (st === 409) return route.fulfill(json(409, { error: "state_conflict", message: "the alert action was refused" }));
      return route.fulfill(json(200, { audit_id: "a1", action: "ACKNOWLEDGED", seq: 2 }));
    }
    if (path.startsWith("/operational-alerts")) return route.fulfill(json(200, list(opts.alerts ?? [])));
    return route.fulfill(json(200, list([])));
  });
  await installCapabilities(page); // a fully licensed hotel appliance (e2e/capabilities.ts)
}

const stay = {
  id: "s1",
  pms_interface_id: "i1",
  external_reservation_id: "R900",
  room: "900",
  status: "CHECKED_OUT",
  lifecycle_version: 1,
  posting_allowed: false,
  occupants: 2,
  effective_checkout_at: "2026-07-21T09:00:00Z",
};

test("stays list shows the stay and its occupants and room charge state on demand", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    stays: [stay],
    stayDetail: {
      ...stay,
      occupant_list: [
        { display_name: "Byron, Ada", is_primary: true },
        { display_name: "Babbage, Chas", is_primary: false },
      ],
      posting_block_reason: "NOT_IN_HOUSE", posting_permission_source: "PMS_FEED",
      posting_blocks: [],
    },
    mutations,
  });
  await page.goto("/stays");
  await page.getByLabel("Filter by status").selectOption("");
  await expect(page.getByText("R900")).toBeVisible();
  await page.getByRole("button", { name: /^View: stay/ }).click();
  await expect(page.getByText("Byron, Ada")).toBeVisible();
  await expect(page.getByRole("region", { name: "Room charge" }).getByText("The guest is not in house")).toBeVisible();
  expect(mutations.filter((m) => m.method !== "GET")).toHaveLength(0);
});

test("stay events open on the whole feed and still show why an event was refused", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    events: [
      {
        id: "e1",
        pms_interface_id: "i1",
        external_event_identity: "FC-2",
        event_type: "GI",
        processing_status: "MANUAL_REVIEW",
        review_code: "FOLIO_CLAIMED_BY_OTHER_STAY",
        received_at: "2026-07-21T09:05:00Z",
      },
    ],
    mutations,
  });
  await page.goto("/stay-events");
  await expect(page.getByText(/folio claimed by other stay/i)).toBeVisible();
  await expect(page.getByLabel(/Filter by what happened to the message/i)).toHaveValue("");
  await expect(page.getByText(/need a decision/i).first()).toBeVisible();
});

test("an operator acknowledges an operational alert", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    alerts: [
      {
        audit_id: "a1",
        stay_id: "s1",
        lifecycle_version: 1,
        alert_code: "EMERGENCY_GRACE_USED",
        trigger: "EMERGENCY_GRACE",
        reason_code: "POLICY_MISMATCH",
        boundary_at: "2026-07-21T09:00:00Z",
        boundary_clock_suspect: false,
        created_at: "2026-07-21T09:01:00Z",
        state: "OPEN",
        seq: 1,
      },
    ],
    mutations,
  });
  await page.goto("/operational-alerts");
  await page.getByRole("button", { name: /^Acknowledge: alert/ }).click();
  await expect
    .poll(() => mutations.find((m) => m.path === "/operational-alerts/a1/acknowledge"))
    .toBeTruthy();
});

test("phase-3 pages are accessible: named controls, one heading, labelled filters", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { stays: [stay], events: [], alerts: [], mutations });

  await page.goto("/stays");
  await expect(page.getByRole("heading", { level: 1, name: "Stays" })).toBeVisible();
  await expect(page.getByLabel("Filter by status")).toBeVisible();

  await page.goto("/stay-events");
  await expect(page.getByRole("heading", { level: 1, name: "PMS activity" })).toBeVisible();
  await expect(page.getByLabel(/Filter by what happened to the message/i)).toBeVisible();

  const unnamed = await page.evaluate(() => {
    const els = Array.from(document.querySelectorAll("button, a[href], select, input"));
    return els.filter((el) => {
      const label = (el.getAttribute("aria-label") || el.textContent || "").trim();
      const id = el.getAttribute("id");
      const labelled = id ? !!document.querySelector(`label[for="${id}"]`) : !!el.closest("label");
      return !label && !labelled;
    }).length;
  });
  expect(unnamed).toBe(0);
});

test("an alert changed by someone else refreshes the queue instead of overwriting it", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    alerts: [{
      audit_id: "a1", stay_id: "s1", lifecycle_version: 1, alert_code: "EMERGENCY_GRACE_USED",
      trigger: "EMERGENCY_GRACE", boundary_at: "2026-07-21T09:00:00Z",
      boundary_clock_suspect: false, created_at: "2026-07-21T09:01:00Z", state: "OPEN", seq: 1,
    }],
    alertActionStatus: 409,
    mutations,
  });
  await page.goto("/operational-alerts");
  await page.getByRole("button", { name: /^Acknowledge: alert/ }).click();
  await expect(page.getByText(/changed while you were looking at it/i)).toBeVisible();
});
