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
const push = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));
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
  get.mockReset(); post.mockReset(); push.mockReset();
  get.mockImplementation((p: string) => Promise.resolve(p === "/network/recovery" ? status : p === "/network/changes" ? { groups: [] }
    : p === "/auth/whoami" ? { roles: ["site_admin"] } : p === "/pms-interfaces" ? { interfaces: [] } : items));
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
    await waitFor(() => expect(push).toHaveBeenCalledWith("/network"));
  });

  it("says plainly that a staged recovery has not been applied", async () => {
    get.mockImplementation((p: string) => Promise.resolve(p === "/network/recovery" ? status : p === "/network/changes"
      ? { groups: [{ group_id: "g1", group_kind: "FULL_RESET", state: "PENDING", created_at: "", changes: [] }] } : items));
    render(<RecoveryPage />);
    await screen.findByText("A recovery is staged and has not been applied");
    expect(screen.getByText(/the client networks are exactly as they were/)).toBeTruthy();
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

  it("shows staged, applied and confirmed as three different things", async () => {
    for (const [state, text, absent] of [
      ["STAGED", /Nothing has changed yet\. Press Apply changes/, /archived and the Default Network/],
      ["APPLIED", /It is live and affecting clients/, /archived and the Default Network/],
      ["CONFIRMED", /archived and the Default Network serves clients/, null],
    ] as const) {
      get.mockImplementation((p: string) => Promise.resolve(p === "/network/recovery"
        ? { ...status, latest: { group_id: "g1", kind: "FULL_RESET", state, at: "2026-10-26T08:00:00Z" } } : p === "/network/changes" ? { groups: [] } : items));
      const { unmount } = render(<RecoveryPage />);
      await screen.findByText(/Latest recovery: Full client networking reset/);
      expect(screen.getByText(text)).toBeTruthy();
      if (absent) expect(screen.queryByText(absent)).toBeNull();
      unmount();
    }
  });

  it("tells an applied recovery apart from a staged one", async () => {
    get.mockImplementation((p: string) => Promise.resolve(p === "/network/recovery" ? status : p === "/network/changes"
      ? { groups: [{ group_id: "g1", group_kind: "FULL_RESET", state: "APPLIED", created_at: "", changes: [] }] } : items));
    render(<RecoveryPage />);
    await screen.findByText("A recovery is applied and waiting for you to keep it");
    expect(screen.queryByText("A recovery is staged and has not been applied")).toBeNull();
    expect(screen.queryByText(/exactly as they were/)).toBeNull();
  });

  it("shows a prepared network that would be created when the old one was changed beyond restoring", async () => {
    post.mockResolvedValueOnce({ ...plan, op: "factory_defaults", networks: [
      { id: "", name: "Client Wi-Fi", action: "create_and_switch_on", changes: ["untagged on ens192, 10.20.0.0/20, local DHCP"], active_clients: 0 },
      { id: "n2", name: "Old guests", action: "switch_off", active_clients: 5 }] });
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working/);
    fireEvent.click(screen.getAllByText(/Review…/)[1]);
    await screen.findByText(/Client Wi-Fi: Prepared network created and switched on/);
    expect((screen.getByRole("button", { name: "Stage this recovery" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("needs the site name typed for a full reset", async () => {
    post.mockResolvedValueOnce({ ...plan, op: "full_reset", confirm_site_name: "Coral Resort" }).mockResolvedValueOnce({});
    render(<RecoveryPage />);
    await screen.findByText(/Last confirmed working/);
    fireEvent.click(screen.getAllByText(/Review…/)[2]);
    const stage = await screen.findByRole("button", { name: "Stage this recovery" });
    expect((stage as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/to confirm a full reset/), { target: { value: "Coral Resort" } });
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

describe("the optional room sign-in step after a confirmed reset", () => {
  const confirmed = (step: unknown) => ({ ...status, latest: { group_id: "g1", kind: "FULL_RESET", state: "CONFIRMED", at: "2026-10-25T14:59:06Z", room_sign_in_step: step } });
  const serve = (st: unknown, roles = ["site_admin"]) => get.mockImplementation((p: string) => Promise.resolve(
    p === "/network/recovery" ? st : p === "/network/changes" ? { groups: [] } : p === "/auth/whoami" ? { roles } : p === "/pms-interfaces" ? { interfaces: [] } : items));

  it("offers it, separately from the reset, saying the reset changed no PMS routing", async () => {
    serve(confirmed({ offered: true, network_id: "d1", network_name: "Client Wi-Fi", policy: "", state: "UNDECIDED", summary: "undecided" }));
    render(<RecoveryPage />);
    await screen.findByText("Room sign-in on the new network (optional)");
    expect(screen.getByText(/The reset did not change any PMS routing/)).toBeTruthy();
    expect(await screen.findByRole("button", { name: /Configure room sign-in…/ })).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
  });

  it("states the decision once one has been made and no longer offers it", async () => {
    serve(confirmed({ offered: false, network_id: "d1", network_name: "Client Wi-Fi", policy: "SHARED_STRICT", state: "READY", summary: "Room sign-in checks all 2 selected PMS interfaces strictly." }));
    render(<RecoveryPage />);
    await screen.findByText(/Room sign-in checks all 2 selected PMS interfaces strictly/);
    expect(screen.queryByRole("button", { name: /Configure room sign-in…/ })).toBeNull();
  });

  it("does not appear while the reset is only staged or applied, nor for a role that cannot route", async () => {
    serve({ ...status, latest: { group_id: "g1", kind: "FULL_RESET", state: "APPLIED", at: "2026-10-25T14:58:53Z" } });
    const { unmount } = render(<RecoveryPage />);
    await screen.findByText(/Latest recovery/);
    expect(screen.queryByText("Room sign-in on the new network (optional)")).toBeNull();
    unmount();
    serve(confirmed({ offered: true, network_id: "d1", network_name: "Client Wi-Fi", policy: "", state: "UNDECIDED", summary: "" }), ["front_office_operator"]);
    render(<RecoveryPage />);
    await screen.findByText("Room sign-in on the new network (optional)");
    expect(screen.queryByRole("button", { name: /Configure room sign-in…/ })).toBeNull();
  });
});
