import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for the PMS INTERFACE admin surface. edged is fully mocked at the network layer — no real
// backend, no database, no production data, no PMS. The Next server under test runs with
// NEXT_PUBLIC_PHASE3_ADMIN=1, a TEST-only flag-ON profile that is never the deployed dark bundle.
//
// What a browser proves that jsdom cannot: the pages actually render and are operable end to end, the forms
// submit what the network sees, and every control has an accessible name in a real accessibility tree.

type Mutations = { method: string; path: string; body: any }[];

const IFACE = {
  id: "i1",
  connector_kind: "protel-fias",
  display_label: "Main PMS",
  lifecycle_state: "ACTIVE",
  current_revision_id: "r1",
  current_revision_no: 1,
  revision_count: 2,
  published: true,
  secret_generation: 3,
  secret_rotated_at: new Date().toISOString(),
};

const REVISIONS = [
  {
    id: "r2", revision_no: 2, source_timezone: "Europe/Berlin", folio_identity_strategy: "UNIQUE_PER_STAY",
    normalization_version: 1, published: false,
    // as edged returns it: already redacted, so a browser test cannot accidentally assert on a real secret
    config: { host: "pms.local", port: 5011, password: "[redacted]" },
  },
  {
    id: "r1", revision_no: 1, source_timezone: "Europe/Berlin", folio_identity_strategy: "UNIQUE_PER_STAY",
    normalization_version: 1, published: true, config: { host: "pms.local", port: 5010 },
  },
];

const HEALTH = {
  pms_interface_id: "i1",
  transport_status: "CONNECTED",
  continuity_status: "CONTINUOUS",
  sync_status: "IN_SYNC",
  in_house_stays: 12,
  pending_events: 4,
  review_events: 1,
  oldest_pending_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
};

async function installBackend(
  page: Page,
  opts: {
    mutations: Mutations;
    interfaces?: any[];
    revisions?: any[];
    health?: any;
    routes?: any[];
    unmapped?: any[];
    conflicts?: any[];
    resolutions?: any[];
    publishStatus?: number;
    secretStatus?: number;
    providers?: any[];
  },
) {
  const json = (status: number, body: unknown) => ({
    status, contentType: "application/json", body: JSON.stringify(body),
  });

  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
  ]);

  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    let body: any;
    try { body = req.postDataJSON(); } catch { /* none */ }
    if (method !== "GET") opts.mutations.push({ method, path, body });

    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/pms-providers") {
      return opts.providers
        ? route.fulfill(json(200, { providers: opts.providers }))
        : route.fulfill(json(404, { error: "not_found", message: "page not found" }));
    }
    if (path === "/pms-interfaces" && method === "POST") {
      return route.fulfill(json(201, { id: "new1", connector_kind: body?.connector_kind, display_label: body?.display_label, lifecycle_state: "AUTH_DISABLED" }));
    }
    if (path.endsWith("/revisions") && method === "POST") {
      return route.fulfill(json(201, { revision_id: "rv-new", revision_no: 1, published: false }));
    }
    if (path === "/auth/logout") return route.fulfill(json(200, {}));

    if (path.endsWith("/publish")) {
      const st = opts.publishStatus ?? 200;
      if (st === 409) {
        return route.fulfill(json(409, {
          error: "revision_conflict",
          message: "another operator published a different revision while this form was open",
          current_revision_id: "r2",
        }));
      }
      return route.fulfill(json(200, { current_revision_id: body?.revision_id, revision_no: 2 }));
    }
    if (path.endsWith("/secret")) {
      const st = opts.secretStatus ?? 200;
      if (st !== 200) {
        return route.fulfill(json(st, {
          error: "encryption_unavailable", message: "credential encryption is not configured on this appliance",
        }));
      }
      return route.fulfill(json(200, { generation_no: 4 }));
    }
    if (path.endsWith("/revisions")) return route.fulfill(json(200, { revisions: opts.revisions ?? REVISIONS }));
    if (path.endsWith("/health")) return route.fulfill(json(200, { health: opts.health ?? HEALTH }));
    if (path === "/pms-interfaces") return route.fulfill(json(200, { interfaces: opts.interfaces ?? [IFACE] }));
    if (path.startsWith("/pms-interfaces/")) {
      return route.fulfill(json(200, { interface: IFACE, guest_networks: opts.routes ?? [] }));
    }
    if (path.startsWith("/pms-routing")) {
      return route.fulfill(json(200, {
        routes: opts.routes ?? [], unmapped_guest_networks: opts.unmapped ?? [],
      }));
    }
    if (path.startsWith("/pms-source-conflicts")) {
      return route.fulfill(json(200, { conflicts: opts.conflicts ?? [] }));
    }
    if (path.startsWith("/pms-resolutions")) {
      return route.fulfill(json(200, { data: opts.resolutions ?? [], meta: { has_more: false } }));
    }
    return route.fulfill(json(200, {}));
  });
}

