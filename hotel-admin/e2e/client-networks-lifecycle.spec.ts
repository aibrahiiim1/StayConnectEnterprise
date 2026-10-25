import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const FACTORY = {
  state: "prepared", reason: "The prepared Access network has not been activated.",
  template: {
    name: "Access", mode: "Access (untagged)", subnet_cidr: "10.20.0.0/20", gateway_ip: "10.20.0.1", dhcp: "Local DHCP",
    pool: { start_ip: "10.20.0.10", end_ip: "10.20.15.254" }, services: ["DNS", "Captive portal", "NAT", "Internet"], pms: "Not connected",
  },
  lan_interface: "ens192", conflicts: [], checklist: ["The LAN port is cabled to the access switch.", "The switch port is untagged in one VLAN."],
};
const PLAN = {
  mode: "trunk", port: "ens192", conflicts: [], switch_off: [],
  create: [{ name: "VLAN 60", vlan_id: 60, subnet_cidr: "10.60.0.0/24", gateway_ip: "10.60.0.1", pools: [] }],
  impact: ["Connected clients stay on the Access network."],
  switch_requirements: ["The switch port is a trunk allowing VLAN 60.", "The hypervisor port group passes VLAN 60."],
};

async function install(page: Page, calls: { path: string; body: unknown }[]) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (req.method() === "POST") calls.push({ path, body: req.postDataJSON() });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/network/guest-networks") return route.fulfill(json(200, { data: [], meta: {} }));
    if (path === "/network/revisions") return route.fulfill(json(200, { data: [], meta: {} }));
    if (path === "/network/factory") return route.fulfill(json(200, FACTORY));
    if (path === "/network/factory/activate") return route.fulfill(json(200, {}));
    if (path === "/network/changes") return route.fulfill(json(200, { groups: [] }));
    if (path === "/network/ports") {
      return route.fulfill(json(200, { ports: [
        { name: "ens192", protected: false, mode: "access", vlans: [], staged_changes: 0,
          native: { id: "a", name: "Access", subnet_cidr: "10.20.0.0/20", enabled: true, active_clients: 3 } },
      ] }));
    }
    if (path === "/network/ports/ens192/mode") return route.fulfill(json(200, req.postDataJSON().preview ? PLAN : {}));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    return route.fulfill(json(404, { error: "not_found", message: "page not found" }));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

for (const vp of [{ width: 1280, height: 800 }, { width: 390, height: 800 }]) {
  test(`factory activation needs the acknowledgement (${vp.width}px)`, async ({ page }) => {
    const calls: { path: string; body: unknown }[] = [];
    await page.setViewportSize(vp);
    await install(page, calls);
    await page.goto("/network");
    await expect(page.getByText("10.20.0.0/20 (gateway 10.20.0.1)")).toBeVisible();
    await noHorizontalScroll(page);
    await page.getByRole("button", { name: /Activate…/ }).click();
    const stage = page.getByRole("button", { name: "Stage activation" });
    await expect(stage).toBeDisabled();
    await noHorizontalScroll(page);
    await page.getByLabel(/No other DHCP server/).check();
    await stage.click();
    await expect.poll(() => calls.some((c) => c.path === "/network/factory/activate")).toBe(true);
  });

  test(`trunk conversion previews before it stages (${vp.width}px)`, async ({ page }) => {
    const calls: { path: string; body: unknown }[] = [];
    await page.setViewportSize(vp);
    await install(page, calls);
    await page.goto("/network");
    await page.getByRole("button", { name: /Convert to Trunk…/ }).click();
    await page.getByLabel("VLAN id 1").fill("60");
    await page.getByRole("button", { name: "Preview" }).click();
    await expect(page.getByText("Check outside the appliance")).toBeVisible();
    await noHorizontalScroll(page);
    await page.getByRole("button", { name: /Stage this change/ }).click();
    await expect.poll(() => calls.filter((c) => c.path === "/network/ports/ens192/mode").length).toBe(2);
  });
}
