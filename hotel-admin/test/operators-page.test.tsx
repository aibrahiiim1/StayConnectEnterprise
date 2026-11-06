import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/lib/api";
import { lockoutWords, operatorError, periodWords } from "@/lib/operator-errors";

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
  usePathname: () => "/operators",
}));

const OPS = {
  data: [
    { id: "me", email: "me@hotel", display_name: "", status: "active", roles: ["site_admin"], created_at: "2026-01-01T00:00:00Z", must_change_password: false, two_step_enrolled: false },
    { id: "o2", email: "desk@hotel", display_name: "", status: "active", roles: ["site_viewer"], created_at: "2026-01-01T00:00:00Z", must_change_password: false, two_step_enrolled: true },
    { id: "o3", email: "gone@hotel", display_name: "", status: "disabled", roles: [], created_at: "2026-01-01T00:00:00Z", must_change_password: false, two_step_enrolled: false },
  ],
};
let failing: Set<string>;

beforeEach(() => {
  get.mockReset(); post.mockReset();
  failing = new Set();
  get.mockImplementation((path: string) => {
    if (failing.has(path)) return Promise.reject(new ApiError(500, { error: "internal", message: "query failed" }));
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"], operator_id: "me", email: "me@hotel" });
    if (path === "/operators") return Promise.resolve(OPS);
    return Promise.resolve({});
  });
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const Page = (await import("@/app/(app)/operators/page")).default;
  render(<Page />);
}

describe("Operator words", () => {
  it("puts the appliance's refusals in plain words", () => {
    expect(operatorError(new ApiError(401, { error: "reauth_required", message: "your password is required to change a staff account" }), "x"))
      .toBe("Your password was not accepted. Nothing was changed.");
    expect(operatorError(new ApiError(409, { error: "conflict", message: "email already exists" }), "x"))
      .toBe("That email or username is already an operator. Nothing was changed.");
    expect(operatorError(new ApiError(403, { error: "limit_exceeded", message: "license limit reached for max_operators" }), "x"))
      .toContain("Your licence allows no more operators");
    expect(operatorError(new ApiError(500, { error: "internal", message: "insert failed" }), "Fallback.")).toBe("Fallback.");
    expect(periodWords(730)).toBe("2 years");
    expect(periodWords(90)).toBe("90 days");
    expect(lockoutWords(1, 30)).toBe("1 wrong password locks an account for 30 s");
    expect(lockoutWords(5, 900)).toBe("5 wrong passwords lock an account for 15 min");
  });
});

describe("Operators page", () => {
  it("offers Try again when the operators cannot be read, and recovers", async () => {
    failing.add("/operators");
    await renderPage();
    expect(await screen.findByText("The operators could not be loaded")).toBeTruthy();
    failing.delete("/operators");
    await userEvent.click(screen.getAllByRole("button", { name: /Try again/ })[0]);
    expect(await screen.findByText("desk@hotel")).toBeTruthy();
  });

  it("says when your role could not be checked", async () => {
    failing.add("/auth/whoami");
    await renderPage();
    expect(await screen.findByText("Your role could not be checked")).toBeTruthy();
  });

  it("shows a refused password in plain words and forgets it when the dialog closes", async () => {
    post.mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "your password is required to change a staff account" }));
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Reset password: desk@hotel" }));
    let dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/^New password/), "temporary-pass-1");
    await userEvent.type(within(dialog).getByLabelText(/Type it again/), "temporary-pass-1");
    await userEvent.type(within(dialog).getByLabelText(/^Your password/), "wrong-one");
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset password" }));
    expect(await within(dialog).findByText("Your password was not accepted. Nothing was changed.")).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await userEvent.click(screen.getByRole("button", { name: "Reset password: desk@hotel" }));
    dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText(/Your password was not accepted/)).toBeNull();
  });

  it("does not offer to remove your own Site admin role, and says why", async () => {
    await renderPage();
    await userEvent.click(await screen.findByText("me@hotel"));
    expect(await screen.findByText("You cannot remove your own")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Remove Site admin from me@hotel/ })).toBeNull();
  });

  it("marks two-step as not applicable for a disabled operator", async () => {
    await renderPage();
    await screen.findByText("gone@hotel");
    expect(screen.getByText("Not applicable")).toBeTruthy();
    expect(screen.queryByText("—")).toBeNull();
  });

  it("keeps a settings panel on the page with Try again when it cannot be read", async () => {
    failing.add("/operators/session-policy");
    await renderPage();
    expect(await screen.findByText("The session limits settings could not be loaded")).toBeTruthy();
  });
});
