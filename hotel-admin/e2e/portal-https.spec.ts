import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

const ONEGATE = "abcd1234.gw.onegate.getsemantics.com";

function doc(domain: string | null) {
  return {
    settings: { portal_ip: "10.255.255.1", custom_domain: domain, updated_at: "2026-10-12T10:00:00Z", updated_by: "migration 0116", configured: true },
    state: {
      central_available: true, onegate_hostname: ONEGATE, dns_state: "SYNCED", custom_domain: domain ?? "",
      custom_domain_status: domain ? "PENDING" : "",
      custom_domain_records: domain ? [
        { type: "TXT", name: `_onegate-verify.${domain}`, value: "verify-token-1" },
        { type: "CNAME", name: domain, value: `${ONEGATE}.` },
        { type: "CNAME", name: `_acme-challenge.${domain}`, value: "q1w2e3r4t5y6u7i8.acme.onegate.getsemantics.com." },
      ] : [],
      custom_domain_error: "", canonical_hostname: "", https_active: false, fallback_reason: "NOT_CONFIGURED",
      fallback_since: "2026-10-12T09:00:00Z", last_sync_at: null, last_sync_error: "", updated_at: null,
    },
    certificates: [],
    callback_urls: [{ hostname: ONEGATE, kind: "ONEGATE", url: `https://${ONEGATE}/auth/social/callback`, canonical: false }],
    history: [],
    health: { name: "portal_https", level: "warn", reason: "NOT_CONFIGURED", message: "not set up" },
  };
}

async function installBackend(page: Page, sent: { method: string; path: string; body: unknown }[]) {
  const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.context().addCookies([{ name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" }]);
  let domain: string | null = null;
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    if (path === "/auth/whoami") return route.fulfill(json(200, { email: "admin@test.local", roles: ["site_admin"] }));
    if (path === "/portal-https" && req.method() === "GET") return route.fulfill(json(200, doc(domain)));
    if (path === "/portal-https/custom-domain" && req.method() === "PUT") {
      const body = req.postDataJSON();
      sent.push({ method: "PUT", path, body });
      domain = body.fqdn;
      return route.fulfill(json(200, { changed: true, sync_requested: true, portal: doc(domain) }));
    }
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await installCapabilities(page);
}

test("Portal HTTPS: the fallback in words, then a hotel domain and its DNS records", async ({ page }) => {
  const sent: { method: string; path: string; body: unknown }[] = [];
  await installBackend(page, sent);
  await page.goto("/portal-https");

  await expect(page.getByRole("link", { name: "Portal HTTPS" })).toBeVisible();
  await expect(page.getByText("The Client Portal is on the HTTP fallback")).toBeVisible();
  await expect(page.getByText(/OneGate Central has not been set up/)).toBeVisible();

  await page.getByLabel("Domain").fill("wifi.nilehotel.com");
  await page.getByRole("button", { name: /Use this domain/ }).click();
  const dlg = page.getByRole("dialog");
  await dlg.getByLabel(/Confirm your password/).fill("pw");
  await dlg.getByRole("button", { name: "Use this domain" }).click();

  await expect(page.getByText("verify-token-1")).toBeVisible();
  await expect(page.getByText("q1w2e3r4t5y6u7i8.acme.onegate.getsemantics.com.")).toBeVisible();
  expect(sent).toEqual([{ method: "PUT", path: "/portal-https/custom-domain", body: { fqdn: "wifi.nilehotel.com", password: "pw" } }]);
});
