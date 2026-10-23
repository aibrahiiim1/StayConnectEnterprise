import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api, ApiError } from "@/lib/api";
import TwoStepSigninPage from "@/app/(app)/account/security/page";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  get.mockResolvedValue({ enrolled: true, confirmed_at: "2026-10-01T10:00:00Z", recovery_codes_left: 4 });
});

async function open() {
  const user = userEvent.setup();
  render(<TwoStepSigninPage />);
  await user.click(await screen.findByRole("button", { name: "Make new recovery codes" }));
  return user;
}

describe("new recovery codes", () => {
  it("needs the password and a 6-digit code, and sends both", async () => {
    const user = await open();
    const make = screen.getByRole("button", { name: "Make codes" });
    await user.type(screen.getByLabelText(/Your password/), "pw-2026");
    expect(make).toBeDisabled();
    const code = screen.getByLabelText(/Code from your authenticator app/);
    await user.type(code, "ABCDE-FGHJK");
    expect(make).toBeDisabled();
    await user.clear(code);
    await user.type(code, "123456");
    expect(make).toBeEnabled();
    post.mockResolvedValue({ recovery_codes: ["AAAAA-BBBBB"] });
    await user.click(make);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/auth/mfa/recovery-codes", { password: "pw-2026", code: "123456" }));
    expect(await screen.findByRole("list", { name: "New recovery codes" })).toHaveTextContent("AAAAA-BBBBB");
  });

  it("says at the field when the code is refused", async () => {
    const user = await open();
    await user.type(screen.getByLabelText(/Your password/), "pw-2026");
    const code = screen.getByLabelText(/Code from your authenticator app/);
    await user.type(code, "123456");
    post.mockRejectedValue(new ApiError(401, { error: "wrong_code", message: "That code is not right." }));
    await user.click(screen.getByRole("button", { name: "Make codes" }));
    expect(await screen.findByText("That code is not right.")).toBeInTheDocument();
    expect(code).toHaveAttribute("aria-invalid", "true");
    expect(code).toHaveValue("");
  });
});
