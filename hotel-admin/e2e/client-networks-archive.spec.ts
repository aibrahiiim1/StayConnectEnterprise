import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const net = (id: string, name: string, extra: Record<string, unknown>) => ({
  id, name, enabled: false, network_type: "untagged", parent_interface: "ens192", bridge_name: `br-${id}`, gateway_ip: "10.20.0.1", subnet_cidr: "10.20.0.0/20",
  dhcp_mode: "local", dns_mode: "appliance", domain_name: "guest.local", lease_default_seconds: 3600, lease_min_seconds: 900, lease_max_seconds: 7200,
  captive_portal_enabled: true, internet_access_enabled: true, nat_enabled: true, client_isolation_enabled: true,
  pools: [{ start_ip: "10.20.0.10", end_ip: "10.20.15.254" }], archived: false, is_default: false, ...extra,
});

const DEFAULT = net("d1", "Client Wi-Fi", { enabled: true, is_default: true });
const OLD1 = net("o1", "VLAN 60", { network_type: "vlan", vlan_id: 60, subnet_cidr: "10.60.0.0/24", gateway_ip: "10.60.0.1", archived: true });
const OLD2 = net("o2", "PRELIVE Test Guest with a rather long name to test wrapping", { network_type: "vlan", vlan_id: 90, subnet_cidr: "192.168.77.0/24", archived: true });

const ov = (n: ReturnType<typeof net>) => ({
  id: n.id, name: n.name, network_type: n.network_type, parent_interface: n.parent_interface, bridge_name: n.bridge_name, subnet_cidr: n.subnet_cidr, gateway_ip: n.gateway_ip,
  dhcp_mode: "local", saved_enabled: n.enabled, running: n.enabled, ever_activated: true, status: n.enabled ? "active" : "disabled",
  reason: n.enabled ? "Confirmed and running." : "Switched off.", changes: [], active_clients: 0, pms_interfaces: [], health_known: true, attention: [], archived: n.archived, is_default: n.is_default,
});

