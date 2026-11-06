import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn() }) }));
vi.mock("next/link", () => ({ default: ({ href, children, className }: { href: string; children: React.ReactNode; className?: string }) => <a href={href} className={className}>{children}</a> }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/forgot-password",
}));
let emailLicensed = true;
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => null, moduleLicensed: () => emailLicensed };
});

import { api, ApiError } from "@/lib/api";
import ForgotPasswordPage from "@/app/forgot-password/page";
import LoginPage from "@/app/login/page";
import { OperatorPasswordReset } from "@/components/operator-password-reset";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;

const BOUNDS = [
  { key: "code_minutes", label: "How long a code works", unit: "minutes", min: 5, max: 60, default: 15, explains: "x" },
  { key: "max_attempts", label: "Wrong codes before a code stops working", unit: "attempts", min: 3, max: 10, default: 5, explains: "x" },
  { key: "account_per_hour", label: "Codes per email address each hour", unit: "requests", min: 1, max: 20, default: 5, explains: "x" },
  { key: "address_per_hour", label: "Requests from one network address each hour", unit: "requests", min: 5, max: 200, default: 20, explains: "x" },
];
const SETTINGS = { enabled: false, code_minutes: 15, max_attempts: 5, account_per_hour: 5, address_per_hour: 20, allow_last_site_admin: false, config_version: 0 };

beforeEach(() => { vi.clearAllMocks(); emailLicensed = true; });
afterEach(() => cleanup());

describe("Sign-in page", () => {
  it("offers forgot password only when the appliance says it is available", async () => {
    get.mockResolvedValue({ available: true, code_minutes: 15 });
    render(<LoginPage />);
    const link = await screen.findByRole("link", { name: "Forgot your password?" });
    expect(link).toHaveAttribute("href", "/forgot-password");
    cleanup();
    get.mockResolvedValue({ available: false });
    render(<LoginPage />);
    await waitFor(() => expect(get).toHaveBeenCalledWith("/auth/password-reset"));
    expect(screen.queryByRole("link", { name: "Forgot your password?" })).toBeNull();
  });
});

describe("Forgot password", () => {
  it("tells the person to ask a site admin when no code can be sent", async () => {
    get.mockResolvedValue({ available: false });
    render(<ForgotPasswordPage />);
    expect(await screen.findByText(/Ask a site admin to reset your password under Operators/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Email address")).toBeNull();
  });

  it("asks for a code, then changes the password with it", async () => {
    get.mockResolvedValue({ available: true, code_minutes: 15 });
    post.mockResolvedValueOnce({ code_minutes: 15, message: "If that email address belongs to an account here that can reset its password, a code is on its way. It works for 15 minutes." })
      .mockResolvedValueOnce({ message: "ok" });
    render(<ForgotPasswordPage />);
    await userEvent.type(await screen.findByLabelText("Email address"), "ann@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Email me a code" }));
    expect(post).toHaveBeenCalledWith("/auth/password-reset/request", { email: "ann@example.com" });
    await userEvent.type(await screen.findByLabelText(/Code from the email/), "12345678");
    await userEvent.type(screen.getByLabelText("New password"), "a-new-password-1");
    await userEvent.type(screen.getByLabelText("New password again"), "a-new-password-1");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/auth/password-reset/confirm",
      { email: "ann@example.com", code: "12345678", password: "a-new-password-1", mfa_code: "" }));
    expect(await screen.findByRole("heading", { name: "Password changed" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });

  it("asks for the authenticator code when the account has one, and says when a code is refused", async () => {
    get.mockResolvedValue({ available: true, code_minutes: 15 });
    post.mockResolvedValueOnce({ code_minutes: 15 })
      .mockRejectedValueOnce(new ApiError(401, { error: "mfa_required", message: "x" }))
      .mockRejectedValueOnce(new ApiError(400, { error: "code_refused", message: "x" }));
    render(<ForgotPasswordPage />);
    await userEvent.type(await screen.findByLabelText("Email address"), "ann@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Email me a code" }));
    await userEvent.type(await screen.findByLabelText(/Code from the email/), "12345678");
    await userEvent.type(screen.getByLabelText("New password"), "a-new-password-1");
    await userEvent.type(screen.getByLabelText("New password again"), "a-new-password-1");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    const mfa = await screen.findByLabelText(/Authenticator code/);
    await userEvent.type(mfa, "123456");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByText(/The code is wrong or has expired/)).toBeInTheDocument();
  });

  it("catches two different new passwords before sending anything", async () => {
    get.mockResolvedValue({ available: true, code_minutes: 15 });
    post.mockResolvedValueOnce({ code_minutes: 15 });
    render(<ForgotPasswordPage />);
    await userEvent.type(await screen.findByLabelText("Email address"), "ann@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Email me a code" }));
    await userEvent.type(await screen.findByLabelText(/Code from the email/), "12345678");
    await userEvent.type(screen.getByLabelText("New password"), "a-new-password-1");
    await userEvent.type(screen.getByLabelText("New password again"), "a-new-password-2");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByText(/The two passwords are not the same/)).toBeInTheDocument();
    expect(post).toHaveBeenCalledTimes(1);
  });
});

