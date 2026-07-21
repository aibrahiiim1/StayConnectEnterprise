import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for the Phase-3 (DARK) Hotel-Admin surface. The edged backend is fully mocked at the
// network layer — no real backend, no database, no production data, no PMS. The Next server under test runs
// with NEXT_PUBLIC_PHASE3_ADMIN=1 (a TEST-only flag-ON profile, never the deployed dark bundle).

type Mutations = { method: string; path: string; body: unknown }[];

async function installBackend(
  page: Page,
  opts: {
    stays?: unknown[];
    stayDetail?: unknown;
    events?: unknown[];
    alerts?: unknown[];
    unpublished?: boolean;
    alertActionStatus?: number;
    packages?: unknown[];
    grace?: unknown;
    gracePutStatus?: number;
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
    if (path.startsWith("/pms-events")) return route.fulfill(json(200, list(opts.events ?? [])));
    if (path.startsWith("/operational-alerts") && method === "POST") {
      const st = opts.alertActionStatus ?? 200;
      if (st === 409) return route.fulfill(json(409, { error: "state_conflict", message: "the alert action was refused" }));
      return route.fulfill(json(200, { audit_id: "a1", action: "ACKNOWLEDGED", seq: 2 }));
    }
    if (path.startsWith("/operational-alerts")) return route.fulfill(json(200, list(opts.alerts ?? [])));
    if (path === "/checkout-grace" && method === "PUT") {
      const st = opts.gracePutStatus ?? 200;
      if (st === 409) return route.fulfill(json(409, { error: "version_conflict", message: "current version is 9" }));
      if (st !== 200) return route.fulfill(json(st, { error: "bad_request", message: "the checkout-grace policy was refused" }));
      return route.fulfill(json(200, { config_version: 8 }));
    }
    if (path === "/checkout-grace/packages") {
      return route.fulfill(json(200, JSON.stringify({ data: opts.packages ?? [gracePackage], meta: { has_more: false } })));
    }
    if (path === "/checkout-grace") {
      if (opts.unpublished) {
        return route.fulfill(json(200, { published: false, config_version: 0, supported_device_policies: ["REJECT_NEW_DEVICE"] }));
      }
      return route.fulfill(json(200, {
        published: true, config_version: 7,
        supported_device_policies: ["REJECT_NEW_DEVICE"],
        policy: opts.grace ?? {},
      }));
    }
    return route.fulfill(json(200, list([])));
  });
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

const gracePackage = {
  package_revision_id: "rev-1", package_code: "site-grace-pkg", revision_no: 1,
  service_plan_revision_id: "plan-1", service_plan_code: "site-grace-plan",
  down_kbps: 4000, up_kbps: 1500, data_quota_bytes: 524288000, device_limit: 2,
  device_limit_policy: "REJECT_NEW_DEVICE", grace_duration_seconds: 3600,
  settlement_mode: "NOT_REQUIRED", is_current: true, selected: true,
};

const graceCfg = {
  grace_package_revision_id: null,
  grace_duration_seconds: 3600,
  grace_down_kbps: 4000,
  grace_up_kbps: 1500,
  grace_data_quota_bytes: 524288000,
  grace_device_limit: 2,
  grace_device_limit_policy: "REJECT_NEW_DEVICE",
  eligibility_window_seconds: 86400,
  config_version: 7,
};

test("stays list shows the stay and its occupants/folios on demand", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    stays: [stay],
    stayDetail: {
      ...stay,
      occupant_list: [
        { display_name: "Byron, Ada", is_primary: true },
        { display_name: "Babbage, Chas", is_primary: false },
      ],
      folios: [{ external_folio_id: "F900", folio_kind: "GUEST", status: "OPEN", is_default_posting_target: true }],
    },
    mutations,
  });
  await page.goto("/stays");
  await expect(page.getByText("R900")).toBeVisible();
  await page.getByRole("button", { name: "Details" }).click();
  await expect(page.getByText("Byron, Ada")).toBeVisible();
  await expect(page.getByText("F900", { exact: false })).toBeVisible();
  // a read-only page issues no mutations at all
  expect(mutations.filter((m) => m.method !== "GET")).toHaveLength(0);
});

