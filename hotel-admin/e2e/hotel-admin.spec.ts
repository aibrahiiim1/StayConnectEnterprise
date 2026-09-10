import { test, expect, type Page, type Route } from "@playwright/test";

// The edged backend is fully mocked at the network layer; no real backend / DB / production data.
// The Next app under test was built with NEXT_PUBLIC_PHASE2_ADMIN=1 (flag-ON profile) — a TEST-only
// build, never the deployed dark bundle.

type Mutations = { method: string; path: string; body: unknown }[];

// A single dispatcher intercepts every edged call (robust vs per-glob matching). GETs return fixtures;
// mutations (POST/PUT) are captured and answered. No real backend is contacted.
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

  // The middleware gate only checks presence of the httpOnly session cookie; the client whoami mock below
  // supplies the actual (test) operator identity.
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    let body: unknown;
    try { body = req.postDataJSON(); } catch { /* none */ }
    if (method !== "GET") opts.mutations.push({ method, path, body });

    // auth
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));

    // packages collection
    if (path === "/commercial-packages" && method === "POST") return route.fulfill(json(200, { package_id: "pk-new", current_revision_id: "rev-new" }));
    if (path === "/commercial-packages" && method === "GET") {
      if (opts.packagesStatus === 503) return route.fulfill(json(503, { error: "phase2_disabled" }));
      return route.fulfill(json(200, list(opts.packages ?? [])));
    }
    // plans
    if (path === "/commercial-packages/plans" && method === "POST") return route.fulfill(json(200, { plan_id: "pl-new", current_revision_id: "rev-new" }));
    if (path === "/commercial-packages/plans" && method === "GET") return route.fulfill(json(200, list(opts.plans ?? [])));
    if (/^\/commercial-packages\/plans\/[^/]+\/revisions$/.test(path)) return route.fulfill(json(200, list([])));
    // package revisions
    const rev = path.match(/^\/commercial-packages\/([^/]+)\/revisions$/);
    if (rev) return route.fulfill(json(200, list(opts.revisions?.[rev[1]] ?? [])));
    // activate/deactivate
    if (/^\/commercial-packages\/[^/]+\/active$/.test(path)) return route.fulfill(json(200, { active: false }));
    // grace
    if (path === "/commercial-packages/grace" && method === "PUT") { const g = opts.gracePut ?? { status: 200, body: { grace_package_revision_id: "r1" } }; return route.fulfill(json(g.status, g.body)); }
    if (path === "/commercial-packages/grace" && method === "GET") return route.fulfill(json(200, { grace_package_revision_id: "", config: {} }));
    // inspection
    if (path === "/commercial-packages/quotes") return route.fulfill(json(200, list([{ id: "q1", package_revision_id: "r1", price_minor: 0, currency: "USD", expires_at: "2026-08-01T00:00:00Z", consumed_at: null }])));
    if (path === "/commercial-packages/purchases") return route.fulfill(json(200, list([{ id: "pu1", package_revision_id: "r1", state: "GRANTED", amount_minor: 0, currency: "USD" }])));

    return route.fulfill(json(200, list([])));
  });
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
  // no publish/activate/plan-create/grace mutation was issued
  expect(mutations.filter((m) => m.method !== "GET")).toHaveLength(0);
});

// The single "full admin flow" test covered four screens that were four tabs. Service plans and checkout
// grace are now their own pages, so the walk is split — deliberately keeping every CONTRACT the original
// asserted (VALIDITY_WINDOW on plan publish, plan chosen by selector rather than raw UUID, no price/PMS
// field in the package payload, two-prompt step-up on deactivate, sanitized inspection rows) rather than
// dropping the ones that became inconvenient to reach.

test("service plans: a new plan publishes with the supported time-accounting mode", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    plans: [{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }],
    mutations,
  });
  await page.goto("/service-plans");
  // "Add plan" opens the form, and the form's own submit carries the same words -- so the submit is taken
  // from the form rather than by name, which would match both.
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

  // Published through the plan SELECTOR, which is keyed by PLAN -- the operator never sees, types or
  // selects a revision id. The revision the save pins is resolved from the chosen plan.
  await page.getByRole("button", { name: /add package/i }).click();
  await page.getByLabel("code").fill("FREEWIFI2");
  await page.getByLabel("service-plan").selectOption("p1");
  // The per-tier speed step lives under Advanced now and is not what this test is about: the package's
  // speed comes from the chosen plan, and the form starts with the one grant tier a package needs.
  await page.locator('form button[type="submit"]').click();
  await expect.poll(() => mutations.find((m) => m.path.endsWith("/commercial-packages") && m.method === "POST")).toBeTruthy();
  const pkgReq = mutations.find((m) => m.path.endsWith("/commercial-packages") && m.method === "POST")!;
  const pkgJson = JSON.stringify(pkgReq.body).toLowerCase();
  expect(pkgJson).not.toMatch(/price|settlement|pms|tax|currency/); // free-only, no PMS
  expect((pkgReq.body as { service_plan_revision_id: string }).service_plan_revision_id).toBe("rev-gold");

  // Withdrawing a package still takes a reason AND the operator's password -- now in a confirmation dialog
  // that states what stops, with the password masked. No browser prompt is used anywhere.
  let browserDialogs = 0;
  page.on("dialog", (d) => { browserDialogs += 1; d.dismiss(); });
  await page.getByRole("button", { name: /^disable$/i }).click();
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
  await page.getByRole("tab", { name: /guest activity/i }).click();
  await expect(page.getByText(/^q1/)).toBeVisible();
  await expect(page.getByText("GRANTED")).toBeVisible();
  const html = (await page.content()).toLowerCase();
  for (const pii of ["auth_context", "device_id", "guest_network", "voucher_id", "guest_account", "password\"", "mac address"]) {
    expect(html).not.toContain(pii);
  }
});
