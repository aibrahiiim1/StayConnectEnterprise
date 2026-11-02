import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfirmDialog, ConfirmPolicyContext } from "@/components/ui/dialog";

function Harness({ confirmOnly, onConfirm, strict = false }: { confirmOnly: boolean; onConfirm: (a: { reason: string; password: string }) => void; strict?: boolean }) {
  return (
    <ConfirmPolicyContext.Provider value={{ confirmOnly }}>
      <ConfirmDialog open onOpenChange={() => {}} title="Save the setting" requireReason requirePassword strictPassword={strict} onConfirm={onConfirm} />
    </ConfirmPolicyContext.Provider>
  );
}

describe("confirm-only dialogs", () => {
  it("ask for nothing but the confirmation, and record a standard reason", async () => {
    const onConfirm = vi.fn();
    render(<Harness confirmOnly onConfirm={onConfirm} />);
    expect(screen.queryByLabelText(/password/i)).toBeNull();
    expect(screen.queryByLabelText(/reason/i)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(onConfirm).toHaveBeenCalledWith({ reason: "Confirmed in the console", password: "" }));
  });

  it("still ask for the password where the action is account-takeover class", async () => {
    const onConfirm = vi.fn();
    render(<Harness confirmOnly strict onConfirm={onConfirm} />);
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Confirm" })).toBeDisabled();
  });

  it("keep the full prompts in a console that does not turn the policy on", () => {
    render(<Harness confirmOnly={false} onConfirm={vi.fn()} />);
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/reason/i)).toBeInTheDocument();
  });
});