test("stay events open on the review queue and show why an event was refused", async ({ page }) => {
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
  await expect(page.getByText("FOLIO_CLAIMED_BY_OTHER_STAY")).toBeVisible();
  await expect(page.getByLabel("Filter by processing status")).toHaveValue("MANUAL_REVIEW");
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
  await page.getByRole("button", { name: /Acknowledge alert/ }).click();
  await expect
    .poll(() => mutations.find((m) => m.path === "/operational-alerts/a1/acknowledge"))
    .toBeTruthy();
});

test("publishing the checkout-grace policy sends the COMPLETE policy and reports the new version", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { grace: graceCfg, mutations });
  await page.goto("/checkout-grace");
  // the operator chooses a package; the numbers are the package's own pinned values, shown read-only
  await expect(page.getByText("4000 kbps")).toBeVisible();
  await page.getByLabel("Confirm your password", { exact: true }).fill("operator-pw");
  await page.getByRole("button", { name: /Publish policy/ }).click();

  await expect.poll(() => mutations.find((m) => m.path === "/checkout-grace" && m.method === "PUT")).toBeTruthy();
  const req = mutations.find((m) => m.path === "/checkout-grace" && m.method === "PUT")!;
  const sent = req.body as Record<string, unknown>;
  for (const k of Object.keys(graceCfg)) expect(sent).toHaveProperty(k);
  expect(sent.grace_package_revision_id).toBe("rev-1");
  expect(sent.grace_duration_seconds).toBe(3600);
  expect(sent.grace_down_kbps).toBe(4000);
  // governed publication: the version the operator read, a bounded reason and a password confirmation
  expect(sent.expected_config_version).toBe(7);
  expect(sent.reason_code).toBeTruthy();
  expect(sent.password).toBe("operator-pw");
  await expect(page.getByRole("status")).toHaveText(/version 8/);
});

test("a refused policy is surfaced, not silently swallowed", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { grace: graceCfg, gracePutStatus: 400, mutations });
  await page.goto("/checkout-grace");
  await page.getByRole("button", { name: /Publish policy/ }).click();
  // the refusal reaches the operator verbatim rather than being swallowed into a success state
  await expect(page.getByText(/refused/i)).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
});

test("a site with no published policy starts from defaults rather than an error", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { unpublished: true, mutations });
  await page.goto("/checkout-grace");
  // "no policy published yet" is a starting point, not a failure the operator has to interpret
  await expect(page.getByText(/nothing published yet/)).toBeVisible();
  await expect(page.getByText(/Failed to load the checkout-grace policy/)).toHaveCount(0);
});

// Accessibility: the Phase-3 pages must be operable and understandable without sight — every control carries a
// name, the page has one main heading, and the filters are labelled.
test("phase-3 pages are accessible: named controls, one heading, labelled filters", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { stays: [stay], events: [], alerts: [], grace: graceCfg, mutations });

  await page.goto("/stays");
  await expect(page.getByRole("heading", { level: 1, name: "Stays" })).toBeVisible();
  await expect(page.getByLabel("Filter by status")).toBeVisible();

  await page.goto("/stay-events");
  await expect(page.getByRole("heading", { level: 1, name: "Stay events" })).toBeVisible();
  await expect(page.getByLabel("Filter by processing status")).toBeVisible();

  await page.goto("/checkout-grace");
  await expect(page.getByRole("heading", { level: 1, name: "Checkout grace" })).toBeVisible();
  for (const label of [
    "Grace package",
    "Eligibility window (seconds)",
    "Reason",
    "Confirm your password",
  ]) {
    await expect(page.getByLabel(label, { exact: true })).toBeVisible();
  }

  // every interactive control on the page has an accessible name
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

test("a policy published by someone else is a conflict the operator can see, not an overwrite", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { grace: graceCfg, gracePutStatus: 409, mutations });
  await page.goto("/checkout-grace");
  await page.getByLabel("Confirm your password", { exact: true }).fill("operator-pw");
  await page.getByRole("button", { name: /Publish policy/ }).click();
  await expect(page.getByText(/newer policy/i)).toBeVisible();
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
  await page.getByRole("button", { name: /Acknowledge alert/ }).click();
  await expect(page.getByText(/changed while you were looking at it/i)).toBeVisible();
});
