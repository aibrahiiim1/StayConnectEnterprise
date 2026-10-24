import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

type Mutations = { method: string; path: string; body: unknown }[];

async function installBackend(page: Page, opts: {
  packages?: unknown[];
  plans?: unknown[];
  revisions?: Record<string, unknown[]>;
  packagesStatus?: number;
  gracePut?: { status: number; body: unknown };
  mutations: Mutations;
}) {
  const list = (data: unknown[]) => JSON.stringify({ data, meta: { has_more: false } });
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: typeof body === "string" ? body : JSON.stringify(body) });

  // Middleware checks cookie presence; whoami supplies the mock identity.
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    let body: unknown;
    try { body = req.postDataJSON(); } catch { /* none */ }
    if (method !== "GET") opts.mutations.push({ method, path, body });

    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));

    if (path === "/commercial-packages" && method === "POST") return route.fulfill(json(200, { package_id: "pk-new", current_revision_id: "rev-new" }));
    if (path === "/commercial-packages" && method === "GET") {
      if (opts.packagesStatus === 503) return route.fulfill(json(503, { error: "phase2_disabled" }));
      return route.fulfill(json(200, list(opts.packages ?? [])));
    }
    if (path === "/commercial-packages/plans" && method === "POST") return route.fulfill(json(200, { plan_id: "pl-new", current_revision_id: "rev-new" }));
    if (path === "/commercial-packages/plans" && method === "GET") return route.fulfill(json(200, list(opts.plans ?? [])));
    if (/^\/commercial-packages\/plans\/[^/]+\/revisions$/.test(path)) return route.fulfill(json(200, list([])));
    const rev = path.match(/^\/commercial-packages\/([^/]+)\/revisions$/);
    if (rev) return route.fulfill(json(200, list(opts.revisions?.[rev[1]] ?? [])));
    if (/^\/commercial-packages\/[^/]+\/active$/.test(path)) return route.fulfill(json(200, { active: false }));
    if (path === "/commercial-packages/grace" && method === "PUT") { const g = opts.gracePut ?? { status: 200, body: { grace_package_revision_id: "r1" } }; return route.fulfill(json(g.status, g.body)); }
    if (path === "/commercial-packages/grace" && method === "GET") return route.fulfill(json(200, { grace_package_revision_id: "", config: {} }));
    if (path === "/commercial-packages/guest-activity") return route.fulfill(json(200, list([{
      quote_id: "q1", purchase_id: "pu1", package_revision_id: "r1",
      offered_at: "2026-08-01T00:00:00Z", taken_at: "2026-08-01T00:05:00Z", expires_at: "2026-08-01T00:10:00Z",
      room: "101", pms_interface: "Protel", reservation: "RES-1", stay_id: "st1",
      sign_in_method: "PMS", package: "Free Internet Package", package_type: "GENERAL",
      price_minor: 0, currency: "USD", outcome: "TAKEN", trigger: "GUEST_SELECTION",
      service_plan: "Free Internet", quota_bytes: 1000000000, entitlement_id: "e1",
    }])));
    if (path === "/commercial-packages/activity") return route.fulfill(json(200, {
      data: [
        {
          purchase_id: "pu1", entitlement_id: "e1", package_id: "pk1", package_code: "FREEWIFI",
          package_name: "Free Internet Package", package_revision_id: "r1", revision_no: 2,
          price_minor: 0, currency: "USD", currency_exponent: 2,
          source: "GUEST_SELECTION", source_label: "Chosen on the portal", purchase_state: "GRANTED", status: "ACTIVE",
          sign_in_kind: "STAY", stay_id: "st1", room: "101", pms_interface: "Protel", reservation: "RES-1",
          had_offer: true, offer_taken_at: "2026-08-01T00:05:00Z",
          started_at: "2026-08-01T00:05:00Z", occurred_at: "2026-08-01T00:05:00Z",
          service_plan: "Free Internet", quota_bytes: 1000000000,
          sessions: 2, devices: 1, bytes_down: 5000000, bytes_up: 100000, online_now: true,
          usage_href: "/usage?stay=st1",
        },
        {
          purchase_id: "pu2", entitlement_id: "e2", package_id: "pk1", package_code: "FREEWIFI",
          package_name: "Free Internet Package", package_revision_id: "r1", revision_no: 2,
          price_minor: 0, currency: "USD", currency_exponent: 2,
          source: "VOUCHER_REDEMPTION", source_label: "Voucher", purchase_state: "GRANTED", status: "TERMINATED",
          end_reason: "TIME", sign_in_kind: "VOUCHER", had_offer: false,
          started_at: "2026-08-01T01:00:00Z", ended_at: "2026-08-01T02:00:00Z", occurred_at: "2026-08-01T01:00:00Z",
          sessions: 1, devices: 1, bytes_down: 1000, bytes_up: 1000, online_now: false,
        },
      ],
      meta: { total: 2, limit: 25, offset: 0, has_more: false },
      summary: {
        in_range: 2, started_in_range: 2, status_counts: { active: 1, ended: 1, other: 0 },
        data_bytes: 5102000, active_now: 1, undated: 0,
        by_package: [{ package_id: "pk1", code: "FREEWIFI", name: "Free Internet Package", is_system: false, grants: 2 }],
        by_source: [{ source: "GUEST_SELECTION", label: "Chosen on the portal", grants: 1 }, { source: "VOUCHER_REDEMPTION", label: "Voucher", grants: 1 }],
        active_by_package: [{ package_id: "pk1", grants: 1 }],
      },
      range: { from: "2026-07-25T00:00:00Z", to: "2026-08-01T12:00:00Z" },
    }));
    if (/^\/commercial-packages\/[^/]+\/deletability$/.test(path)) return route.fulfill(json(200, {
      deletable: false,
      reasons: [
        { code: "ENTITLEMENTS", message: "2 internet grants given to guests record this package.", count: 2 },
      ],
    }));
    if (path === "/commercial-packages/quotes") return route.fulfill(json(200, list([{ id: "q1", package_revision_id: "r1", price_minor: 0, currency: "USD", expires_at: "2026-08-01T00:00:00Z", consumed_at: null }])));
    if (path === "/commercial-packages/purchases") return route.fulfill(json(200, list([{ id: "pu1", package_revision_id: "r1", state: "GRANTED", amount_minor: 0, currency: "USD" }])));

    return route.fulfill(json(200, list([])));
  });
  await installCapabilities(page); // a fully licensed hotel appliance (e2e/capabilities.ts)
}