async function install(page: Page, posts: { path: string }[]) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (req.method() === "POST") posts.push({ path });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/network/guest-networks") return route.fulfill(json(200, { data: [DEFAULT, OLD1, OLD2], meta: {} }));
    if (path === "/network/revisions") return route.fulfill(json(200, { data: [], meta: {} }));
    if (path === "/network/overview") {
      return route.fulfill(json(200, { active_revision: { id: "r1", seq: 1 }, in_flight_revision: null, changes_blocked: false, diagnostics_available: false,
        networks: [ov(DEFAULT), ov(OLD1), ov(OLD2)] }));
    }
    if (path === "/network/archive") {
      return route.fulfill(json(200, { count: 2, data: [
        { id: "o1", name: OLD1.name, archived_at: "2026-10-26T08:00:00Z", retired_by: "Full client networking reset", has_history: false, has_pms_routing: false },
        { id: "o2", name: OLD2.name, archived_at: "2026-10-26T08:00:00Z", retired_by: "Full client networking reset", has_history: true, has_pms_routing: true }] }));
    }
    if (path === "/network/default") {
      return route.fulfill(json(200, { network_id: "d1", name: "Client Wi-Fi", present: true,
        items: [{ id: "dhcp", label: "DHCP", state: "ok", detail: "Local DHCP." }, { id: "pms", label: "Room sign-in", state: "unresolved", detail: "Room sign-in is not offered on this network until you choose how it works." }],
        pms_candidates: [], packages_pinned_to_other_networks: [],
        room_sign_in: { network_id: "d1", name: "Client Wi-Fi", enabled: true, is_default: true, policy: "", state: "UNDECIDED", summary: "undecided", interfaces: [], reasons: [] } }));
    }
    if (path === "/pms-interfaces") {
      return route.fulfill(json(200, { interfaces: [{ id: "p1", display_label: "Protel", connector_kind: "protel-fias", lifecycle_state: "ACTIVE", published: true }] }));
    }
    if (path === "/pms-routing") {
      return route.fulfill(json(200, { routes: [], unmapped_guest_networks: [], policies: [{ network_id: "d1", name: "Client Wi-Fi", enabled: true, archived: false, is_default: true, mode: "", fingerprint: "fp-d1", interfaces: [] }] }));
    }
    if (path === "/pms-routing/d1/preview") {
      return route.fulfill(json(200, {
        proposal_id: "pr-d1", network: { network_id: "d1", name: "Client Wi-Fi", mode: "", interfaces: [] },
        proposed: { mode: "DEDICATED", interfaces: [{ pms_interface_id: "p1", label: "Protel", lifecycle_state: "ACTIVE", published: true, in_house_stays: 3, room_auth_ready: true, health_known: true, also_used_by: ["PRELIVE Test Guest"], live_sessions_on_this_network: 0 }] },
        added: [], removed: [], blockers: [], warnings: [{ code: "INTERFACE_ALSO_USED", text: "Protel is also used by PRELIVE Test Guest." }],
        impact: { active_sessions: 4, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
        guarantees: ["No PMS traffic is started or stopped, and nothing is posted to any PMS."] }));
    }
    if (path.endsWith("/status")) return route.fulfill(json(200, { id: "x", bridge_name: "br", enabled: true, active_clients: 0 }));
    if (path === "/network/factory") return route.fulfill(json(200, { state: "activated", reason: "", template: {}, conflicts: [], checklist: [] }));
    if (path === "/network/changes") return route.fulfill(json(200, { groups: [] }));
    if (path === "/network/ports") return route.fulfill(json(200, { ports: [] }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    return route.fulfill(json(404, { error: "not_found", message: "page not found" }));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

for (const vp of [{ width: 1280, height: 800 }, { width: 390, height: 800 }]) {
  test(`the main list shows the Default Network and the archive is collapsed behind its count (${vp.width}px)`, async ({ page }) => {
    const posts: { path: string }[] = [];
    await page.setViewportSize(vp);
    await install(page, posts);
    await page.goto("/network");
    await expect(page.getByRole("heading", { name: "Client networks" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Client Wi-Fi", exact: true })).toBeVisible();
    await expect(page.getByText("Default", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "VLAN 60", exact: true })).toHaveCount(0);

    const toggle = page.getByRole("button", { name: "Archived networks (2)" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByText("Switched off by a confirmed reset")).toHaveCount(0);
    await noHorizontalScroll(page);
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByText("Switched off by a confirmed reset")).toBeVisible();
    await expect(page.getByRole("link", { name: "VLAN 60", exact: true })).toBeVisible();
    await noHorizontalScroll(page);

    await page.getByRole("button", { name: "Restore from archive" }).first().click();
    await expect.poll(() => posts.some((p) => p.path === "/network/guest-networks/o1/unarchive")).toBe(true);
  });

  test(`room sign-in is chosen for the Default Network, previewed, and applied only when that preview is confirmed (${vp.width}px)`, async ({ page }) => {
    const puts: unknown[] = [];
    await page.setViewportSize(vp);
    await install(page, []);
    await page.route("**/api/edge/v1/pms-routing/d1", async (route) => {
      if (route.request().method() === "PUT") puts.push(route.request().postDataJSON());
      await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    });
    await page.goto("/network");
    await expect(page.getByText("Room sign-in is not offered on this network until you choose how it works.")).toBeVisible();
    expect(puts).toHaveLength(0);
    await page.getByRole("button", { name: /Configure room sign-in…/ }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("radio", { name: /One dedicated PMS/ }).check();
    await dialog.getByRole("radio", { name: /Protel/ }).check();
    await noHorizontalScroll(page);
    await dialog.getByRole("button", { name: "Preview the change" }).click();
    await expect(dialog.getByText("Protel is also used by PRELIVE Test Guest.")).toBeVisible();
    await expect(dialog.getByText(/nothing is posted to any PMS/)).toBeVisible();
    await noHorizontalScroll(page);
    expect(puts).toHaveLength(0);
    await dialog.getByRole("button", { name: "Confirm and apply" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0]).toEqual({ mode: "DEDICATED", pms_interface_ids: ["p1"], proposal_id: "pr-d1" });
  });
}