// Opens the connection's side sheet from its card, then (optionally) one of its tabs.
async function openSheet(page: Page, tab?: string) {
  await page.getByRole("button", { name: "Manage" }).click();
  if (tab) await page.getByRole("tab", { name: tab }).click();
}

test("the connections page shows what is running, how it is doing, and how far behind it is", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");

  await expect(page.getByRole("heading", { name: "PMS connection", exact: true })).toBeVisible();
  await expect(page.getByText("Main PMS")).toBeVisible();
  await openSheet(page);

  // The dimensions are stated SEPARATELY, in operator words, rather than collapsed into one "degraded".
  const sheet = page.getByRole("dialog");
  await expect(sheet.getByText("The checks room sign-in depends on")).toBeVisible();
  await expect(sheet.getByText("Connected", { exact: true }).first()).toBeVisible();
  await expect(sheet.getByText("Receiving updates", { exact: true }).first()).toBeVisible();
  await expect(sheet.getByText("Up to date", { exact: true })).toBeVisible();
  await expect(sheet.getByText("Guests in house").first()).toBeVisible();
  await expect(sheet.getByText("12", { exact: true }).first()).toBeVisible();
  // the backlog's AGE, which is what distinguishes a busy morning from a stuck processor
  await expect(sheet.getByText(/^Oldest waiting message arrived/)).toBeVisible();
});

test("the published revision is the one the interface points at, not the newest", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");
  await openSheet(page, "Configuration");

  // THE LIVE SECTION IS WHAT IS IN FORCE. Version 1 is in use even though version 2 is newer; version 2 is a
  // draft that can be put live, never labelled as in use.
  const sheet = page.getByRole("dialog");
  await expect(sheet.getByRole("heading", { name: "Live configuration" })).toBeVisible();
  await expect(sheet.getByText("Version 1", { exact: true })).toBeVisible();
  await expect(sheet.getByText("In use", { exact: true })).toHaveCount(1);
  await expect(sheet.getByRole("button", { name: "Put version 2 live" })).toBeVisible();
});

test("publishing sends the revision the operator believed was live", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");
  await openSheet(page, "History");

  await page.getByRole("button", { name: "Put version 2 live" }).click();
  const confirm = page.getByRole("dialog", { name: /Put version 2 live/ });
  await confirm.getByLabel("Reason").selectOption("ENDPOINT_CHANGE");
  await confirm.getByLabel("Confirm your password").fill("operator-pw");
  await confirm.getByRole("button", { name: "Put live" }).click();

  await expect.poll(() => mutations.length).toBeGreaterThan(0);
  const m = mutations.find((x) => x.path.endsWith("/publish"))!;
  expect(m.body).toEqual({
    revision_id: "r2", expected_revision_id: "r1", reason_code: "ENDPOINT_CHANGE", password: "operator-pw",
  });
});

test("a concurrent publication is shown as a refusal, not as success", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations, publishStatus: 409 });
  await page.goto("/pms-interfaces");
  await openSheet(page, "History");

  await page.getByRole("button", { name: "Put version 2 live" }).click();
  const confirm = page.getByRole("dialog", { name: /Put version 2 live/ });
  await confirm.getByLabel("Confirm your password").fill("operator-pw");
  await confirm.getByRole("button", { name: "Put live" }).click();

  await expect(page.getByRole("alert").filter({ hasText: /published a different revision/ })).toBeVisible();
  // the form stays open so the operator can reload and decide, rather than closing as if it had worked
  await expect(confirm.getByRole("button", { name: "Put live" })).toBeVisible();
});

