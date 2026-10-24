import { test, expect } from "@playwright/test";

// Middleware gates operator pages using cookie presence; browser redirects must survive Next upgrades.

const APP_ROUTES = ["/dashboard", "/pms-interfaces", "/financial-recovery", "/operators", "/network/system"];

test.describe("the authentication gate", () => {
  test("sends an operator with no session to the login page, remembering where they were going", async ({
    page,
  }) => {
    await page.context().clearCookies();
    for (const route of APP_ROUTES) {
      await page.goto(route);
      // Caddy rewrites the internal redirect host; assert destination path and preserved return URL.
      const url = new URL(page.url());
      expect(url.pathname).toBe("/login");
      expect(url.searchParams.get("next")).toBe(route);
      await expect(page.getByRole("heading", { name: "OneGate Admin Console" })).toBeVisible();
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
    await expect(page.getByRole("heading", { name: "OneGate Admin Console" })).toBeVisible();
    await expect(page.getByRole("heading", { name: /manual review/i })).toHaveCount(0);
  });

  test("sends an operator who already has a session away from the login page", async ({ page, baseURL }) => {
    // Cover both hosts so cookie scope does not confound redirect behavior.
    const port = new URL(baseURL!).port;
    await page.context().addCookies([
      { name: "sc_edge_session", value: "e2e-test", url: `http://127.0.0.1:${port}` },
      { name: "sc_edge_session", value: "e2e-test", url: `http://localhost:${port}` },
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
    page, baseURL,
  }) => {
    await page.context().clearCookies();
    // Follow redirects because a relative Location can fail Next's URL normalization.
    const resp = await page.goto("/dashboard");
    expect(resp?.status()).toBe(200);
    const url = new URL(page.url());
    expect(url.protocol).toBe("http:");
    expect(url.host).toMatch(new RegExp(`^(127\\.0\\.0\\.1|localhost):${new URL(baseURL!).port}$`));
    expect(url.pathname).toBe("/login");
  });
});
