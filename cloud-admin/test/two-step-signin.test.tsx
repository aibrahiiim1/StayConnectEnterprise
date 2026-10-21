import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

// TWO-STEP SIGN-IN, AS CENTRAL'S CONSOLE PRESENTS IT (migration 0052, GDPR plan C10).
//   - the password opens nothing: the console asks for the authenticator code, and only that signs in;
//   - the first time, the app is set up (QR code and the key to type) and the ten recovery codes are shown once;
//   - an administrator resets a colleague's two-step sign-in with a reason; never their own row.

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

import LoginPage from "@/app/login/page";
import { UsersManager } from "@/components/users-manager";

beforeEach(() => nav.reset("/login"));

async function passwordStep(user: ReturnType<typeof userEvent.setup>) {
  render(<LoginPage />);
  await user.type(await screen.findByLabelText(/^Email/), "admin@example.test");
  await user.type(screen.getByLabelText(/^Password/), "a-good-password");
  await user.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("signing in to Central", () => {
  it("first sign-in: sets up the app, shows the recovery codes once, then opens the console", async () => {
    const user = userEvent.setup();
    const codes = Array.from({ length: 10 }, (_, i) => `AAAA${i}-BBBB${i}`);
    const { calls } = mockFetch([
      { method: "POST", match: "/api/v1/auth/login", body: { second_step: "enrol" } },
      { method: "POST", match: "/api/v1/auth/login/enrol", body: { secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/x", account: "a" } },
      { method: "POST", match: "/api/v1/auth/login/code", body: { ...PLATFORM_ME, recovery_codes: codes } },
    ]);
    await passwordStep(user);
    expect(await screen.findByAltText(/QR code/)).toBeInTheDocument();
    expect(screen.getByText("JBSW Y3DP EHPK 3PXP JBSW")).toBeInTheDocument();
    await user.type(screen.getByLabelText(/Code from the app/), "123456");
    await user.click(screen.getByRole("button", { name: "Confirm and sign in" }));
    const list = await screen.findByRole("list", { name: "Recovery codes" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(10);
    expect(nav.router.replace).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "I have saved them" }));
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/overview"));
    expect(calls.find((c) => c.url === "/api/v1/auth/login/code")?.body).toEqual({ code: "123456" });
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
    expect(screen.getByText("Sets up two-step at next sign-in")).toBeInTheDocument();
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