test("nav shows Internet packages (flag-ON build) and lists packages", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { packages: [{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 2 }], plans: [], mutations });
  await page.goto("/internet-packages");
  await expect(page.getByRole("link", { name: "Internet packages" })).toBeVisible();
  await expect(page.getByText("FREEWIFI")).toBeVisible();
});

test("approved disabled behavior on 503 makes no commerce mutation requests", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { packagesStatus: 503, mutations });
  await page.goto("/internet-packages");
  await expect(page.getByText(/not switched on/i)).toBeVisible();
  expect(mutations.filter((m) => m.method !== "GET")).toHaveLength(0);
});

test("service plans: a new plan publishes with the supported time-accounting mode", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    plans: [{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }],
    mutations,
  });
  await page.goto("/service-plans");
  // Scope the submit to its form because the trigger has the same label.
  await page.getByRole("button", { name: /add plan/i }).click();
  await page.locator('input[name="code"]').fill("PLATINUM");
  await page.getByRole("dialog").locator('button[type="submit"]').click();
  await expect.poll(() => mutations.find((m) => m.path.endsWith("/plans") && m.method === "POST")).toBeTruthy();
  const planReq = mutations.find((m) => m.path.endsWith("/plans") && m.method === "POST")!;
  expect((planReq.body as { time_accounting_mode: string }).time_accounting_mode).toBe("VALIDITY_WINDOW");
});

