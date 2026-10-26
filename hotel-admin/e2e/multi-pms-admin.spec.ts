import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

// The multi-PMS, multi-network administrator journeys, in a real browser at desktop and phone width: stays that belong to a PMS connection, removing a connection, which design
// each network shows, what each network actually offers at sign-in, and what a physical port carries. edged is mocked per test.

const A = "11111111-1111-4111-8111-111111111111";
const B = "22222222-2222-4222-8222-222222222222";
const C = "33333333-3333-4333-8333-333333333333";

const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
const conn = (id: string, label: string, over: Record<string, unknown> = {}) => ({
  id, connector_kind: "protel-fias", display_label: label, lifecycle_state: "ACTIVE", published: true, current_revision_id: "r-" + id, current_revision_no: 1,
  revision_count: 1, ...over,
});

type Call = { method: string; path: string; body?: any };

async function install(page: Page, calls: Call[], routes: Record<string, (req: { method: string; query: URLSearchParams; body: any }) => unknown>) {
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^.*\/api\/edge\/v1/, "");
    const body = req.method() === "GET" ? undefined : req.postDataJSON?.();
    if (req.method() !== "GET") calls.push({ method: req.method(), path, body });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    const h = routes[path];
    if (h) {
      const out = h({ method: req.method(), query: url.searchParams, body }) as { __status?: number } | undefined;
      if (out && typeof out === "object" && "__status" in out) return route.fulfill(json(out.__status ?? 200, out));
      return route.fulfill(json(200, out ?? {}));
    }
    return route.fulfill(json(200, {}));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

const stay = (id: string, iface: string, label: string, res: string, room: string) => ({
  id, pms_interface_id: iface, pms_interface_label: label, external_reservation_id: res, room, status: "IN_HOUSE", lifecycle_version: 1,
  posting_allowed: false, occupants: 1,
});

