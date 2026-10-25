import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("@/components/network/shared", () => ({ useNetworkAccess: () => ({ known: true, writable: true }) }));

import RecoveryPage from "@/app/(app)/network/recovery/page";

const status = { last_working: { revision: 7, confirmed_at: "2026-10-25T08:00:00Z", available: true }, factory: { available: true, subnet: "10.20.0.0/20" }, review_open: 1 };
const items = { data: [{ id: "r1", network_id: "n1", network: "Rooms", layer: "pms_routing", detail: "PMS routing differs.", room_sign_in_paused: true, created_at: "" }] };
const plan = {
  stageable: true, op: "restore_last_working", summary: "Put every client network back.", cannot_restore: [], conflicts: [],
  networks: [{ id: "n1", name: "Rooms", action: "restore_settings", changes: ["name"], active_clients: 0 }, { id: "n2", name: "Extra", action: "switch_off", active_clients: 3 }],
  review: [{ network_id: "n1", network: "Rooms", layer: "portal_profile", state: "CHANGED", detail: "Put back." }, { network_id: "n1", network: "Rooms", layer: "signin", state: "UNCHANGED", detail: "Same." }],
  protected: ["Guest records."], switch_checklist: ["The switch passes the VLANs."],
};

beforeEach(() => {
  get.mockReset(); post.mockReset();
  get.mockImplementation((p: string) => Promise.resolve(p === "/network/recovery" ? status : items));
});

describe("recovery page", () => {
  it("shows what would change, who loses access, the layers and what stays untouched, then stages", async () => {
    post.mockResolvedValueOnce(plan).mockResolvedValueOnce({});
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working: revision 7/);
    fireEvent.click(screen.getAllByText(/Review…/)[0]);
    await screen.findByText(/Extra: Switched off/);
    expect(screen.getByText(/3 connected device\(s\) lose access/)).toBeTruthy();
    expect(screen.getByText("Restored – review needed")).toBeTruthy();
    expect(screen.getByText("Unchanged")).toBeTruthy();
    expect(screen.getByText("Guest records.")).toBeTruthy();
    expect(screen.getByText("Check outside the appliance")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Stage this recovery" }));
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/network/recovery/stage", { op: "restore_last_working", confirm_site_name: "" }));
  });

  it("will not stage a plan with a conflict or one that changes nothing", async () => {
    post.mockResolvedValueOnce({ ...plan, stageable: false, conflicts: ["The WAN network overlaps 10.20.0.0/20. The factory address is never forced onto a conflicting network."] });
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working/);
    fireEvent.click(screen.getAllByText(/Review…/)[1]);
    await screen.findByText("Cannot be staged");
    expect((screen.getByRole("button", { name: "Stage this recovery" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("will not stage a recovery that cannot be done completely", async () => {
    post.mockResolvedValueOnce({ ...plan, stageable: false, cannot_restore: ["Gone was deleted after it was last confirmed."] });
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working/);
    fireEvent.click(screen.getAllByText(/Review…/)[0]);
    await screen.findByText("Cannot be restored from here");
    expect((screen.getByRole("button", { name: "Stage this recovery" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("needs the site name typed for a full reset", async () => {
    post.mockResolvedValueOnce({ ...plan, op: "full_reset", confirm_site_name: "Coral Resort" }).mockResolvedValueOnce({});
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working/);
    fireEvent.click(screen.getAllByText(/Review…/)[2]);
    const stage = await screen.findByRole("button", { name: "Stage this recovery" });
    expect((stage as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Type the site name/), { target: { value: "Coral Resort" } });
    expect((stage as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(stage);
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/network/recovery/stage", { op: "full_reset", confirm_site_name: "Coral Resort" }));
  });

  it("lists what a restore asks staff to review and acknowledges it", async () => {
    post.mockResolvedValue({});
    render(<RecoveryPage />);
    await screen.findByText(/PMS routing differs/);
    expect(screen.getByText("Room sign-in paused here")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/recovery/review/r1/acknowledge", {}));
  });
});
