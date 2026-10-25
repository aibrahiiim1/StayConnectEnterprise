import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const net = (id: string, name: string, vlan: number, enabled = true) => ({
  id, name, enabled, network_type: "vlan", parent_interface: "ens192", vlan_id: vlan, bridge_name: `br-g${vlan}`,
  gateway_ip: `10.${vlan}.0.1`, subnet_cidr: `10.${vlan}.0.0/24`, dhcp_mode: "local", dns_mode: "appliance", domain_name: "guest.local",
  lease_default_seconds: 3600, lease_min_seconds: 900, lease_max_seconds: 7200, captive_portal_enabled: true, internet_access_enabled: true,
  nat_enabled: true, client_isolation_enabled: false, pools: [{ start_ip: `10.${vlan}.0.100`, end_ip: `10.${vlan}.0.200` }],
});

const overviewNet = (n: ReturnType<typeof net>, status: string, reason: string, extra: Record<string, unknown> = {}) => ({
  id: n.id, name: n.name, network_type: n.network_type, vlan_id: n.vlan_id, parent_interface: n.parent_interface, bridge_name: n.bridge_name,
  subnet_cidr: n.subnet_cidr, gateway_ip: n.gateway_ip, dhcp_mode: "local", saved_enabled: n.enabled, running: status === "active",
  ever_activated: status !== "not_activated", status, reason, changes: [], active_clients: 4, pms_interfaces: [], health_known: true, attention: [], ...extra,
});

const N90 = net("n90", "PRELIVE Test Guest", 90);
const N60 = net("n60", "VLAN 60", 60);
const N70 = net("n70", "Conference", 70, false);

const DIAG = {
  network: {
    network_id: "n60", name: "VLAN 60",
    checks: [
      { id: "port_link", label: "Port link", state: "ok", detail: "ens192 has link (up)", where: "appliance" },
      { id: "frames", label: "Frames delivered to the interface", state: "fail", detail: "0 frames received and 18 sent on ens192.60 since it was created", where: "appliance" },
      { id: "dhcp_bound", label: "DHCP server bound to the gateway", state: "ok", detail: "Kea holds a DHCP socket for 10.60.0.1", where: "appliance" },
    ],
    findings: ["No frames were delivered to ens192.60 by this appliance's network stack. That does not show where they were lost."],
    outside: [
      "The switch port facing this appliance allows VLAN 60 (trunk allowed list).",
      "The virtual switch or hypervisor port group passes VLAN 60 to this appliance.",
      "The access point or controller maps the Wi-Fi network to VLAN 60.",
    ],
  },
  server: [{ id: "kea_file", label: "DHCP configuration saved for a restart", state: "ok", detail: "the file Kea starts from holds the confirmed configuration", where: "appliance" }],
};

async function install(page: Page) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/network/guest-networks") return route.fulfill(json(200, { data: [N90, N60, N70], meta: {} }));
    if (path === "/network/revisions") return route.fulfill(json(200, { data: [], meta: {} }));
    if (path === "/network/overview") {
      return route.fulfill(json(200, {
        active_revision: { id: "r97", seq: 97 }, in_flight_revision: null, changes_blocked: true,
        blocked_detail: "The appliance is not known to be on its confirmed configuration since revision r96 (kea not restored).",
        diagnostics_available: true,
        networks: [
          overviewNet(N90, "active", "Confirmed and running."),
          overviewNet(N60, "change_pending", "Saved settings differ from what is running (gateway_ip).",
            { changes: [{ field: "gateway_ip", running: "10.60.0.1", saved: "10.60.0.254" }], running: true }),
          overviewNet(N70, "not_activated", "Prepared but never activated.", { active_clients: 0, ever_activated: false }),
        ],
      }));
    }
    const status = path.match(/^\/network\/guest-networks\/([^/]+)\/status$/);
    if (status) return route.fulfill(json(200, { id: status[1], bridge_name: "br", enabled: true, active_clients: 4 }));
    if (path === "/network/guest-networks/n60/diagnostics") return route.fulfill(json(200, DIAG));
    if (path === "/network/guest-networks/n60") return route.fulfill(json(200, N60));
    if (path === "/network/dhcp/reservations") return route.fulfill(json(200, { data: [], meta: {} }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    return route.fulfill(json(404, { error: "not_found", message: "page not found" }));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

test("the list tells what each network is, not what was saved", async ({ page }) => {
  await install(page);
  await page.goto("/network");
  await expect(page.getByRole("heading", { name: "Client networks" })).toBeVisible();
  await expect(page.getByText("Active", { exact: true })).toBeVisible();
  await expect(page.getByText("Change not applied")).toBeVisible();
  await expect(page.getByText("Not applied: Gateway address")).toBeVisible();
  await expect(page.getByText("Not activated")).toBeVisible();
  await expect(page.getByText("Changes are being refused")).toBeVisible();
  await noHorizontalScroll(page);
});

test("the list fits a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await install(page);
  await page.goto("/network");
  await expect(page.getByText("Change not applied")).toBeVisible();
  await noHorizontalScroll(page);
});

test("diagnostics report what was observed and list what must be checked outside the appliance", async ({ page }) => {
  await install(page);
  await page.goto("/network/n60");
  await page.getByRole("button", { name: /Run checks/ }).click();
  await expect(page.getByText(/does not show where they were lost/)).toBeVisible();
  await expect(page.getByText("Check outside the appliance")).toBeVisible();
  await expect(page.getByText(/switch port facing this appliance allows VLAN 60/)).toBeVisible();
  await expect(page.getByText("Problem").first()).toBeVisible();
  await noHorizontalScroll(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await noHorizontalScroll(page);
});
