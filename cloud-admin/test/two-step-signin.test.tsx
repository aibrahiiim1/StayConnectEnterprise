import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

// Two-step sign-in is optional per account (D68): the password alone signs in until it is turned on, then the code
// is needed; recovery codes appear once and admins cannot reset their own second factor.

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

import LoginPage from "@/app/login/page";
import SecurityPage from "@/app/(app)/account/security/page";
import { MfaRecommendation } from "@/components/mfa-recommendation";
import { UsersManager } from "@/components/users-manager";

beforeEach(() => nav.reset("/login"));

async function passwordStep(user: ReturnType<typeof userEvent.setup>) {
  render(<LoginPage />);
  await user.type(await screen.findByLabelText(/^Email/), "admin@example.test");
  await user.type(screen.getByLabelText(/^Password/), "a-good-password");
  await user.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("signing in to Central", () => {
  it("an account without two-step sign-in is signed in by the password alone", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { method: "POST", match: "/api/v1/auth/login", body: { second_step: "none", ...PLATFORM_ME } },
    ]);
    await passwordStep(user);
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/overview"));
    expect(calls.map((c) => c.url)).toEqual(["/api/v1/auth/login"]);
    expect(screen.queryByLabelText(/^Code/)).toBeNull();
  });

  it("an account with two-step sign-in on needs the code before the console opens", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { method: "POST", match: "/api/v1/auth/login", body: { second_step: "code" } },
      { method: "POST", match: "/api/v1/auth/login/code", body: PLATFORM_ME },
    ]);
    await passwordStep(user);
    await user.type(await screen.findByLabelText(/^Code/), "123456");
    expect(nav.router.replace).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/overview"));
    expect(calls.find((c) => c.url === "/api/v1/auth/login/code")?.body).toEqual({ code: "123456" });
    expect(calls.some((c) => c.url.includes("/enrol"))).toBe(false);
  });

  it("a wrong code says so and stays on the code step", async () => {
    const user = userEvent.setup();
    mockFetch([
      { method: "POST", match: "/api/v1/auth/login", body: { second_step: "code" } },
      { method: "POST", match: "/api/v1/auth/login/code", status: 401, body: { error: "wrong_code", message: "That code is not right." } },
    ]);
    await passwordStep(user);
    await user.type(await screen.findByLabelText(/^Code/), "000000");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText(/That code is not right/)).toBeInTheDocument();
    expect(nav.router.replace).not.toHaveBeenCalled();
  });
});

describe("resetting a colleague's two-step sign-in", () => {
  it("is offered for a colleague with an app, needs a reason, and is never offered for yourself", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { method: "GET", match: "/api/cloud/v1/team", body: { items: [
        { id: "op-1", email: "admin@example.test", status: "active", roles: ["platform_admin"], two_step_enrolled: true },
        { id: "op-2", email: "colleague@example.test", status: "active", roles: ["platform_support"], two_step_enrolled: true },
        { id: "op-3", email: "new@example.test", status: "active", roles: ["platform_support"], two_step_enrolled: false },
      ] } },
      { method: "POST", match: "/api/cloud/v1/team/op-2/mfa-reset", body: { removed: true } },
    ]);
    renderAs(PLATFORM_ME, <UsersManager base="/cloud/v1/team" roles={["platform_support"]} canManage meId="op-1" canResetPassword noun="member" />);
    await screen.findAllByText("colleague@example.test");
    expect(screen.getByText("Two-step off")).toBeInTheDocument();
    expect(screen.getAllByText("Two-step on").length).toBeGreaterThanOrEqual(2);
    const buttons = screen.getAllByRole("button", { name: /Reset two-step/ });
    expect(buttons).toHaveLength(1);
    await user.click(buttons[0]);
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Reset two-step sign-in" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/^Reason/), "lost their phone");
    await user.click(submit);
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ reason: "lost their phone" }));
  });
});

