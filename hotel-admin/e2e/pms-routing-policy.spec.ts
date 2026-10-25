import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

// Room sign-in is chosen per client network: none, one dedicated PMS, or a strict shared set of up to three. These specs drive the real pages in a browser at desktop
// and phone width; edged is mocked per test.

const iface = (id: string, label: string) => ({ id, display_label: label, connector_kind: "protel-fias", lifecycle_state: "ACTIVE", published: true });
const policy = (id: string, name: string, mode: string, labels: string[] = [], extra: Record<string, unknown> = {}) => ({
  network_id: id, name, enabled: true, archived: false, is_default: false, mode, fingerprint: `fp-${id}`,
  interfaces: labels.map((l, i) => ({ pms_interface_id: `i${i + 1}`, label: l, lifecycle_state: "ACTIVE", published: true })), ...extra,
});

const NETWORKS = [
  policy("n1", "Rooms", "DEDICATED", ["Protel A"]),
  policy("n2", "Resort Wi-Fi", "SHARED_STRICT", ["Protel A", "Protel B"]),
  policy("n3", "Vouchers only", "NONE"),
  policy("n4", "Conference", ""),
];

const ASSESSMENT = {
  generated_at: "2026-10-26T08:00:00Z", overall: "PARTIAL", issues: [], interfaces: [],
  networks: [
    { network_id: "n1", name: "Rooms", state: "READY", policy: "DEDICATED", summary: "Room sign-in checks Protel A and it can answer.", interfaces: [], reasons: [] },
    { network_id: "n2", name: "Resort Wi-Fi", state: "BLOCKED", policy: "SHARED_STRICT", summary: "Guests on this network cannot sign in with their room number. This network checks every selected PMS, and one of them cannot answer, so none can be trusted to say a guest is absent.",
      interfaces: [{ pms_interface_id: "i1", label: "Protel A" }, { pms_interface_id: "i2", label: "Protel B" }], reasons: [{ code: "FEED_SILENT", text: "Protel B: the PMS has stopped responding", pms_interface_id: "i2" }] },
    { network_id: "n3", name: "Vouchers only", state: "OFF", policy: "NONE", summary: "Room sign-in is not offered on this network, by choice. Guests use the other sign-in methods.", interfaces: [], reasons: [] },
    { network_id: "n4", name: "Conference", state: "UNDECIDED", policy: "", summary: "No one has decided how room sign-in works on this network, so it is not offered.", interfaces: [], reasons: [] },
  ],
};

const PREVIEW = (blockers: unknown[] = []) => ({
  network: NETWORKS[3],
  proposed: { mode: "SHARED_STRICT", interfaces: [
    { pms_interface_id: "i1", label: "Protel A", lifecycle_state: "ACTIVE", published: true, in_house_stays: 120, room_auth_ready: true, health_known: true, also_used_by: ["Rooms"], live_sessions_on_this_network: 0 },
    { pms_interface_id: "i2", label: "Protel B", lifecycle_state: "ACTIVE", published: true, in_house_stays: 80, room_auth_ready: false, health_known: true, also_used_by: [], live_sessions_on_this_network: 0 },
  ] },
  added: [], removed: [], blockers,
  warnings: [{ code: "ROOM_NUMBERS_OVERLAP", text: "4 room number(s) are in house in more than one selected PMS. A guest in one of them is asked for a second detail (their reservation number)." }],
  impact: { active_sessions: 7, live_room_entitlements: 2, room_numbers_in_more_than_one_selected_pms: 4, sign_in_methods_switched_off: [], network_serves_clients: true },
  guarantees: ["No PMS traffic is started or stopped, and nothing is posted to any PMS.", "Every selected PMS keeps its own rooms and guests; they are never merged, and a guest is never asked which PMS they belong to."],
});

