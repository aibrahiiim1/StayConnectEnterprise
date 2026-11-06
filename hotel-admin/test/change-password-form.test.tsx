import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/lib/api";

const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: vi.fn(), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn() } };
});

beforeEach(() => { post.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderForm(onChanged = vi.fn()) {
  const { ChangePasswordForm } = await import("@/components/change-password-form");
  render(<ChangePasswordForm email="me@hotel" onChanged={onChanged} />);
  return onChanged;
}

async function fill(current: string, next: string, again = next) {
  await userEvent.type(screen.getByLabelText(/Current password/), current);
  await userEvent.type(screen.getByLabelText(/^New password/), next);
  await userEvent.type(screen.getByLabelText(/Type the new password again/), again);
}

describe("Change my password", () => {
  it("says why Change password is disabled while a field is empty", async () => {
    await renderForm();
    expect(screen.getByText("Type your current password.")).toBeTruthy();
    await userEvent.type(screen.getByLabelText(/Current password/), "old-password-1");
    expect(screen.getByText("Choose a new password.")).toBeTruthy();
    await userEvent.type(screen.getByLabelText(/^New password/), "a-brand-new-pass-1");
    expect(screen.getByText("Type the new password again.")).toBeTruthy();
  });

  it("clears a wrong current password, puts the cursor back in it and keeps the new one", async () => {
    post.mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "Your current password is not correct." }));
    await renderForm();
    await fill("wrong-password-1", "a-brand-new-pass-1");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByText("Your current password is not correct. Your password was not changed.")).toBeTruthy();
    const current = screen.getByLabelText(/Current password/) as HTMLInputElement;
    expect(current.value).toBe("");
    expect(document.activeElement).toBe(current);
    expect((screen.getByLabelText(/^New password/) as HTMLInputElement).value).toBe("a-brand-new-pass-1");
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });

  it("puts a failure in plain words", async () => {
    post.mockRejectedValue(new ApiError(500, { error: "internal", message: "hash failed" }));
    await renderForm();
    await fill("old-password-1", "a-brand-new-pass-1");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByText(/Your old password still works\./)).toBeTruthy();
    expect(screen.queryByText("hash failed")).toBeNull();
  });

  it("shows a password on request", async () => {
    await renderForm();
    const field = screen.getByLabelText(/Current password/) as HTMLInputElement;
    expect(field.type).toBe("password");
    await userEvent.click(screen.getAllByRole("button", { name: "Show password" })[0]);
    expect(field.type).toBe("text");
  });

  it("passes on the appliance's notice when the password is changed", async () => {
    post.mockResolvedValue({ notice: "Your password was changed. Any other place you were signed in has been signed out." });
    const onChanged = await renderForm();
    await fill("old-password-1", "a-brand-new-pass-1");
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith("Your password was changed. Any other place you were signed in has been signed out."));
    expect(post).toHaveBeenCalledWith("/auth/password", { current_password: "old-password-1", new_password: "a-brand-new-pass-1" });
  });
});
