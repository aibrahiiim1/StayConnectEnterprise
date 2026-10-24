import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

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
  it("an account without two-step sign-in is signed in by the password alone", async () => {
    post.mockResolvedValue({ second_step: "none", operator_id: "op", roles: ["site_admin"], must_change_password: false });
    await passwordStep();
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/dashboard"));
    expect(post).toHaveBeenCalledTimes(1);
    expect(screen.queryByLabelText(/^Code/)).toBeNull();
  });

  it("an account with two-step sign-in on needs the code before the console opens", async () => {
    post.mockImplementation((path: string) => path === "/auth/login"
      ? Promise.resolve({ second_step: "code" })
      : Promise.resolve({ operator_id: "op", roles: ["site_admin"] }));
    await passwordStep();
    await userEvent.type(await screen.findByLabelText(/^Code/), "123456");
    expect(replace).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/dashboard"));
    expect(post).toHaveBeenCalledWith("/auth/login/code", { code: "123456" });
    expect(post).not.toHaveBeenCalledWith("/auth/login/enrol", expect.anything());
  });

  it("a recovery code sign-in with few codes left says so before opening the console", async () => {
    post.mockImplementation((path: string) => path === "/auth/login"
      ? Promise.resolve({ second_step: "code" })
      : Promise.resolve({ operator_id: "op", roles: ["site_admin"], recovery_codes_left: 2 }));
    await passwordStep();
    await userEvent.type(await screen.findByLabelText(/^Code/), "ABCDE-FGHJK");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText(/You have 2 left/)).toBeTruthy();
    expect(replace).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/dashboard"));
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
    expect(screen.getByText("Two-step off")).toBeTruthy();
    expect(screen.getAllByText("Two-step on").length).toBe(2);
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

describe("your account: two-step sign-in", () => {
  async function securityPage() {
    const Page = (await import("@/app/(app)/account/security/page")).default;
    render(<Page />);
  }

  it("turning it on: the password, then the QR code and the first code, then the recovery codes once", async () => {
    let enrolled = false;
    const codes = Array.from({ length: 10 }, (_, i) => `AAAA${i}-BBBB${i}`);
    get.mockImplementation(() => Promise.resolve({ enrolled, recovery_codes_left: enrolled ? 10 : 0 }));
    post.mockImplementation((path: string) => {
      if (path === "/auth/mfa/setup") return Promise.resolve({ secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/x" });
      if (path === "/auth/mfa/confirm") { enrolled = true; return Promise.resolve({ enrolled: true, recovery_codes: codes }); }
      return Promise.reject(new Error("not mocked " + path));
    });
    await securityPage();
    expect(await screen.findByText("Off")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Turn on two-step sign-in" }));
    const cont = screen.getByRole("button", { name: "Continue" });
    expect((cont as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(screen.getByLabelText(/^Your password/), "my-password");
    await userEvent.click(cont);
    expect(post).toHaveBeenCalledWith("/auth/mfa/setup", { password: "my-password" });
    expect(await screen.findByAltText(/QR code/)).toBeTruthy();
    expect(screen.getByText("JBSW Y3DP EHPK 3PXP JBSW")).toBeTruthy();
    await userEvent.type(screen.getByLabelText(/Code from the app/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    expect(post).toHaveBeenCalledWith("/auth/mfa/confirm", { code: "123456" });
    const list = await screen.findByRole("list", { name: "Recovery codes" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(10);
    expect(screen.getByRole("button", { name: "Download" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "I have saved them" }));
    expect(await screen.findByText("On")).toBeTruthy();
    expect(screen.queryByRole("list", { name: "Recovery codes" })).toBeNull();
  });

  it("a wrong password at the start is reported and nothing is set up", async () => {
    const { ApiError } = await import("@/lib/api");
    get.mockResolvedValue({ enrolled: false, recovery_codes_left: 0 });
    post.mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "Your password is not correct." }));
    await securityPage();
    await userEvent.click(await screen.findByRole("button", { name: "Turn on two-step sign-in" }));
    await userEvent.type(screen.getByLabelText(/^Your password/), "wrong");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/password was not accepted/)).toBeTruthy();
    expect(screen.queryByAltText(/QR code/)).toBeNull();
  });

  it("turning it off needs the password and a code", async () => {
    const { ApiError } = await import("@/lib/api");
    let enrolled = true;
    get.mockImplementation(() => Promise.resolve({ enrolled, confirmed_at: "2026-10-01T00:00:00Z", recovery_codes_left: 8 }));
    post.mockImplementation((_p: string, body: any) => {
      if (body.code !== "123456") return Promise.reject(new ApiError(401, { error: "wrong_code", message: "That code is not right." }));
      enrolled = false;
      return Promise.resolve({ enrolled: false });
    });
    await securityPage();
    expect(await screen.findByText("On")).toBeTruthy();
    expect(screen.getByText("8 left")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Turn off" }));
    const submit = screen.getByRole("button", { name: "Turn off two-step sign-in" });
    await userEvent.type(screen.getByLabelText(/^Your password/), "my-password");
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(screen.getByLabelText(/^Code/), "000000");
    await userEvent.click(submit);
    expect(await screen.findByText(/That code is not right/)).toBeTruthy();
    expect(screen.queryByText("Off")).toBeNull();
    await userEvent.type(screen.getByLabelText(/^Code/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Turn off two-step sign-in" }));
    expect(post).toHaveBeenLastCalledWith("/auth/mfa/disable", { password: "my-password", code: "123456" });
    expect(await screen.findByText("Off")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toMatch(/Two-step sign-in is off/);
  });
});

describe("the sidebar suggestion", () => {
  async function hint() {
    const { MfaRecommendation } = await import("@/components/mfa-recommendation");
    render(<MfaRecommendation email="ops@hotel" />);
  }
  beforeEach(() => { try { window.localStorage.clear(); } catch { /* ignore */ } });

  it("suggests two-step sign-in to an account without it, and stays dismissed", async () => {
    get.mockResolvedValue({ enrolled: false, recovery_codes_left: 0 });
    await hint();
    const link = await screen.findByRole("link", { name: /turn on two-step sign-in/ });
    expect(link.getAttribute("href")).toBe("/account/security");
    await userEvent.click(screen.getByRole("button", { name: /Dismiss/ }));
    expect(screen.queryByRole("link", { name: /turn on two-step sign-in/ })).toBeNull();
    cleanup();
    await hint();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("link", { name: /turn on two-step sign-in/ })).toBeNull();
  });

  it("is never shown to an account that has it on", async () => {
    get.mockResolvedValue({ enrolled: true, recovery_codes_left: 10 });
    await hint();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/auth/mfa"));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("link", { name: /two-step/ })).toBeNull();
  });
});
