import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// ADMIN CONSOLE SIGN-IN SECURITY, AS THE CONSOLE PRESENTS IT (audit N-1, N-8, N-18; edged migration 0119).
//
//   - /login?next= only ever returns to a path on this console.
//   - a must-change session sees only the password form; the form needs the current password and a new one
//     that meets the policy, and sends exactly { current_password, new_password }.
//   - an administrator's reset says it signs the operator out, defaults to "must choose their own", and is not
//     offered for the administrator's own row (that is Change my password, with the current password).
//   - sign-in protection is shown with its bounds, editable only by a role that may change operators, and is
//     saved with a reason and the password.

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const replace = vi.fn();
let search = new URLSearchParams("");
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      put: (...a: any[]) => put(...a),
      del: vi.fn(),
      postRaw: vi.fn(),
    },
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace, refresh: vi.fn() }),
  useSearchParams: () => search,
  usePathname: () => "/operators",
}));

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); replace.mockReset(); search = new URLSearchParams(""); });
afterEach(() => { cleanup(); vi.resetModules(); });

describe("safeNextPath (N-18)", () => {
  it("accepts only a path on this console", async () => {
    const { safeNextPath } = await import("@/lib/safe-next");
    const cases: Record<string, string> = {
      "/operators": "/operators",
      "/guest-accounts?page=2#top": "/guest-accounts?page=2#top",
      "https://evil.example/": "/dashboard",
      "//evil.example/x": "/dashboard",
      "/\\evil.example": "/dashboard",
      "\\\\evil.example": "/dashboard",
      "javascript:alert(1)": "/dashboard",
      "/%2F%2Fevil.example": "/%2F%2Fevil.example",
      " /sessions": "/sessions",
      "/login": "/dashboard",
      "": "/dashboard",
      "/x\u0000y": "/dashboard",
    };
    for (const [input, want] of Object.entries(cases)) {
      expect(safeNextPath(input), JSON.stringify(input)).toBe(want);
    }
    expect(safeNextPath(null)).toBe("/dashboard");
  });

  it("the login page sends a hostile ?next to the dashboard instead", async () => {
    search = new URLSearchParams("next=https://evil.example/phish");
    post.mockResolvedValue({ operator_id: "op", roles: ["site_admin"] });
    const Page = (await import("@/app/login/page")).default;
    render(<Page />);
    await userEvent.type(screen.getByLabelText(/Email or username/), "ops@hotel");
    await userEvent.type(screen.getByLabelText(/^Password/), "a-good-password");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/dashboard"));
  });

  it("a lockout is shown in the server's words", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(429, { error: "too_many_attempts", message: "Too many failed sign-in attempts. Wait 15 minutes and try again." }));
    const Page = (await import("@/app/login/page")).default;
    render(<Page />);
    await userEvent.type(screen.getByLabelText(/Email or username/), "ops@hotel");
    await userEvent.type(screen.getByLabelText(/^Password/), "wrong");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText(/Wait 15 minutes and try again/)).toBeTruthy();
    expect(replace).not.toHaveBeenCalled();
  });
});

