import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Guest Portal browser E2E. The portal is served by the Go `portald`; to drive the REAL client JS in a
// real browser without running the Linux binary, we extract the actual success-page template
// (data-plane/cmd/portald/templates.go — no drift) and serve it via page.route, then mock the portald
// commerce bridge (/api/commerce/*). This exercises the exact rendered panel + JS the guest sees. The
// SERVER-SIDE trust boundary (portald injecting the pins, browser unable to substitute Tenant/Site/
// Auth-Context/Device/Guest-Network) is additionally proven by the Go portald bridge tests.

const templatesGo = join(__dirname, "../../data-plane/cmd/portald/templates.go");

function renderSuccess(commerceEnabled: boolean): string {
  const src = readFileSync(templatesGo, "utf8");
  const start = src.indexOf("const successHTML = `") + "const successHTML = `".length;
  const end = src.indexOf("`", start);
  let html = src.slice(start, end);
  html = html.replace(/\{\{\.SessionID\}\}/g, "sess-1");
  // pick the {{else}} branch of the DurationSeconds conditional
  html = html.replace(/\{\{if \.DurationSeconds\}\}[\s\S]*?\{\{else\}\}([\s\S]*?)\{\{end\}\}/, "$1");
  if (commerceEnabled) {
    html = html.replace("{{if .CommerceEnabled}}", "").replace("{{end}}", "");
  } else {
    html = html.replace(/\{\{if \.CommerceEnabled\}\}[\s\S]*?\{\{end\}\}/, "");
  }
  return html;
}

type Bodies = { path: string; body: Record<string, unknown> }[];

async function serve(page: Page, opts: {
  commerceEnabled: boolean;
  packages?: unknown[];
  quote?: { status: number; body: unknown };
  confirm?: { status: number; body: unknown };
  bodies: Bodies;
}) {
  const html = renderSuccess(opts.commerceEnabled);
  await page.route("**/success", (r: Route) => r.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html }));
  await page.route("**/api/commerce/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    let body: Record<string, unknown> = {};
    try { body = req.postDataJSON() ?? {}; } catch { /* GET */ }
    if (req.method() !== "GET") opts.bodies.push({ path, body });
    const json = (s: number, b: unknown) => route.fulfill({ status: s, contentType: "application/json", body: JSON.stringify(b) });
    if (path.endsWith("/packages")) return json(200, { packages: opts.packages ?? [] });
    if (path.endsWith("/quote")) { const q = opts.quote ?? { status: 200, body: { quote_id: "q1", expires_at: "2026-08-01T00:00:00Z", display: { name: "Free WiFi", max_concurrent_devices: 2, end_mode: "MANUAL_END" } } }; return json(q.status, q.body); }
    if (path.endsWith("/confirm")) { const c = opts.confirm ?? { status: 200, body: { purchase_id: "pu1", entitlement_id: "ent1" } }; return json(c.status, c.body); }
    return json(200, {});
  });
}

const eligiblePkg = { package_id: "pk1", display: { name: "Free WiFi", down_kbps: 10000, up_kbps: 2000, data_quota_bytes: 0, time_quota_seconds: 3600, max_concurrent_devices: 2, end_mode: "MANUAL_END", free: true } };

test("Portal flag OFF: no commerce panel and no commerce API call", async ({ page }) => {
  const bodies: Bodies = [];
  const calls: string[] = [];
  await serve(page, { commerceEnabled: false, bodies });
  page.on("request", (r) => { if (r.url().includes("/api/commerce/")) calls.push(r.url()); });
  await page.goto("http://127.0.0.1:9099/success");
  await expect(page.locator("#commerce")).toHaveCount(0);
  await page.waitForTimeout(300);
  expect(calls).toHaveLength(0);
});

