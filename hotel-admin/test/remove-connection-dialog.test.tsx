import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent, within } from "@testing-library/react";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a) },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));

import { RemoveConnectionDialog, type RemovalAssessment } from "@/components/pms/remove-connection-dialog";

const base = { id: "c1", display_label: "Aqua Club", lifecycle_state: "AUTH_DISABLED", blockers: [], consequences: [],
  facts: { stays: 0, in_house_stays: 0, live_entitlements: 0, events: 0, postings: 0, revisions: 1, ever_connected: false, ever_activated: false } };
const conn = { id: "c1", display_label: "Aqua Club" };

function open(a: Partial<RemovalAssessment>, onDone = vi.fn()) {
  get.mockResolvedValue({ ...base, ...a });
  render(<RemoveConnectionDialog open onOpenChange={vi.fn()} connection={conn} onDone={onDone} />);
  return onDone;
}

beforeEach(() => { get.mockReset(); post.mockReset(); });

describe("removing a PMS connection", () => {
  it("deletes a connection that was never used, after the name, a reason and the password", async () => {
    post.mockResolvedValue({});
    const done = open({ outcome: "DELETE", consequences: ["The connection and its saved drafts are deleted for good."] });
    await screen.findByText("Delete Aqua Club?");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/never connected to a PMS/)).toBeTruthy();
    const confirm = within(dialog).getByRole("button", { name: "Delete connection" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "SET_UP_BY_MISTAKE" } });
    fireEvent.change(within(dialog).getByLabelText(/Type the connection's name/), { target: { value: "Aqua Club" } });
    fireEvent.change(within(dialog).getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    await waitFor(() => expect(confirm.disabled).toBe(false));
    fireEvent.click(confirm);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/pms-interfaces/c1/removal", { outcome: "DELETE", reason_code: "SET_UP_BY_MISTAKE", password: "pw" }));
    await waitFor(() => expect(done).toHaveBeenCalled());
  });

  it("retires a used connection and says its history is kept", async () => {
    post.mockResolvedValue({});
    open({ outcome: "RETIRE", facts: { ...base.facts, stays: 208, ever_connected: true },
      consequences: ["Its history stays exactly as it is: stays, events, entitlements, postings, sign-in records and the audit trail."] });
    await screen.findByText("Retire Aqua Club?");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/cannot be deleted/)).toBeTruthy();
    expect(within(dialog).getByText(/history stays exactly as it is/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Retire connection" })).toBeTruthy();
  });

  it("refuses a connection that is on or routed to, and changes nothing", async () => {
    open({ outcome: "BLOCKED", lifecycle_state: "ACTIVE", blockers: [
      { code: "SWITCH_OFF_FIRST", title: "Switch the connection off first", detail: "It is active." },
      { code: "ROUTED", title: "Networks still use this connection for room sign-in", detail: "Change each network's room sign-in under PMS routing first.", networks: [{ id: "n1", name: "Client Wi-Fi", state: "serving" }] },
    ] });
    await waitFor(() => expect(screen.getAllByText("Aqua Club cannot be removed yet").length).toBeGreaterThan(0));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Switch the connection off first")).toBeTruthy();
    expect(within(dialog).getByText("Client Wi-Fi")).toBeTruthy();
    expect(within(dialog).getByRole("link", { name: "Open PMS routing" })).toBeTruthy();
    expect(within(dialog).queryByLabelText("Confirm your password")).toBeNull();
    expect(post).not.toHaveBeenCalled();
  });

  it("re-reads what removal means when the connection changed while the dialog was open", async () => {
    post.mockRejectedValue(Object.assign(new Error("What removing this connection would do has changed"), { code: "removal_changed" }));
    open({ outcome: "DELETE" });
    await screen.findByText("Delete Aqua Club?");
    const dialog = screen.getByRole("dialog");
    get.mockResolvedValue({ ...base, outcome: "RETIRE" });
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "NO_LONGER_NEEDED" } });
    fireEvent.change(within(dialog).getByLabelText(/Type the connection's name/), { target: { value: "Aqua Club" } });
    fireEvent.change(within(dialog).getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete connection" }));
    await screen.findByText("Retire Aqua Club?");
  });
});