describe("the forced password change (must_change_password)", () => {
  it("needs the current password and a policy-compliant new one, and sends exactly those", async () => {
    post.mockResolvedValue({ status: "changed", notice: "ok" });
    const onChanged = vi.fn();
    const { ForcedPasswordChange } = await import("@/components/forced-password-change");
    render(<ForcedPasswordChange email="admin" onChanged={onChanged} onLogout={vi.fn()} />);
    expect(screen.getByRole("heading", { name: "Set a new password" })).toBeTruthy();
    const go = screen.getByRole("button", { name: "Set new password" }) as HTMLButtonElement;
    await userEvent.type(screen.getByLabelText(/Current password/), "admin");
    await userEvent.type(screen.getByLabelText(/^New password/), "short");
    await userEvent.type(screen.getByLabelText(/Type the new password again/), "short");
    expect(go.disabled, "a 5-character password must not be offered").toBe(true);
    expect(screen.getByText(/5 more characters needed/)).toBeTruthy();

    await userEvent.clear(screen.getByLabelText(/^New password/));
    await userEvent.clear(screen.getByLabelText(/Type the new password again/));
    await userEvent.type(screen.getByLabelText(/^New password/), "a-much-better-one");
    await userEvent.type(screen.getByLabelText(/Type the new password again/), "a-much-better-one");
    expect(go.disabled).toBe(false);
    await userEvent.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/auth/password",
      { current_password: "admin", new_password: "a-much-better-one" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it("refuses the account name as the new password", async () => {
    const { ChangePasswordForm } = await import("@/components/change-password-form");
    render(<ChangePasswordForm email="frontdesk@hotel.example" onChanged={vi.fn()} />);
    await userEvent.type(screen.getByLabelText(/Current password/), "whatever-it-was");
    await userEvent.type(screen.getByLabelText(/^New password/), "frontdesk@hotel.example");
    await userEvent.type(screen.getByLabelText(/Type the new password again/), "frontdesk@hotel.example");
    expect(screen.getByText(/must not be your account name/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Change password" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("Operators (N-8)", () => {
  const OPS = {
    data: [
      { id: "me", email: "me@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["site_admin"] },
      { id: "o2", email: "desk@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["front_office_operator"], must_change_password: true },
    ],
  };
  const PROTECTION = {
    settings: { account_max_failures: 5, account_window_seconds: 900, ip_max_failures: 20, ip_window_seconds: 900, lockout_seconds: 900, config_version: 0 },
    bounds: [
      { key: "account_max_failures", label: "Wrong passwords before an account is locked", unit: "attempts", min: 3, max: 20, default: 5, explains: "per account" },
      { key: "account_window_seconds", label: "Counting window per account", unit: "seconds", min: 60, max: 86400, default: 900, explains: "" },
      { key: "ip_max_failures", label: "Wrong passwords from one address before it is locked", unit: "attempts", min: 5, max: 500, default: 20, explains: "" },
      { key: "ip_window_seconds", label: "Counting window per address", unit: "seconds", min: 60, max: 86400, default: 900, explains: "" },
      { key: "lockout_seconds", label: "Lockout", unit: "seconds", min: 60, max: 86400, default: 900, explains: "" },
    ],
  };
  function routes(roles: string[]) {
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles, operator_id: "me", email: "me@hotel" });
      if (path === "/operators") return Promise.resolve(OPS);
      if (path === "/operators/signin-protection") return Promise.resolve(PROTECTION);
      return Promise.resolve({});
    });
  }

  it("a reset says it signs the operator out, defaults to must-change, and sends require_change", async () => {
    routes(["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    expect(await screen.findByText("Must set a new password")).toBeTruthy();
    await userEvent.click(await screen.findByRole("button", { name: "Reset the password for desk@hotel" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/signed out everywhere/)).toBeTruthy();
    const box = within(dialog).getByRole("checkbox", { name: /Must choose their own password/ }) as HTMLInputElement;
    expect(box.checked).toBe(true);
    await userEvent.type(within(dialog).getByLabelText(/^New password/), "temporary-pass-1");
    await userEvent.type(within(dialog).getByLabelText(/Type it again/), "temporary-pass-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset password" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/operators/o2/set-password",
      { password: "temporary-pass-1", require_change: true }));
  });

  it("offers your own row Change my password, never an administrator reset", async () => {
    routes(["site_admin"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await screen.findByText("You");
    expect(screen.queryByRole("button", { name: "Reset the password for me@hotel" })).toBeNull();
    expect(screen.getByRole("link", { name: "Change my password" }).getAttribute("href")).toBe("/account/password");
  });

  it("sign-in protection is saved with a reason and the password", async () => {
    routes(["site_admin"]);
    put.mockResolvedValue({ config_version: 1 });
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByText("Sign-in protection"));
    const field = screen.getByLabelText(/Wrong passwords before an account is locked/) as HTMLInputElement;
    await userEvent.clear(field);
    await userEvent.type(field, "3");
    const save = screen.getByRole("button", { name: "Save sign-in protection" }) as HTMLButtonElement;
    expect(save.disabled, "a reason and the password are required").toBe(true);
    await userEvent.type(screen.getByLabelText(/^Reason/), "tighten after the audit");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/operators/signin-protection", {
      account_max_failures: 3, account_window_seconds: 900, ip_max_failures: 20, ip_window_seconds: 900,
      lockout_seconds: 900, reason: "tighten after the audit", password: "pw",
    }));
  });

  it("the IT manager reads sign-in protection and cannot change it", async () => {
    routes(["hotel_it_manager"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByText("Sign-in protection"));
    const field = screen.getByLabelText(/Wrong passwords before an account is locked/) as HTMLInputElement;
    expect(field.readOnly || field.disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Save sign-in protection" })).toBeNull();
  });
});