test("Portal flag ON: lists only eligible packages and shows grant details", async ({ page }) => {
  const bodies: Bodies = [];
  await serve(page, { commerceEnabled: true, packages: [eligiblePkg], bodies });
  await page.goto("http://127.0.0.1:9099/success");
  await expect(page.locator("#commerce")).toHaveCount(1);
  await expect(page.getByText("Free WiFi")).toBeVisible();
  await expect(page.getByText(/Devices: 2/)).toBeVisible();
  // an ineligible package the server did not return is simply not present
  await expect(page.getByText("Premium")).toHaveCount(0);
});

test("Portal flag ON: select -> quote -> confirm reaches the active state; browser sends only opaque ids", async ({ page }) => {
  const bodies: Bodies = [];
  await serve(page, { commerceEnabled: true, packages: [eligiblePkg], bodies });
  await page.goto("http://127.0.0.1:9099/success");
  await page.getByRole("button", { name: "Select" }).click();
  await expect(page.getByRole("button", { name: "Confirm" })).toBeVisible();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(page.getByText(/Package active/i)).toBeVisible();
  // the quote request carried ONLY an opaque package_id; the confirm ONLY an opaque quote_id
  const q = bodies.find((b) => b.path.endsWith("/quote"))!;
  expect(Object.keys(q.body)).toEqual(["package_id"]);
  const c = bodies.find((b) => b.path.endsWith("/confirm"))!;
  expect(Object.keys(c.body)).toEqual(["quote_id"]);
  // no browser-controlled trust fields were ever submitted
  for (const forbidden of ["tenant_id", "site_id", "auth_context_id", "device_id", "guest_network_id"]) {
    expect(JSON.stringify(bodies)).not.toContain(forbidden);
  }
});

test("Portal flag ON: double-submit confirm produces exactly one confirm request", async ({ page }) => {
  const bodies: Bodies = [];
  // slow confirm so a second click can be attempted while the first is in flight
  await serve(page, { commerceEnabled: true, packages: [eligiblePkg], bodies,
    confirm: { status: 200, body: { purchase_id: "pu1", entitlement_id: "ent1" } } });
  await page.route("**/api/commerce/confirm", async (route) => {
    await new Promise((r) => setTimeout(r, 400));
    bodies.push({ path: "/api/commerce/confirm", body: (() => { try { return route.request().postDataJSON(); } catch { return {}; } })() });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ purchase_id: "pu1", entitlement_id: "ent1" }) });
  });
  await page.goto("http://127.0.0.1:9099/success");
  await page.getByRole("button", { name: "Select" }).click();
  const confirm = page.getByRole("button", { name: "Confirm" });
  await confirm.click();
  await confirm.click({ force: true }).catch(() => {});   // second click while disabled/in-flight
  await expect(page.getByText(/Package active/i)).toBeVisible();
  await page.waitForTimeout(200);
  expect(bodies.filter((b) => b.path.endsWith("/confirm"))).toHaveLength(1);
});

test("Portal flag ON: an expired/conflict quote shows a generic unavailable message, no active state", async ({ page }) => {
  const bodies: Bodies = [];
  await serve(page, { commerceEnabled: true, packages: [eligiblePkg], bodies,
    confirm: { status: 409, body: { error: "unavailable" } } });
  await page.goto("http://127.0.0.1:9099/success");
  await page.getByRole("button", { name: "Select" }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(page.locator("#cx-note")).toContainText(/unavailable|expired/i);
  await expect(page.getByText(/Package active/i)).toHaveCount(0);
});

test("Portal flag ON: a failed quote (no session) shows generic unavailable", async ({ page }) => {
  const bodies: Bodies = [];
  await serve(page, { commerceEnabled: true, packages: [eligiblePkg], bodies,
    quote: { status: 503, body: { error: "unavailable" } } });
  await page.goto("http://127.0.0.1:9099/success");
  await page.getByRole("button", { name: "Select" }).click();
  await expect(page.locator("#cx-note")).toContainText(/unavailable/i);
  await expect(page.getByRole("button", { name: "Confirm" })).toHaveCount(0);
});