// THE SUPPORTED CONNECTOR HAS NO CREDENTIAL, so no credential surface may be presented. The Protel FIAS link
// carries no transport authentication; a Credentials tab exists only for a provider that signs in with a key,
// and nothing on this page ever asks the server for a credential.
test("no credential surface is presented, and no credential is ever fetched", async ({ page }) => {
  const mutations: Mutations = [];
  const gets: string[] = [];
  await installBackend(page, { mutations });
  page.on("request", (r) => {
    if (r.method() === "GET" && r.url().includes("/api/edge/v1")) gets.push(new URL(r.url()).pathname);
  });

  await page.goto("/pms-interfaces");
  await openSheet(page);

  await expect(page.getByRole("tab", { name: /credential/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /replace credential/i })).toHaveCount(0);
  expect((await page.content()).toLowerCase()).not.toContain("never set");
  for (const g of gets) expect(g).not.toMatch(/secret/);
  expect(mutations.filter((m) => m.path.includes("/secret"))).toHaveLength(0);
});

test("adding a Protel connection sends exactly the create and draft requests, and nothing else", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");

  await page.getByRole("button", { name: /add connection/i }).click();
  const wizard = page.getByRole("dialog", { name: /Add a PMS connection/ });
  // Without a provider catalogue only Protel is offered, and it is already chosen.
  await expect(wizard.getByText(/Only Protel is offered/)).toBeVisible();
  await wizard.getByRole("button", { name: "Next" }).click();
  await wizard.getByLabel(/^Name/).fill("Front office");
  await wizard.getByLabel(/PMS address and port/).fill("10.0.0.5:5010");
  await wizard.getByRole("button", { name: "Next" }).click();
  await wizard.getByRole("button", { name: "Create connection" }).click();
  await expect(page.getByRole("button", { name: "Publish and activate" })).toBeVisible();

  expect(mutations.map((m) => `${m.method} ${m.path}`)).toEqual([
    "POST /pms-interfaces",
    "POST /pms-interfaces/new1/revisions",
  ]);
  expect(mutations[0].body).toEqual({ connector_kind: "protel-fias", display_label: "Front office" });
  expect(mutations[1].body).toEqual({
    endpoint: "10.0.0.5:5010", source_timezone: "Africa/Cairo",
    dial_timeout_ms: 5000, read_timeout_ms: 15000, write_timeout_ms: 15000,
    heartbeat_interval_ms: 30000, heartbeat_timeout_ms: 90000, feed_freshness_ms: 120000, complete_sync_ms: 600000,
    read_only: true,
  });
});

test("routing names the guest networks that are mapped to nothing", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    mutations,
    routes: [{
      guest_network_id: "gn1", guest_network_name: "Guest VLAN 10",
      pms_interface_id: "i1", pms_interface_label: "Main PMS", is_default: true, routing_mode: "MAPPED",
    }],
    unmapped: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }],
  });
  await page.goto("/pms-routing");

  await expect(page.getByText("Guest VLAN 10")).toBeVisible();
  await expect(page.getByText("Main PMS")).toBeVisible();
  // the point of the page: an absence is invisible in a list of what exists
  await expect(page.getByText("Conference VLAN 20")).toBeVisible();
  await expect(page.getByText(/will not be recognised/)).toBeVisible();
});

test("source conflicts name both interfaces by their labels", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    mutations,
    conflicts: [{
      id: "c1", interface_a: "i1", interface_a_label: "Main PMS",
      interface_b: "i2", interface_b_label: "Spa PMS", severity: "HIGH", resolution: "UNRESOLVED",
    }],
  });
  await page.goto("/pms-source-conflicts");

  await expect(page.getByText("Main PMS")).toBeVisible();
  await expect(page.getByText("Spa PMS")).toBeVisible();
  await expect(page.getByText("high", { exact: true })).toBeVisible();
});