async function install(page: Page, calls: { method: string; path: string; body?: unknown }[], over: { roles?: string[]; blockers?: unknown[]; latest?: unknown } = {}) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (req.method() !== "GET") calls.push({ method: req.method(), path, body: req.postDataJSON?.() });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: over.roles ?? ["site_admin"] }));
    if (path === "/pms-interfaces") return route.fulfill(json(200, { interfaces: [iface("i1", "Protel A"), iface("i2", "Protel B"), iface("i3", "Protel C"), iface("i4", "Protel D")] }));
    if (path === "/pms-routing") return route.fulfill(json(200, { routes: [], policies: NETWORKS, unmapped_guest_networks: [], limits: { max_shared_interfaces: 3 } }));
    if (path === "/pms-routing/readiness") return route.fulfill(json(200, ASSESSMENT));
    if (path.endsWith("/preview")) return route.fulfill(json(200, PREVIEW(over.blockers)));
    if (path.startsWith("/pms-routing/") && req.method() === "PUT") return route.fulfill(json(200, NETWORKS[3]));
    if (path === "/network/recovery") {
      return route.fulfill(json(200, { last_working: { revision: 98, confirmed_at: "2026-10-25T14:59:06Z", available: true }, factory: { available: true, subnet: "10.20.0.0/20" }, review_open: 0,
        latest: over.latest ?? null }));
    }
    if (path === "/network/changes") return route.fulfill(json(200, { groups: [] }));
    if (path === "/network/recovery/review") return route.fulfill(json(200, { data: [] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    return route.fulfill(json(404, { error: "not_found", message: "page not found" }));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

for (const vp of [{ width: 1280, height: 800 }, { width: 390, height: 800 }]) {
  test.describe(`${vp.width}px`, () => {
    test.use({ viewport: vp });

    test("PMS routing shows each network's choice and status, and a PMS-free network is not a fault", async ({ page }) => {
      await install(page, []);
      await page.goto("/pms-routing");
      await expect(page.getByRole("heading", { name: "PMS routing" })).toBeVisible();
      await expect(page.getByText("Rooms", { exact: true })).toBeVisible();
      await expect(page.getByText("One dedicated PMS")).toBeVisible();
      await expect(page.getByText("Shared strict set")).toBeVisible();
      await expect(page.getByText("Not offered", { exact: true })).toBeVisible();
      await expect(page.getByText("Needs a decision", { exact: true })).toBeVisible();
      // the blocked shared network is named with its reason, once, in the callout
      await expect(page.getByText(/Room sign-in is not working on some client networks/)).toBeVisible();
      await expect(page.getByText(/Protel B: the PMS has stopped responding|the PMS has stopped responding/).first()).toBeVisible();
      await noHorizontalScroll(page);
    });

    test("a shared strict set is limited to three, previewed, and a blocker stops the confirmation", async ({ page }) => {
      const calls: { method: string; path: string; body?: unknown }[] = [];
      await install(page, calls, { blockers: [{ code: "DUPLICATE_SOURCE", text: "Protel A and Protel B may be the same property's PMS." }] });
      await page.goto("/pms-routing");
      await page.getByRole("button", { name: /Decide/ }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("radio", { name: /A shared strict set/ }).check();
      for (const l of ["Protel A", "Protel B", "Protel C"]) await dialog.getByRole("checkbox", { name: new RegExp(l) }).check();
      await expect(dialog.getByRole("checkbox", { name: /Protel D/ })).toBeDisabled();
      await noHorizontalScroll(page);
      await dialog.getByRole("button", { name: "Preview the change" }).click();
      await expect(dialog.getByText("This cannot be applied")).toBeVisible();
      await expect(dialog.getByText("4 room number(s) are in house in more than one selected PMS", { exact: false })).toBeVisible();
      await expect(dialog.getByText(/nothing is posted to any PMS/)).toBeVisible();
      await expect(dialog.getByRole("button", { name: "Confirm and apply" })).toBeDisabled();
      await noHorizontalScroll(page);
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
      expect(calls.find((c) => c.path.endsWith("/preview"))?.body).toEqual({ mode: "SHARED_STRICT", pms_interface_ids: ["i1", "i2", "i3"] });
    });

    test("applying sends the preview's fingerprint and the explicit set, never 'every active PMS'", async ({ page }) => {
      const calls: { method: string; path: string; body?: unknown }[] = [];
      await install(page, calls);
      await page.goto("/pms-routing");
      await page.getByRole("button", { name: /Decide/ }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("radio", { name: /A shared strict set/ }).check();
      for (const l of ["Protel A", "Protel B"]) await dialog.getByRole("checkbox", { name: new RegExp(l) }).check();
      await dialog.getByRole("button", { name: "Preview the change" }).click();
      await dialog.getByRole("button", { name: "Confirm and apply" }).click();
      await expect.poll(() => calls.filter((c) => c.method === "PUT").length).toBe(1);
      const put = calls.find((c) => c.method === "PUT")!;
      expect(put.path).toBe("/pms-routing/n4");
      expect(put.body).toEqual({ mode: "SHARED_STRICT", pms_interface_ids: ["i1", "i2"], based_on: "fp-n4" });
    });

    test("after a CONFIRMED reset the optional room sign-in step is offered; it changes nothing until confirmed", async ({ page }) => {
      const calls: { method: string; path: string; body?: unknown }[] = [];
      await install(page, calls, { latest: { group_id: "g1", kind: "FULL_RESET", state: "CONFIRMED", at: "2026-10-25T14:59:06Z",
        room_sign_in_step: { offered: true, network_id: "n4", network_name: "Conference", policy: "", state: "UNDECIDED", summary: "undecided" } } });
      await page.goto("/network/recovery");
      await expect(page.getByText("Room sign-in on the new network (optional)")).toBeVisible();
      await expect(page.getByText(/The reset did not change any PMS routing/)).toBeVisible();
      await noHorizontalScroll(page);
      await page.getByRole("button", { name: /Configure room sign-in…/ }).click();
      await expect(page.getByRole("dialog").getByRole("radio", { name: /No room sign-in/ })).toBeVisible();
      await noHorizontalScroll(page);
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
    });

    test("a role that cannot change routing sees the choices but no controls", async ({ page }) => {
      await install(page, [], { roles: ["front_office_operator"] });
      await page.goto("/pms-routing");
      await expect(page.getByText("Rooms", { exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: /Decide|Change/ })).toHaveCount(0);
    });
  });
}