test("internet packages: publish via the plan selector, then step-up deactivate", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    packages: [{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r2", revision_count: 2 }],
    plans: [{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }],
    revisions: { pk1: [{ revision_id: "r2", revision_no: 2, is_current: true, package_type: "GENERAL", price_minor: 0, currency: "USD" }] },
    mutations,
  });
  await page.goto("/internet-packages");
  await expect(page.getByText("FREEWIFI")).toBeVisible();

  await page.getByRole("button", { name: /add package/i }).click();
  await page.getByRole("dialog").getByTestId("code").fill("FREEWIFI2");
  await page.getByRole("dialog").getByTestId("service-plan").selectOption("p1");
  await page.locator('form button[type="submit"]').click();
  await expect.poll(() => mutations.find((m) => m.path.endsWith("/commercial-packages") && m.method === "POST")).toBeTruthy();
  const pkgReq = mutations.find((m) => m.path.endsWith("/commercial-packages") && m.method === "POST")!;
  const pkgBody = pkgReq.body as { price_minor?: number; acquisition_methods?: string[] };
  expect(pkgBody.price_minor).toBe(0);
  expect(pkgBody.acquisition_methods).toEqual(["NOT_REQUIRED"]);
  const pkgJson = JSON.stringify(pkgReq.body).toLowerCase();
  expect(pkgJson).not.toMatch(/pms|tax|settlement|posting|room_charge|online_payment|prepaid/); // free-only, no PMS
  expect((pkgReq.body as { service_plan_revision_id: string }).service_plan_revision_id).toBe("rev-gold");
  expect((pkgReq.body as { create_only?: boolean }).create_only).toBe(true);

  let browserDialogs = 0;
  page.on("dialog", (d) => { browserDialogs += 1; d.dismiss(); });
  await page.getByRole("button", { name: /^Actions for / }).first().click();
  await page.getByRole("menuitem", { name: /^disable/i }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByRole("button", { name: /stop offering it/i })).toBeDisabled();
  await confirm.getByLabel(/why are you disabling it/i).fill("retire it");
  await confirm.getByLabel(/confirm your password/i).fill("operatorpw");
  await expect(confirm.locator('input[type="password"]')).toHaveCount(1);
  await confirm.getByRole("button", { name: /stop offering it/i }).click();
  await expect.poll(() => mutations.find((m) => m.path.includes("/active"))).toBeTruthy();
  expect(browserDialogs, "a browser prompt/confirm was used for the step-up").toBe(0);
  const actReq = mutations.find((m) => m.path.includes("/active"))!;
  expect(actReq.body).toMatchObject({ active: false, reason: "retire it", password: "operatorpw" });
});

test("internet packages: guest activity rows are sanitized and carry no guest PII", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    packages: [{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r2", revision_count: 2 }],
    mutations,
  });
  await page.goto("/internet-packages");
  await page.getByRole("tab", { name: /client activity/i }).click();

  // Include grants without portal offers, such as vouchers.
  await expect(page.getByText(/Room 101/)).toBeVisible();
  await expect(page.locator("tbody").getByText("Free Internet Package").first()).toBeVisible();
  await expect(page.getByText("A voucher client")).toBeVisible();
  await expect(page.getByText("In use").first()).toBeVisible();

  await page.locator("tbody").getByText("Free Internet Package").first().click();
  await expect(page.getByRole("link", { name: /open this stay/i })).toHaveAttribute("href", "/usage?stay=st1");
  await page.keyboard.press("Escape");

  // Ignore the operator's own password link when checking guest-data exposure.
  const html = (await page.content()).toLowerCase().split('href="/account/password"').join("");
  for (const pii of ["auth_context", "device_id", "guest_network", "voucher_id", "guest_account", "password\"", "mac address"]) {
    expect(html).not.toContain(pii);
  }
  expect(html).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/);
});

test("internet packages: Delete… of a used package explains what is attached and never deletes", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    packages: [{ package_id: "pk1", code: "FREEWIFI", name: "Free WiFi", active: true, current_revision_id: "r2", revision_count: 2 }],
    mutations,
  });
  await page.goto("/internet-packages");
  await page.locator("tbody").getByText("Free WiFi", { exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: /delete…/i }).click();
  await expect(page.getByText("2 internet grants given to guests record this package.")).toBeVisible();
  await expect(page.getByText(/why it can't be deleted/i)).toBeVisible();
  await expect(page.getByRole("button", { name: /^delete package$/i })).toHaveCount(0);
  await page.getByRole("button", { name: /disable instead/i }).click();
  await expect(page.getByLabel(/why are you disabling it/i)).toBeVisible();
  expect(mutations).toHaveLength(0);
});
