import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/lib/api";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() },
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/account/security",
}));
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

beforeEach(() => { get.mockReset(); post.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

async function securityPage() {
  const Page = (await import("@/app/(app)/account/security/page")).default;
  render(<Page />);
}

describe("Two-step sign-in page", () => {
  it("offers Try again when the state cannot be read, and recovers", async () => {
    get.mockRejectedValueOnce(new ApiError(500, { error: "query_failed", message: "two-step sign-in could not be read" }));
    get.mockResolvedValue({ enrolled: false, recovery_codes_left: 0 });
    await securityPage();
    expect(await screen.findByText("Your two-step sign-in could not be loaded")).toBeTruthy();
    expect(screen.queryByText("Checking…")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Try again/ }));
    expect(await screen.findByText("Off")).toBeTruthy();
  });

  it("clears a refused password and puts the cursor back in it", async () => {
    get.mockResolvedValue({ enrolled: false, recovery_codes_left: 0 });
    post.mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "Your password is not correct." }));
    await securityPage();
    await userEvent.click(await screen.findByRole("button", { name: "Turn on two-step sign-in" }));
    const field = screen.getByLabelText(/^Your password/) as HTMLInputElement;
    await userEvent.type(field, "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Your password was not accepted. Nothing was changed.")).toBeTruthy();
    expect(field.value).toBe("");
    expect(document.activeElement).toBe(field);
  });

  it("offers Start again when the set-up has expired", async () => {
    get.mockResolvedValue({ enrolled: false, recovery_codes_left: 0 });
    post.mockImplementation((path: string) => path === "/auth/mfa/setup"
      ? Promise.resolve({ secret: "JBSWY3DPEHPK3PXPJBSW", otpauth_uri: "otpauth://totp/x" })
      : Promise.reject(new ApiError(409, { error: "nothing_to_confirm", message: "There is no set-up waiting to be confirmed. Start again." })));
    await securityPage();
    await userEvent.click(await screen.findByRole("button", { name: "Turn on two-step sign-in" }));
    await userEvent.type(screen.getByLabelText(/^Your password/), "my-password");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await userEvent.type(await screen.findByLabelText(/Code from the app/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    await userEvent.click(await screen.findByRole("button", { name: /Start again/ }));
    expect(await screen.findByLabelText(/^Your password/)).toBeTruthy();
    expect(screen.queryByAltText(/QR code/)).toBeNull();
  });

  it("says plainly when no recovery codes are left", async () => {
    get.mockResolvedValue({ enrolled: true, confirmed_at: "2026-10-01T08:00:00Z", recovery_codes_left: 0 });
    await securityPage();
    expect(await screen.findByText("You have no recovery codes left. Make a new set now.")).toBeTruthy();
    expect(screen.queryByText(/:00:00/)).toBeNull();
  });

  it("confirms a new set of recovery codes once they are saved", async () => {
    get.mockResolvedValue({ enrolled: true, confirmed_at: "2026-10-01T08:00:00Z", recovery_codes_left: 5 });
    post.mockResolvedValue({ recovery_codes: ["AAAAA-BBBBB", "CCCCC-DDDDD"] });
    await securityPage();
    await userEvent.click(await screen.findByRole("button", { name: "Make new recovery codes" }));
    await userEvent.type(screen.getByLabelText(/Your password/), "my-password");
    await userEvent.type(screen.getByLabelText(/Code from your authenticator app/), "123456");
    await userEvent.click(screen.getByRole("button", { name: "Make codes" }));
    await userEvent.click(await screen.findByRole("button", { name: "I have saved them" }));
    expect(await screen.findByText("New recovery codes made. Your earlier codes no longer work.")).toBeTruthy();
  });
});
