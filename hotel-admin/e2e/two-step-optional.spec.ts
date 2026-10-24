import { test, expect, type Page, type Route } from "@playwright/test";
import { installCapabilities } from "./capabilities";

// Two-step sign-in is optional per account (D68). edged is mocked: a small in-memory account whose authenticator
// can be turned on and off, so the real login, account page and sidebar run against the real request shapes.

type Account = { enrolled: boolean; signedIn: boolean; calls: string[] };

const json = (status: number, body: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(body) });

async function installBackend(page: Page, baseURL: string, acct: Account) {
  const port = new URL(baseURL).port;
  const signIn = () => page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: `http://127.0.0.1:${port}` },
    { name: "sc_edge_session", value: "e2e-test", url: `http://localhost:${port}` },
  ]);
  if (acct.signedIn) await signIn();
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/api\/edge\/v1/, "");
    const body = req.postDataJSON?.() ?? null;
    acct.calls.push(`${req.method()} ${path}`);
    const me = { operator_id: "op-1", email: "ops@hotel.test", roles: ["site_admin"], must_change_password: false };
    switch (path) {
      case "/auth/login":
        if (body?.password !== "correct-password") return route.fulfill(json(401, { error: "unauthenticated", message: "invalid credentials" }));
        if (!acct.enrolled) {
          await signIn();
          return route.fulfill(json(200, { second_step: "none", ...me }));
        }
        return route.fulfill(json(200, { second_step: "code" }));
      case "/auth/login/code":
        if (body?.code !== "123456") return route.fulfill(json(401, { error: "wrong_code", message: "That code is not right." }));
        await signIn();
        return route.fulfill(json(200, me));
      case "/auth/whoami":
        return route.fulfill(json(200, me));
      case "/auth/mfa":
        return route.fulfill(json(200, { enrolled: acct.enrolled, recovery_codes_left: acct.enrolled ? 10 : 0,
          confirmed_at: acct.enrolled ? "2026-10-24T08:00:00Z" : undefined }));
      case "/auth/mfa/setup":
        if (body?.password !== "correct-password") return route.fulfill(json(401, { error: "reauth_required", message: "Your password is not correct." }));
        return route.fulfill(json(200, { secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/OneGate:ops?secret=JBSWY3DPEHPK3PXPJBSW", account: "ops@hotel.test" }));
      case "/auth/mfa/confirm":
        if (body?.code !== "123456") return route.fulfill(json(401, { error: "wrong_code", message: "That code is not right." }));
        acct.enrolled = true;
        return route.fulfill(json(200, { enrolled: true, recovery_codes: Array.from({ length: 10 }, (_, i) => `ABCDE-FGH${i}J`) }));
      case "/auth/mfa/disable":
        if (body?.password !== "correct-password") return route.fulfill(json(401, { error: "reauth_required", message: "Your password is not correct." }));
        if (body?.code !== "123456") return route.fulfill(json(401, { error: "wrong_code", message: "That code is not right." }));
        acct.enrolled = false;
        return route.fulfill(json(200, { enrolled: false }));
    }
    return route.fulfill(json(200, { data: [], meta: { has_more: false } }));
  });
  await installCapabilities(page);
}

async function signInWithPassword(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Email or username").fill("ops@hotel.test");
  await page.getByLabel("Password").fill("correct-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("an account without two-step sign-in is signed in by its password and offered a quiet suggestion", async ({ page, baseURL }) => {
  const acct: Account = { enrolled: false, signedIn: false, calls: [] };
  await installBackend(page, baseURL!, acct);
  await signInWithPassword(page);
  await page.waitForURL("**/dashboard");
  expect(acct.calls).not.toContain("POST /auth/login/code");
  const hint = page.locator("aside").first().getByRole("link", { name: /turn on two-step sign-in/ });
  await expect(hint).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("an account with two-step sign-in on needs the code", async ({ page, baseURL }) => {
  const acct: Account = { enrolled: true, signedIn: false, calls: [] };
  await installBackend(page, baseURL!, acct);
  await signInWithPassword(page);
  const code = page.getByLabel("Code");
  await expect(code).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/login");
  await code.fill("000000");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText("That code is not right.")).toBeVisible();
  await page.getByLabel("Code").fill("123456");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.waitForURL("**/dashboard");
  await expect(page.locator("aside").first().getByRole("link", { name: "Two-step sign-in" })).toBeVisible();
  await expect(page.getByRole("link", { name: /turn on two-step sign-in/ })).toHaveCount(0);
});

test("turning two-step sign-in on from the account page, then off again", async ({ page, baseURL }) => {
  const acct: Account = { enrolled: false, signedIn: true, calls: [] };
  await installBackend(page, baseURL!, acct);
  await page.goto("/account/security");
  const main = page.locator("main");
  await expect(main.getByText("Off", { exact: true })).toBeVisible();

  await main.getByRole("button", { name: "Turn on two-step sign-in" }).click();
  await main.getByLabel("Your password").fill("wrong-password");
  await main.getByRole("button", { name: "Continue" }).click();
  await expect(main.getByText("Your password was not accepted.")).toBeVisible();
  await main.getByLabel("Your password").fill("correct-password");
  await main.getByRole("button", { name: "Continue" }).click();
  await expect(main.getByAltText("QR code to scan with your authenticator app")).toBeVisible();
  await expect(main.getByText("JBSW Y3DP EHPK 3PXP JBSW")).toBeVisible();
  await main.getByLabel("Code from the app").fill("123456");
  await main.getByRole("button", { name: "Turn on", exact: true }).click();
  const codes = main.getByRole("list", { name: "Recovery codes" });
  await expect(codes.getByRole("listitem")).toHaveCount(10);
  await expect(main.getByRole("button", { name: "Download" })).toBeVisible();
  await main.getByRole("button", { name: "I have saved them" }).click();
  await expect(main.getByText("On", { exact: true })).toBeVisible();
  await expect(codes).toHaveCount(0);
  // The suggestion goes as soon as it is on.
  await expect(page.getByRole("link", { name: /turn on two-step sign-in/ })).toHaveCount(0);

  await main.getByRole("button", { name: "Turn off" }).click();
  await main.getByLabel("Your password").fill("correct-password");
  await main.getByLabel("Code").fill("000000");
  await main.getByRole("button", { name: "Turn off two-step sign-in" }).click();
  await expect(main.getByText("That code is not right.")).toBeVisible();
  await expect(main.getByText("On", { exact: true })).toBeVisible();
  await main.getByLabel("Code").fill("123456");
  await main.getByRole("button", { name: "Turn off two-step sign-in" }).click();
  await expect(main.getByText("Off", { exact: true })).toBeVisible();
  await expect(main.getByRole("status").filter({ hasText: "Two-step sign-in is off" })).toBeVisible();
  expect(acct.calls.filter((c) => c === "POST /auth/mfa/disable")).toHaveLength(2);
});