describe("Forgot password settings", () => {
  it("warns that forgot password is off at sign-in without an SMTP server and points to Delivery", async () => {
    get.mockResolvedValue({ settings: SETTINGS, bounds: BOUNDS, mail: { known: true, ready: false, kind: "stub" } });
    render(<OperatorPasswordReset writable />);
    await userEvent.click(await screen.findByText("Forgot password by email"));
    expect(screen.getByText("Forgot password is off at sign-in")).toBeInTheDocument();
    expect(screen.getByText(/No SMTP mail server is set up/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Delivery" })).toHaveAttribute("href", "/notifications");
  });

  it("asks the vendor when the licence has no email delivery", async () => {
    emailLicensed = false;
    get.mockResolvedValue({ settings: SETTINGS, bounds: BOUNDS, mail: { known: true, ready: false, kind: "stub" } });
    render(<OperatorPasswordReset writable />);
    await userEvent.click(await screen.findByText("Forgot password by email"));
    expect(screen.getByText(/Ask your OneGate vendor to add it/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Delivery" })).toBeNull();
  });

  it("shows no warning once SMTP is ready", async () => {
    get.mockResolvedValue({ settings: { ...SETTINGS, enabled: true }, bounds: BOUNDS, mail: { known: true, ready: true, kind: "smtp" } });
    render(<OperatorPasswordReset writable />);
    await userEvent.click(await screen.findByText("Forgot password by email"));
    expect(screen.queryByText("Forgot password is off at sign-in")).toBeNull();
  });

  it("turns it on only with the administrator's password and the version it was read at", async () => {
    get.mockResolvedValue({ settings: SETTINGS, bounds: BOUNDS, mail: { known: true, ready: true, kind: "smtp" } });
    put.mockResolvedValue({ config_version: 1, notice: "Saved." });
    render(<OperatorPasswordReset writable />);
    await userEvent.click(await screen.findByText("Forgot password by email"));
    await userEvent.click(screen.getByRole("switch", { name: "Offer forgot password at sign-in" }));
    await userEvent.click(screen.getByRole("button", { name: "Save password reset settings" }));
    await userEvent.type(await screen.findByLabelText(/Confirm your password/), "my-password");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/operators/password-reset", {
      enabled: true, allow_last_site_admin: false, code_minutes: 15, max_attempts: 5, account_per_hour: 5,
      address_per_hour: 20, expected_version: 0, password: "my-password",
    }));
  });

  it("is read-only for a role that cannot change operators", async () => {
    get.mockResolvedValue({ settings: SETTINGS, bounds: BOUNDS, mail: { known: true, ready: true, kind: "smtp" } });
    render(<OperatorPasswordReset writable={false} />);
    await userEvent.click(await screen.findByText("Forgot password by email"));
    expect(screen.getByRole("switch", { name: "Offer forgot password at sign-in" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save password reset settings" })).toBeNull();
  });
});