describe("your two-step sign-in", () => {
  it("turning it on: the password step-up, the QR code and first code, then the recovery codes once", async () => {
    const user = userEvent.setup();
    let enrolled = false;
    const codes = Array.from({ length: 10 }, (_, i) => `AAAA${i}-BBBB${i}`);
    const { calls } = mockFetch([
      { method: "GET", match: "/api/v1/auth/mfa", body: () => ({ body: { enrolled, recovery_codes_left: enrolled ? 10 : 0 } }) },
      { method: "POST", match: "/api/v1/auth/reauth", body: { ok: true } },
      { method: "POST", match: "/api/v1/auth/mfa/setup", body: { secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/x" } },
      { method: "POST", match: "/api/v1/auth/mfa/confirm", body: () => { enrolled = true; return { body: { enrolled: true, recovery_codes: codes } }; } },
    ]);
    render(<SecurityPage />);
    expect(await screen.findByText("Off")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Turn on two-step sign-in" }));
    await user.type(screen.getByLabelText(/^Your password/), "my-password");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByAltText(/QR code/)).toBeInTheDocument();
    expect(screen.getByText("JBSW Y3DP EHPK 3PXP JBSW")).toBeInTheDocument();
    expect(calls.find((c) => c.url === "/api/v1/auth/reauth")?.body).toEqual({ password: "my-password" });
    await user.type(screen.getByLabelText(/Code from the app/), "123456");
    await user.click(screen.getByRole("button", { name: "Turn on" }));
    const list = await screen.findByRole("list", { name: "Recovery codes" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(10);
    expect(calls.find((c) => c.url === "/api/v1/auth/mfa/confirm")?.body).toEqual({ code: "123456" });
    await user.click(screen.getByRole("button", { name: "I have saved them" }));
    expect(await screen.findByText("On")).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Recovery codes" })).toBeNull();
  });

  it("a wrong password stops before anything is set up", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { method: "GET", match: "/api/v1/auth/mfa", body: { enrolled: false, recovery_codes_left: 0 } },
      { method: "POST", match: "/api/v1/auth/reauth", status: 401, body: { error: "unauthenticated", message: "reauthentication failed" } },
    ]);
    render(<SecurityPage />);
    await user.click(await screen.findByRole("button", { name: "Turn on two-step sign-in" }));
    await user.type(screen.getByLabelText(/^Your password/), "wrong");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/password was not accepted/)).toBeInTheDocument();
    expect(calls.some((c) => c.url === "/api/v1/auth/mfa/setup")).toBe(false);
  });

  it("turning it off needs the password and a code", async () => {
    const user = userEvent.setup();
    let enrolled = true;
    const { calls } = mockFetch([
      { method: "GET", match: "/api/v1/auth/mfa", body: () => ({ body: { enrolled, recovery_codes_left: 8, confirmed_at: "2026-10-01T00:00:00Z" } }) },
      { method: "POST", match: "/api/v1/auth/reauth", body: { ok: true } },
      { method: "POST", match: "/api/v1/auth/mfa/disable", body: (n: number) => {
        if (n === 0) return { status: 400, body: { error: "wrong_code", message: "That code is not right." } };
        enrolled = false;
        return { body: { enrolled: false } };
      } },
    ]);
    render(<SecurityPage />);
    expect(await screen.findByText("On")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Turn off" }));
    await user.type(screen.getByLabelText(/^Your password/), "my-password");
    const submit = screen.getByRole("button", { name: "Turn off two-step sign-in" });
    expect(submit).toBeDisabled();
    await user.type(screen.getByLabelText(/^Code(?! from)/), "000000");
    await user.click(submit);
    expect(await screen.findByText(/That code is not right/)).toBeInTheDocument();
    expect(screen.queryByText("Off")).toBeNull();
    await user.type(screen.getByLabelText(/^Code(?! from)/), "ABCDE-FGHJK");
    await user.click(screen.getByRole("button", { name: "Turn off two-step sign-in" }));
    expect(await screen.findByText("Off")).toBeInTheDocument();
    const disables = calls.filter((c) => c.url === "/api/v1/auth/mfa/disable");
    expect(disables.at(-1)?.body).toEqual({ code: "ABCDE-FGHJK" });
    expect(calls.filter((c) => c.url === "/api/v1/auth/reauth")).toHaveLength(2);
  });
});

describe("the sidebar suggestion", () => {
  beforeEach(() => { try { window.localStorage.clear(); } catch { /* ignore */ } });

  it("suggests two-step sign-in to an account without it, and stays dismissed", async () => {
    const user = userEvent.setup();
    mockFetch([{ method: "GET", match: "/api/v1/auth/mfa", body: { enrolled: false, recovery_codes_left: 0 } }]);
    const { unmount } = render(<MfaRecommendation email="admin@example.test" />);
    const link = await screen.findByRole("link", { name: /turn on two-step sign-in/ });
    expect(link).toHaveAttribute("href", "/account/security");
    await user.click(screen.getByRole("button", { name: /Dismiss/ }));
    expect(screen.queryByRole("link", { name: /turn on two-step sign-in/ })).toBeNull();
    unmount();
    render(<MfaRecommendation email="admin@example.test" />);
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("link", { name: /turn on two-step sign-in/ })).toBeNull();
  });

  it("is never shown to an account that has it on", async () => {
    const { calls } = mockFetch([{ method: "GET", match: "/api/v1/auth/mfa", body: { enrolled: true, recovery_codes_left: 10 } }]);
    render(<MfaRecommendation email="admin@example.test" />);
    await waitFor(() => expect(calls.length).toBe(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("link", { name: /two-step/ })).toBeNull();
  });
});