test("resolution evidence summarises outcomes and names no guest", async ({ page }) => {
  const mutations: Mutations = [];
  const now = new Date().toISOString();
  await installBackend(page, {
    mutations,
    resolutions: [
      { id: "a1", guest_network_id: "gn1", outcome_code: "VERIFIED", resolved: true, resolved_at: now },
      { id: "a2", guest_network_id: "gn1", outcome_code: "AMBIGUOUS_DISCRIMINATOR_REQUIRED", resolved: false, resolved_at: now },
      { id: "a3", guest_network_id: "gn1", outcome_code: "AMBIGUOUS_DISCRIMINATOR_REQUIRED", resolved: false, resolved_at: now },
    ],
  });
  await page.goto("/pms-resolutions");

  await expect(page.getByText("Checks recorded")).toBeVisible();
  await expect(page.getByText("Let online").first()).toBeVisible();
  await expect(page.getByText(/1 of 3 verified|33% of attempts/)).toBeVisible();
  await expect(page.getByText(/ambiguous discriminator required/i).first()).toBeVisible();
  await expect(page.getByText("2", { exact: true }).first()).toBeVisible();
  // the table carries outcomes and networks only — a list naming rooms would be a way to enumerate who is
  // staying at the property, which is exactly what the guest-facing uniform failure exists to prevent
  const table = await page.locator("table").innerHTML();
  for (const forbidden of ["room", "reservation", "stay_id", "folio"]) {
    expect(table.toLowerCase()).not.toContain(forbidden);
  }
});

test("the PMS pages are accessible: one heading, named controls, labelled fields", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, {
    mutations,
    routes: [{ guest_network_id: "gn1", guest_network_name: "Guest VLAN 10", pms_interface_id: "i1",
      pms_interface_label: "Main PMS", is_default: true, routing_mode: "MAPPED" }],
    unmapped: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }],
    conflicts: [{ id: "c1", interface_a: "i1", interface_a_label: "Main PMS", interface_b: "i2",
      interface_b_label: "Spa PMS", severity: "HIGH", resolution: "UNRESOLVED" }],
    resolutions: [{ id: "a1", guest_network_id: "gn1", outcome_code: "VERIFIED", resolved: true,
      resolved_at: new Date().toISOString() }],
  });

  for (const path of ["/pms-interfaces", "/pms-routing", "/pms-source-conflicts", "/pms-resolutions"]) {
    await page.goto(path);
    // exactly one h1: a screen-reader user navigating by heading needs one place the page starts
    await expect(page.locator("h1")).toHaveCount(1);
    // every interactive control has an accessible name — an unnamed button is a button nobody can be told
    // to press
    for (const b of await page.getByRole("button").all()) {
      const name = ((await b.getAttribute("aria-label")) ?? (await b.innerText())).trim();
      expect(name.length, `unnamed button on ${path}`).toBeGreaterThan(0);
    }
  }

  // the forms specifically: every input is reachable by its label, which is what a screen reader announces
  await page.goto("/pms-interfaces");
  await openSheet(page, "History");
  await page.getByRole("button", { name: "Put version 2 live" }).click();
  const confirm = page.getByRole("dialog", { name: /Put version 2 live/ });
  await expect(confirm.getByLabel("Reason")).toBeVisible();
  await expect(confirm.getByLabel("Confirm your password")).toBeVisible();

  // Errors are ANNOUNCED, not merely coloured. Refusing the publication and then finding the message in a
  // live region is the check that matters — a red paragraph with no role is invisible to a screen reader,
  // and this page's whole job is telling an operator that something was refused.
  await page.unrouteAll({ behavior: "ignoreErrors" });
  const mutations2: Mutations = [];
  await installBackend(page, { mutations: mutations2, publishStatus: 409 });
  await page.goto("/pms-interfaces");
  await openSheet(page, "History");
  await page.getByRole("button", { name: "Put version 2 live" }).click();
  const refused = page.getByRole("dialog", { name: /Put version 2 live/ });
  await refused.getByLabel("Confirm your password").fill("pw");
  await refused.getByRole("button", { name: "Put live" }).click();
  await expect(page.getByRole("alert").filter({ hasText: /published a different revision/ })).toBeVisible();
});

test("the connections page and its sheet fit a 390px phone without sideways scrolling", async ({ page }) => {
  const mutations: Mutations = [];
  await page.setViewportSize({ width: 390, height: 844 });
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");
  await expect(page.getByText("Main PMS")).toBeVisible();
  const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(await overflow()).toBeLessThanOrEqual(0);
  await openSheet(page, "Configuration");
  await expect(page.getByRole("heading", { name: "Live configuration" })).toBeVisible();
  expect(await overflow()).toBeLessThanOrEqual(0);
});
