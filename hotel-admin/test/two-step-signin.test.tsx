import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// TWO-STEP SIGN-IN, AS THE CONSOLE PRESENTS IT (edged migration 0132, GDPR plan C10).
//
//   - the password opens nothing: the console asks for the authenticator code, and only that signs in;
//   - the first time, the app is set up here (QR code and the key to type) and the ten recovery codes are shown
//     once before the console opens;
//   - a wrong code says so and keeps the operator on the code step; an expired step goes back to the password;
//   - an administrator can reset a colleague's two-step sign-in with a reason and their password, never their own.

const get = vi.fn();
const post = vi.fn();
const replace = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() },
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace, refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/operators",
}));
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

beforeEach(() => { get.mockReset(); post.mockReset(); replace.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

async function passwordStep() {
  const Page = (await import("@/app/login/page")).default;
  render(<Page />);
  await userEvent.type(screen.getByLabelText(/Email or username/), "ops@hotel");
  await userEvent.type(screen.getByLabelText(/^Password/), "a-good-password");
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("signing in", () => {
  it("first sign-in: sets up the app, shows the recovery codes once, then opens the console", async () => {
    const codes = Array.from({ length: 10 }, (_, i) => `AAAA${i}-BBBB${i}`);
    post.mockImplementation((path: string) => {
      if (path === "/auth/login") return Promise.resolve({ second_step: "enrol" });
      if (path === "/auth/login/enrol") return Promise.resolve({ secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/x", account: "ops@hotel" });
      return Promise.resolve({ operator_id: "op", roles: ["site_admin"], recovery_codes: codes });
    });
    await passwordStep();
    expect(await screen.findByAltText(/QR code/)).toBeTruthy();
    expect(screen.getByText("JBSW Y3DP EHPK 3PXP JBSW")).toBeTruthy();
    await userEvent.type(screen.getByLabelText(/Code from the app/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Confirm and sign in" }));
    const list = await screen.findByRole("list", { name: "Recovery codes" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(10);
    expect(replace).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "I have saved them" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/dashboard"));
    expect(post).toHaveBeenCalledWith("/auth/login/code", { code: "123456" });
  });

  it("a wrong code says so and stays on the code step", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockImplementation((path: string) => path === "/auth/login"
      ? Promise.resolve({ second_step: "code" })
      : Promise.reject(new ApiError(401, { error: "wrong_code", message: "That code is not right." })));
    await passwordStep();
    await userEvent.type(await screen.findByLabelText(/^Code/), "000000");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText(/That code is not right/)).toBeTruthy();
    expect(screen.getByLabelText(/^Code/)).toBeTruthy();
    expect(replace).not.toHaveBeenCalled();
  });

  it("an expired code step goes back to the password with the reason", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockImplementation((path: string) => path === "/auth/login"
      ? Promise.resolve({ second_step: "code" })
      : Promise.reject(new ApiError(401, { error: "signin_expired", message: "Sign in again: the code step took longer than five minutes." })));
    await passwordStep();
    await userEvent.type(await screen.findByLabelText(/^Code/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText(/took longer than five minutes/)).toBeTruthy();
    expect(screen.getByLabelText(/^Password/)).toBeTruthy();
  });
});

describe("an administrator resetting a colleague", () => {
  it("is offered for a colleague with an app, needs a reason and the password, and is never offered for yourself", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/operators") return Promise.resolve({ data: [
        { id: "me", email: "me@hotel", display_name: "", status: "active", roles: ["site_admin"], created_at: "2026-10-01T00:00:00Z", two_step_enrolled: true },
        { id: "c1", email: "colleague@hotel", display_name: "", status: "active", roles: ["front_office_operator"], created_at: "2026-10-01T00:00:00Z", two_step_enrolled: true },
        { id: "c2", email: "new@hotel", display_name: "", status: "active", roles: ["site_viewer"], created_at: "2026-10-01T00:00:00Z", two_step_enrolled: false },
      ] });
      if (path === "/auth/whoami") return Promise.resolve({ operator_id: "me", roles: ["site_admin"], email: "me@hotel" });
      return Promise.reject(new Error("not mocked"));
    });
    post.mockResolvedValue({ removed: true });
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await screen.findAllByText("colleague@hotel");
    expect(screen.queryByRole("button", { name: "Reset two-step sign-in for me@hotel" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset two-step sign-in for new@hotel" })).toBeNull();
    expect(screen.getByText("Sets up two-step at next sign-in")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Reset two-step sign-in for colleague@hotel" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Reset two-step sign-in" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(within(dialog).getByLabelText(/^Reason/), "lost their phone");
    await userEvent.type(within(dialog).getByLabelText(/^Your password/), "my-password");
    await userEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/operators/c1/reset-mfa", { reason: "lost their phone", password: "my-password" }));
  });
});
