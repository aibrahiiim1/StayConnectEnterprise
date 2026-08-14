import { test, expect } from "@playwright/test";

// THE AUTHENTICATION GATE, in a real browser.
//
// Every operator page on this appliance is behind one Edge middleware function, and the whole of its job is
// four lines: no session cookie means /login, a session on /login means /dashboard, and the asset paths are
// exempt so the login page can render itself.
//
// It had no browser coverage, which is how it came to be the least-tested code with the widest blast
// radius. The middleware runtime is also the part of Next most likely to behave differently across a major
// upgrade -- the comment in middleware.ts records a previous 500 caused by exactly that -- so these tests
// exist to make a framework upgrade prove the gate still closes, rather than leaving it to be discovered on
// the appliance.
//
// No backend is contacted: the middleware sees only the PRESENCE of the cookie, and every assertion here is
// about a redirect the Next server performs on its own.

const APP_ROUTES = ["/dashboard", "/pms-interfaces", "/financial-recovery", "/operators", "/network/system"];

test.describe("the authentication gate", () => {
  test("sends an operator with no session to the login page, remembering where they were going", async ({
    page,
  }) => {
    await page.context().clearCookies();
    for (const route of APP_ROUTES) {
      await page.goto(route);
      // The HOST of the redirect is the appliance's internal bind address, which Caddy rewrites for the
      // browser; asserting on it here would be asserting about the test harness. What matters is that the
      // operator lands on the login page and that where they were going survives the round trip.
      const url = new URL(page.url());
      expect(url.pathname).toBe("/login");
      expect(url.searchParams.get("next")).toBe(route);
      // ...and the login page actually renders, rather than the redirect landing on a 500
      await expect(page.getByText("Hotel Admin sign-in")).toBeVisible();
      await expect(page.getByLabel("Email or username")).toBeVisible();
      await expect(page.getByLabel("Password")).toBeVisible();
    }
  });

  test("does not leave an unauthenticated operator on a page that renders anything about the site", async ({
    page,
  }) => {
    await page.context().clearCookies();
    const resp = await page.goto("/financial-review");
    expect(resp?.status()).toBe(200); // the LOGIN page, having been redirected there
    await expect(page.getByText("Hotel Admin sign-in")).toBeVisible();
    // nothing from the financial surface leaked into the response the browser actually got
    await expect(page.getByRole("heading", { name: /manual review/i })).toHaveCount(0);
  });

  test("sends an operator who already has a session away from the login page", async ({ page }) => {
    // Both hosts: the middleware redirects to the server's own bind host, so a cookie pinned to one of them
    // would be dropped halfway through the redirect and the test would be measuring cookie scope.
    await page.context().addCookies([
      { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
      { name: "sc_edge_session", value: "e2e-test", url: "http://localhost:3123" },
    ]);
    await page.goto("/login");
    expect(new URL(page.url()).pathname).toBe("/dashboard");
  });

  test("leaves the favicon alone, so the login page can render itself", async ({ page }) => {
    await page.context().clearCookies();
    const resp = await page.request.get("/icon.svg");
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toContain("svg");
  });

  test("redirects with an absolute Location, which is what the framework requires of middleware", async ({
    page,
  }) => {
    await page.context().clearCookies();
    // A relative Location threw ERR_INVALID_URL inside Next's middleware normalisation once before. Following
    // the redirect end to end is the assertion: if the header were relative again, this navigation would 500.
    const resp = await page.goto("/dashboard");
    expect(resp?.status()).toBe(200);
    const url = new URL(page.url());
    expect(url.protocol).toBe("http:");
    expect(url.host).toMatch(/^(127\.0\.0\.1|localhost):3123$/);
    expect(url.pathname).toBe("/login");
  });
});
