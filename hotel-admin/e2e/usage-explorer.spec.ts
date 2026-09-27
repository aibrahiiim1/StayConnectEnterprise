import { test, expect, type Page, type Route } from "@playwright/test";

// USAGE EXPLORER, BY ACCESS SOURCE, IN A BROWSER.
//
// edged is mocked at the network layer. No appliance, no database, no client data. What this asserts is what a
// component test cannot: the real page loads under Next, the new tab lists every source type, the type
// filter reaches edged, a stay deep link still lands on the stay, and nothing a voucher card would need to be
// used appears on the page.

const ACCOUNT = "aaaaaaaa-1111-4111-8111-111111111111";
const VOUCHER = "3fa85f64-5717-4562-b3fc-2c963f66afa6";
const STAY = "cccccccc-3333-4333-8333-333333333333";
const T = (down: number, up: number) => ({ bytes_down: down, bytes_up: up, bytes_total: down + up, sessions: 1, devices: 1 });

const ROWS = [
  { source_type: "account", source_id: ACCOUNT, account_username: "alex.morgan", totals: T(900_000_000, 10_000_000) },
  { source_type: "voucher", source_id: VOUCHER, totals: T(500_000_000, 5_000_000), end_reason: "DATA" },
  { source_type: "stay", source_id: STAY, room: "4202", pms_interface: "Main PMS", reservation: "BK-88213",
    stay_status: "IN_HOUSE", arrival: "2026-09-18T12:00:00Z", totals: T(100_000_000, 1_000_000) },
];
const DEVICES = [{ mac: "02:00:00:aa:bb:cc", bytes_down: 1000, bytes_up: 1000, bytes_total: 2000, sessions: 1 }];
const SESSIONS = [{ session_id: "s1", mac: "02:00:00:aa:bb:cc", state: "CLOSED", started: "2026-09-20T10:00:00Z",
  ended: "2026-09-20T12:00:00Z", bytes_down: 1000, bytes_up: 1000, bytes_total: 2000 }];

async function installBackend(page: Page, requested: string[], baseURL = "http://127.0.0.1:3123") {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: baseURL }]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const u = new URL(route.request().url());
    const path = u.pathname.replace(/^.*\/api\/edge\/v1/, "");
    requested.push(path + u.search);
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/usage/sources") {
      const type = u.searchParams.get("type");
      return route.fulfill(json(200, { data: ROWS.filter((r) => !type || r.source_type === type), meta: { has_more: false } }));
    }
    const m = path.match(/^\/usage\/sources\/(account|voucher|stay)\/(.+)$/);
    if (m) {
      const row = ROWS.find((r) => r.source_type === m[1])!;
      return route.fulfill(json(200, { source: row, service_plan: "Standard", access_status: m[1] === "stay" ? undefined : "ACTIVE", devices: DEVICES, sessions: SESSIONS }));
    }
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
}

test("usage explorer lists access sources and filters them by type", async ({ page, baseURL }) => {
  const requested: string[] = [];
  await installBackend(page, requested, baseURL);
  await page.goto("/usage");

  await expect(page.getByRole("tab", { name: /by access source/i })).toBeVisible();
  await expect(page.getByRole("tab", { name: /by room or stay/i })).toHaveCount(0);
  await expect(page.getByText("alex.morgan")).toBeVisible();
  await expect(page.getByText("Card 3fa85f64…")).toBeVisible();
  await expect(page.getByText("Room 4202")).toBeVisible();

  await page.getByRole("radio", { name: "Client account" }).click();
  await expect(page.getByText("Room 4202")).toHaveCount(0);
  expect(requested.some((r) => r.startsWith("/usage/sources?type=account"))).toBe(true);

  await page.getByRole("button", { name: "Details for alex.morgan" }).click();
  await expect(page.getByText("Devices used under this access")).toBeVisible();
  const body = (await page.locator("main").innerText()).toLowerCase();
  expect(body).not.toContain("reservation");
  expect(body).not.toContain("room ");
});

test("the /usage?stay=<id> deep link still opens the stay", async ({ page, baseURL }) => {
  const requested: string[] = [];
  await installBackend(page, requested, baseURL);
  await page.goto(`/usage?stay=${STAY}`);
  await expect(page.getByText("Devices used during this stay")).toBeVisible();
  await expect(page.getByText(/Main PMS · reservation BK-88213/)).toBeVisible();
  expect(requested).toContain(`/usage/sources/stay/${STAY}`);
});
