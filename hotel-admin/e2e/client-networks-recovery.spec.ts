import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const PLAN = {
  op: "restore_last_working", summary: "Put every client network back to the configuration the appliance last confirmed working.",
  networks: [
    { id: "n1", name: "Rooms", action: "restore_settings", changes: ["name", "lease times"], active_clients: 0 },
    { id: "n2", name: "Conference centre with a long name", action: "switch_off", active_clients: 12 },
  ],
  review: [
    { network_id: "n1", network: "Rooms", layer: "portal_profile", state: "CHANGED", detail: "The portal design was changed after the last confirmation and is put back." },
    { network_id: "n1", network: "Rooms", layer: "pms_routing", state: "CHANGED", detail: "The PMS routing of this network differs from the last confirmation." },
  ],
  cannot_restore: [], conflicts: [], protected: ["Guest records, sessions, devices and packages.", "Licensing and the signed site assignment."],
  switch_checklist: ["The switch port and the virtual switch port group pass the VLANs the restored networks use."],
};

async function install(page: Page, posts: { path: string; body: unknown }[]) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (req.method() === "POST") posts.push({ path, body: req.postDataJSON() });
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/network/recovery") return route.fulfill(json(200, { last_working: { revision: 7, confirmed_at: "2026-10-25T08:00:00Z", available: true }, factory: { available: true, subnet: "10.20.0.0/20" }, review_open: 1 }));
    if (path === "/network/recovery/review") return route.fulfill(json(200, { data: [{ id: "r1", network_id: "n1", network: "Rooms", layer: "pms_routing", detail: "PMS routing differs from the last confirmation.", room_sign_in_paused: true, created_at: "" }] }));
    if (path === "/network/recovery/preview") return route.fulfill(json(200, PLAN));
    if (path === "/network/recovery/stage") return route.fulfill(json(201, { group_id: "g1" }));
    if (path === "/auth/logout") return route.fulfill(json(200, {}));
    return route.fulfill(json(404, { error: "not_found", message: "page not found" }));
  });
  await installCapabilities(page);
}

const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

for (const vp of [{ width: 1280, height: 800 }, { width: 390, height: 800 }]) {
  test(`recovery previews, then stages (${vp.width}px)`, async ({ page }) => {
    const posts: { path: string; body: unknown }[] = [];
    await page.setViewportSize(vp);
    await install(page, posts);
    await page.goto("/network/recovery");
    await expect(page.getByRole("heading", { name: "Recovery" })).toBeVisible();
    await expect(page.getByText("Room sign-in paused here")).toBeVisible();
    await noHorizontalScroll(page);
    await page.getByRole("button", { name: /Review…/ }).first().click();
    await expect(page.getByText(/12 connected device\(s\) lose access/)).toBeVisible();
    await expect(page.getByText("Check outside the appliance")).toBeVisible();
    await noHorizontalScroll(page);
    await page.getByRole("button", { name: "Stage this recovery" }).click();
    await expect.poll(() => posts.some((p) => p.path === "/network/recovery/stage")).toBe(true);
  });
}