for (const vp of [{ width: 1280, height: 800 }, { width: 390, height: 800 }]) {
  test.describe(`${vp.width}px`, () => {
    test.use({ viewport: vp });

    test("stays: the same room number at two PMS connections is two rooms, and the connection tabs filter and count", async ({ page }) => {
      const both = [stay("s1", A, "Protel Hotel", "RES-A", "101"), stay("s2", B, "Aqua Club", "RES-B", "101")];
      const by = [
        { pms_interface_id: B, label: "Aqua Club", lifecycle_state: "ACTIVE", total: 1, in_house: 1 },
        { pms_interface_id: A, label: "Protel Hotel", lifecycle_state: "ACTIVE", total: 1, in_house: 1 },
      ];
      await install(page, [], {
        "/pms-interfaces": () => ({ interfaces: [conn(A, "Protel Hotel"), conn(B, "Aqua Club")] }),
        "/pms-stays": ({ query }) => {
          const only = query.get("pms_interface_id");
          const data = only ? both.filter((s) => s.pms_interface_id === only) : both;
          return { data, meta: { has_more: false }, page: 1, page_size: 50, summary: { total: data.length, with_internet: 0, devices_online: 0, arriving: 0, vip: 0 }, by_interface: by };
        },
      });
      await page.goto("/stays");
      const table = page.getByRole("table");
      await expect(table.getByText("Aqua Club")).toBeVisible();
      await expect(table.getByText("Protel Hotel")).toBeVisible();
      await expect(table.getByText("101")).toHaveCount(2);
      await page.getByRole("radio", { name: /Aqua Club/ }).click();
      await expect(table.getByText("Protel Hotel")).toHaveCount(0);
      await expect(table.getByText("101")).toHaveCount(1);
      await noHorizontalScroll(page);
    });

    test("removing a PMS connection: a used one is retired with its history kept, a live one is refused with the reason", async ({ page }) => {
      const calls: Call[] = [];
      let removed = false;
      const assess = (id: string) => id === B
        ? { id: B, display_label: "Old Aqua", lifecycle_state: "AUTH_DISABLED", outcome: "RETIRE", blockers: [],
            facts: { stays: 208, in_house_stays: 0, live_entitlements: 0, events: 40, postings: 0, revisions: 2, ever_connected: true, ever_activated: true },
            consequences: ["The connection is retired: it cannot be switched on again, edited or published, and it disappears from the ordinary lists.", "Its history stays exactly as it is: stays, events, entitlements, postings, sign-in records and the audit trail."] }
        : { id: A, display_label: "Protel Hotel", lifecycle_state: "ACTIVE", outcome: "BLOCKED", facts: {}, consequences: [],
            blockers: [{ code: "SWITCH_OFF_FIRST", title: "Switch the connection off first", detail: "It is active. Removing a connection that guests are being resolved against would stop room sign-in without warning." }] };
      await install(page, calls, {
        "/pms-interfaces": () => ({ interfaces: [conn(A, "Protel Hotel"), ...(removed ? [] : [conn(B, "Old Aqua", { lifecycle_state: "AUTH_DISABLED", published: false, current_revision_id: "" })])] }),
        "/pms-routing/readiness": () => ({ generated_at: "2026-10-26T08:00:00Z", overall: "READY", networks: [], interfaces: [], issues: [] }),
        [`/pms-interfaces/${A}/removal`]: () => assess(A),
        [`/pms-interfaces/${B}/removal`]: ({ method }) => { if (method === "POST") { removed = true; return { id: B, outcome: "RETIRE" }; } return assess(B); },
        [`/pms-interfaces/${A}/health`]: () => ({ health: { pms_interface_id: A, transport_status: "CONNECTED", sync_status: "IN_SYNC", in_house_stays: 5 } }),
        [`/pms-interfaces/${B}/health`]: () => ({ health: { pms_interface_id: B, transport_status: "DISCONNECTED", sync_status: "NEVER", in_house_stays: 0 } }),
      });
      await page.goto("/pms-interfaces");
      await page.getByRole("button", { name: /Not configured or retired/ }).click();
      await page.getByRole("button", { name: "Remove Old Aqua" }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText("Retire Old Aqua?")).toBeVisible();
      await expect(dialog.getByText(/Its history stays exactly as it is/)).toBeVisible();
      await dialog.getByLabel("Reason").selectOption("REPLACED_BY_ANOTHER");
      await dialog.getByLabel(/Type the connection's name/).fill("Old Aqua");
      await dialog.getByLabel("Confirm your password").fill("pw");
      await dialog.getByRole("button", { name: "Retire connection" }).click();
      await expect.poll(() => calls.find((c) => c.path === `/pms-interfaces/${B}/removal`)?.body?.outcome).toBe("RETIRE");
      await expect.poll(() => calls.find((c) => c.path === `/pms-interfaces/${B}/removal`)?.body?.reason_code).toBe("REPLACED_BY_ANOTHER");
      await noHorizontalScroll(page);
    });

    test("portal designs: only serving networks are offered, a change is staged and applied after confirmation", async ({ page }) => {
      const calls: Call[] = [];
      const net = (id: string, name: string, state: string, profile: string, extra: Record<string, unknown> = {}) =>
        ({ id, name, service_state: state, network_type: "untagged", parent_interface: "ens192", profile_id: profile, ...extra });
      await install(page, calls, {
        "/portal-branding/profiles": () => ({
          default: { id: "default", name: "Site default", network_ids: ["n1", "n3"], is_default: true },
          profiles: [{ id: "p1", name: "Pool bar", network_ids: ["n2"], is_default: false }],
          networks: [net("n1", "Client Wi-Fi", "serving", "default"), net("n2", "Pool", "serving", "p1", { network_type: "vlan", vlan_id: 60 }), net("n3", "Old guest VLAN", "archived", "default", { network_type: "vlan", vlan_id: 90 })],
        }),
        "/portal-branding": () => ({ template: "classic", brand_color: "#0f6b63" }),
      });
      await page.goto("/portal-branding");
      await expect(page.getByRole("heading", { name: "Portal designs" })).toBeVisible();
      await expect(page.getByLabel("Design shown on Client Wi-Fi")).toBeVisible();
      await expect(page.getByLabel("Design shown on Pool")).toBeVisible();
      await expect(page.getByLabel("Design shown on Old guest VLAN")).toHaveCount(0);
      await expect(page.getByText("Untagged (native)").first()).toBeVisible();
      await expect(page.getByText("VLAN 60")).toBeVisible();
      await page.getByLabel("Design shown on Client Wi-Fi").selectOption("p1");
      await expect(page.getByText("Not applied", { exact: true })).toBeVisible();
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
      await page.getByRole("button", { name: "Apply…" }).click();
      await page.getByRole("dialog").getByRole("button", { name: "Apply 1 change" }).click();
      await expect.poll(() => calls.find((c) => c.path === "/portal-branding/profiles/assignments/n1")?.body?.profile_id).toBe("p1");
      await noHorizontalScroll(page);
    });

    test("sign-in methods: each serving network shows what it actually offers and why a method is off", async ({ page }) => {
      await install(page, [], {
        "/guest-signin-protection": () => ({ max_failed_attempts: 5, observation_window_seconds: 3600, restriction_seconds: 3600, is_default: true,
          limits: { min_failed_attempts: 1, max_failed_attempts: 50, min_observation_window_seconds: 60, max_observation_window_seconds: 86400, min_restriction_seconds: 60, max_restriction_seconds: 86400 } }),
        "/auth-methods": () => ({ voucher: { enabled: true }, guest_account: { enabled: true }, email_account: { enabled: true }, pms: { enabled: true, mode: "room_any", provider: "protel-fias" }, open: { enabled: false }, portal: { primary_method: "pms" } }),
        "/notification-providers": () => ({ data: [{ channel: "email", kind: "smtp", enabled: true }] }),
        "/network/overview": () => ({
          networks: [
            { id: "n1", name: "Client Wi-Fi", network_type: "untagged", parent_interface: "ens192", service_state: "serving", saved_enabled: true, room_sign_in: { policy: "DEDICATED", state: "READY", summary: "", interfaces: ["Protel Hotel"] } },
            { id: "n2", name: "Aqua guests", network_type: "vlan", vlan_id: 60, parent_interface: "ens192", service_state: "serving", saved_enabled: true, room_sign_in: { policy: "NONE", state: "OFF", summary: "", interfaces: [] } },
            { id: "n3", name: "Retired VLAN", network_type: "vlan", vlan_id: 90, parent_interface: "ens192", service_state: "archived", saved_enabled: false },
          ],
        }),
        "/network/sign-in-overrides": () => ({ overrides: { n2: { disabled_methods: ["voucher"], primary_method: "" } } }),
      });
      await page.goto("/sign-in-methods");
      await expect(page.getByRole("heading", { name: "What each client network offers" })).toBeVisible();
      const wifi = page.getByRole("list", { name: "Sign-in methods on Client Wi-Fi" });
      await expect(wifi.getByText("Room sign-in")).toBeVisible();
      await expect(page.getByText("Answered by Protel Hotel.", { exact: false }).first()).toBeAttached();
      const aqua = page.getByRole("list", { name: "Sign-in methods on Aqua guests" });
      await expect(aqua).toContainText("Voucher code");
      await expect(page.locator("li", { hasText: "Voucher code: Switched off for this network." }).last()).toBeVisible();
      await expect(page.locator("li", { hasText: "Room sign-in: This network is set to offer no room sign-in (PMS routing)." }).last()).toBeVisible();
      await expect(page.getByRole("list", { name: "Sign-in methods on Retired VLAN" })).toHaveCount(0);
      await noHorizontalScroll(page);
    });

    test("client networks: a trunk port shows its native network and tagged VLANs side by side, each its own network", async ({ page }) => {
      await install(page, [], {
        "/network/guest-networks": () => ({ data: [
          { id: "n1", name: "Client Wi-Fi", enabled: true, network_type: "untagged", parent_interface: "ens192", bridge_name: "br-g1", gateway_ip: "10.20.0.1", subnet_cidr: "10.20.0.0/20", dhcp_mode: "local", captive_portal_enabled: true, service_state: "serving", pools: [] },
          { id: "n2", name: "Aqua guests", enabled: true, network_type: "vlan", vlan_id: 60, parent_interface: "ens192", bridge_name: "br-g60", gateway_ip: "10.60.0.1", subnet_cidr: "10.60.0.0/24", dhcp_mode: "local", captive_portal_enabled: true, service_state: "serving", pools: [] },
        ] }),
        "/network/ports": () => ({ ports: [{
          name: "ens192", protected: false, mode: "trunk", staged_changes: 0,
          native: { id: "n1", name: "Client Wi-Fi", subnet_cidr: "10.20.0.0/20", enabled: true, active_clients: 12 },
          vlans: [{ id: "n2", name: "Aqua guests", vlan_id: 60, subnet_cidr: "10.60.0.0/24", enabled: true, active_clients: 4 }],
          switched_off: [{ id: "n4", name: "Spare", vlan_id: 70, subnet_cidr: "10.70.0.0/24", enabled: false, active_clients: 0 }],
        }] }),
        "/network/overview": () => ({ networks: [] }),
        "/network/revisions": () => ({ data: [] }),
        "/network/changes": () => ({ groups: [] }),
        "/network/recovery": () => ({ latest: null }),
        "/network/factory": () => ({}),
      });
      await page.goto("/network");
      await expect(page.getByText(/carries the untagged \(native\) network Client Wi-Fi and 1 tagged VLAN side by side/)).toBeVisible();
      const lines = page.getByRole("list", { name: "Networks on ens192" });
      await expect(lines).toContainText("Untagged (native)");
      await expect(lines).toContainText("Tagged VLAN 60");
      await expect(lines).toContainText("Spare");
      await expect(lines).toContainText("Switched off");
      await noHorizontalScroll(page);
    });
  });
}
