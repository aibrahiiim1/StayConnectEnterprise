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

test("the interface page shows what is running, how it is doing, and how far behind it is", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");

  await expect(page.getByRole("heading", { name: "PMS interfaces" })).toBeVisible();
  await expect(page.getByText("Main PMS")).toBeVisible();
  await page.getByRole("button", { name: "Open" }).click();

  // three dimensions, stated separately because they fail separately
  await expect(page.getByText("connected")).toBeVisible();
  await expect(page.getByText("continuous")).toBeVisible();
  await expect(page.getByText("in sync")).toBeVisible();
  await expect(page.getByText(/12 stays in house/)).toBeVisible();
  // the backlog's AGE, which is what distinguishes a busy morning from a stuck processor
  await expect(page.getByText(/oldest waiting since/)).toBeVisible();
});

test("the published revision is the one the interface points at, not the newest", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();

  // scoped to the badge, because the list's "Published revision" column header contains the word too
  const badge = page.getByText("published", { exact: true });
  await expect(badge).toBeVisible();
  await expect(badge.locator("xpath=ancestor::tr[1]")).toContainText("#1");
  // and the newer one is the one offering a Publish action
  await expect(page.getByRole("button", { name: "Publish", exact: true })).toBeVisible();
});

test("publishing sends the revision the operator believed was live", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations });
  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();

  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await page.getByLabel("Reason").fill("CONFIG_UPDATE");
  await page.getByLabel("Confirm your password").fill("operator-pw");
  await page.getByRole("button", { name: "Publish revision" }).click();

  await expect.poll(() => mutations.length).toBeGreaterThan(0);
  const m = mutations.find((x) => x.path.endsWith("/publish"))!;
  expect(m.body.revision_id).toBe("r2");
  expect(m.body.expected_revision_id).toBe("r1");
  expect(m.body.reason_code).toBe("CONFIG_UPDATE");
  expect(m.body.password).toBe("operator-pw");
});

test("a concurrent publication is shown as a refusal, not as success", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations, publishStatus: 409 });
  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();

  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await page.getByLabel("Reason").fill("CONFIG_UPDATE");
  await page.getByLabel("Confirm your password").fill("operator-pw");
  await page.getByRole("button", { name: "Publish revision" }).click();

  await expect(page.getByRole("alert").filter({ hasText: /published a different revision/ })).toBeVisible();
  // the form stays open so the operator can reload and decide, rather than closing as if it had worked
  await expect(page.getByRole("button", { name: "Publish revision" })).toBeVisible();
});

test("the credential is write-only: nothing on the page displays one, and nothing fetches one", async ({ page }) => {
  const mutations: Mutations = [];
  const gets: string[] = [];
  await installBackend(page, { mutations });
  page.on("request", (r) => {
    if (r.method() === "GET" && r.url().includes("/api/edge/v1")) gets.push(new URL(r.url()).pathname);
  });

  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();
  await expect(page.getByRole("heading", { name: "Credential" })).toBeVisible();
  await expect(page.getByText(/Currently using generation 3/)).toBeVisible();

  await page.getByRole("button", { name: "Replace credential" }).click();
  const field = page.getByLabel("New credential");
  await expect(field).toHaveAttribute("type", "password");
  await field.fill("s3cr3t-value");
  await page.getByLabel("Reason").fill("ROTATION");
  await page.getByLabel("Confirm your password").fill("operator-pw");
  await page.getByRole("button", { name: "Replace credential" }).click();

  // the confirmation names the generation, never the value
  await expect(page.getByRole("status")).toContainText("generation 4");
  expect(await page.content()).not.toContain("s3cr3t-value");
  // and no request anywhere on this page asks the server for a credential
  for (const g of gets) expect(g).not.toMatch(/secret/);
});

test("a refused rotation says so and stores nothing", async ({ page }) => {
  const mutations: Mutations = [];
  await installBackend(page, { mutations, secretStatus: 503 });
  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();
  await page.getByRole("button", { name: "Replace credential" }).click();
  await page.getByLabel("New credential").fill("anything");
  await page.getByLabel("Reason").fill("ROTATION");
  await page.getByLabel("Confirm your password").fill("operator-pw");
  await page.getByRole("button", { name: "Replace credential" }).click();

  await expect(page.getByRole("alert").filter({ hasText: /encryption is not configured/ })).toBeVisible();
  // and no success confirmation is shown alongside the refusal
  await expect(page.getByRole("status")).toHaveCount(0);
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
  await expect(page.getByText(/resolved against no/)).toBeVisible();
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
  await expect(page.getByText("high")).toBeVisible();
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

  await expect(page.getByText("1 of 3 verified")).toBeVisible();
  await expect(page.getByText(/ambiguous discriminator required · 2/)).toBeVisible();
  // the table carries outcomes and networks only — a list naming rooms would be a way to enumerate who is
  // staying at the property, which is exactly what the guest-facing uniform failure exists to prevent
  const table = await page.locator("table").innerHTML();
  for (const forbidden of ["room", "reservation", "stay_id", "folio"]) {
    expect(table.toLowerCase()).not.toContain(forbidden);
  }
});

test("the new phase-3 pages are accessible: one heading, named controls, labelled fields", async ({ page }) => {
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
  await page.getByRole("button", { name: "Open" }).click();
  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await expect(page.getByLabel("Reason")).toBeVisible();
  await expect(page.getByLabel("Confirm your password")).toBeVisible();

  // Errors are ANNOUNCED, not merely coloured. Refusing the publication and then finding the message in a
  // live region is the check that matters — a red paragraph with no role is invisible to a screen reader,
  // and this page's whole job is telling an operator that something was refused.
  await page.unrouteAll({ behavior: "ignoreErrors" });
  const mutations2: Mutations = [];
  await installBackend(page, { mutations: mutations2, publishStatus: 409 });
  await page.goto("/pms-interfaces");
  await page.getByRole("button", { name: "Open" }).click();
  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await page.getByLabel("Reason").fill("CONFIG_UPDATE");
  await page.getByLabel("Confirm your password").fill("pw");
  await page.getByRole("button", { name: "Publish revision" }).click();
  await expect(page.getByRole("alert").filter({ hasText: /published a different revision/ })).toBeVisible();
});
