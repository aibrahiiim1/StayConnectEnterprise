import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: (...a: unknown[]) => put(...a), patch: vi.fn(), del: vi.fn() },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { ArchivedNetworks, DefaultNetworkCard } from "@/components/network/default-network";

const archived = { data: [
  { id: "a1", name: "VLAN 60", archived_at: "2026-10-26T08:00:00Z", retired_by: "Full client networking reset", has_history: false, has_pms_routing: false },
  { id: "a2", name: "PRELIVE Test Guest", archived_at: "2026-10-26T08:00:00Z", retired_by: "Full client networking reset", has_history: true, has_pms_routing: true },
] };

const ready = {
  network_id: "d1", name: "Client Wi-Fi", present: true,
  items: [
    { id: "dhcp", label: "DHCP", state: "ok", detail: "Local DHCP, 10.20.0.10 – 10.20.15.254." },
    { id: "pms", label: "Room sign-in", state: "unresolved", detail: "Room sign-in is not offered on this network until you choose how it works." },
    { id: "packages", label: "Package eligibility", state: "unresolved", detail: "1 package(s) are limited to other networks." },
  ],
  pms_candidates: [{ pms_interface_id: "p1", label: "Protel", routed_from: ["PRELIVE Test Guest"], preview: "" }],
  room_sign_in: { network_id: "d1", name: "Client Wi-Fi", enabled: true, is_default: true, policy: "", state: "UNDECIDED", summary: "undecided", interfaces: [], reasons: [] },
  packages_pinned_to_other_networks: [{ code: "Lobby pass", revision: 2, networks: ["VLAN 60"] }],
};

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); });

describe("archived networks", () => {
  it("is collapsed by default, shows its count, and restores one without deleting anything", async () => {
    get.mockResolvedValue(archived);
    post.mockResolvedValue({});
    const onRestored = vi.fn();
    render(<ArchivedNetworks writable reloadKey={0} onRestored={onRestored} />);
    const toggle = await screen.findByRole("button", { name: /Archived networks \(2\)/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("VLAN 60")).toBeNull();
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    await screen.findByText("VLAN 60");
    expect(screen.getByText(/has client history; keeps its PMS routing/)).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: "Restore from archive" })[0]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/guest-networks/a1/unarchive"));
    await waitFor(() => expect(onRestored).toHaveBeenCalled());
  });

  it("shows nothing when nothing is archived, and no restore for a read-only role", async () => {
    get.mockResolvedValue({ data: [] });
    const { container, unmount } = render(<ArchivedNetworks writable reloadKey={0} onRestored={vi.fn()} />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    unmount();
    get.mockResolvedValue(archived);
    render(<ArchivedNetworks writable={false} reloadKey={0} onRestored={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: /Archived networks/ }));
    await screen.findByText("VLAN 60");
    expect(screen.queryByRole("button", { name: "Restore from archive" })).toBeNull();
  });
});

describe("Default Network checks", () => {
  const route = (path: string, over: Record<string, unknown> = {}) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: (over.roles as string[]) ?? ["site_admin"] });
    if (path === "/network/default") return Promise.resolve(over.ready ?? ready);
    if (path === "/pms-interfaces") return Promise.resolve({ interfaces: [{ id: "p1", display_label: "Protel", lifecycle_state: "ACTIVE", published: true }] });
    if (path === "/pms-routing") {
      return Promise.resolve({ routes: [], unmapped_guest_networks: [], policies: [{ network_id: "d1", name: "Client Wi-Fi", enabled: true, archived: false, is_default: true, mode: "", fingerprint: "fp-d1", interfaces: [] }] });
    }
    return Promise.resolve({});
  };

  it("names what is unresolved and routes PMS only after the operator previews and confirms their choice", async () => {
    get.mockImplementation((path: string) => route(path));
    post.mockResolvedValue({
      network: { network_id: "d1", name: "Client Wi-Fi", mode: "", fingerprint: "fp-d1", interfaces: [] },
      proposed: { mode: "DEDICATED", interfaces: [{ pms_interface_id: "p1", label: "Protel", lifecycle_state: "ACTIVE", published: true, in_house_stays: 3, room_auth_ready: true, health_known: true, also_used_by: [], live_sessions_on_this_network: 0 }] },
      added: [], removed: [], blockers: [], warnings: [],
      impact: { active_sessions: 0, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
      guarantees: ["No PMS traffic is started or stopped, and nothing is posted to any PMS."],
    });
    put.mockResolvedValue({});
    render(<DefaultNetworkCard reloadKey={0} />);
    await screen.findByText("Default Network: Client Wi-Fi");
    expect(screen.getAllByText("Needs your decision")).toHaveLength(2);
    expect(screen.getByText(/Lobby pass \(revision 2\) is limited to VLAN 60/)).toBeTruthy();
    expect(screen.getByText(/not offered on the Default Network and were not widened/)).toBeTruthy();
    expect(put).not.toHaveBeenCalled();

    fireEvent.click(await screen.findByRole("button", { name: /Configure room sign-in…/ }));
    fireEvent.click(await screen.findByRole("radio", { name: /one dedicated pms/i }));
    fireEvent.click(await screen.findByRole("radio", { name: /protel/i }));
    fireEvent.click(screen.getByRole("button", { name: /preview the change/i }));
    await screen.findByTestId("routing-preview");
    expect(screen.getByText(/nothing is posted to any PMS/)).toBeTruthy();
    expect(put).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /confirm and apply/i }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/pms-routing/d1", { mode: "DEDICATED", pms_interface_ids: ["p1"], based_on: "fp-d1" }));
  });

  it("offers no routing action to a role that cannot route, and shows nothing without a Default Network", async () => {
    get.mockImplementation((path: string) => route(path, { roles: ["front_office_operator"] }));
    const { unmount } = render(<DefaultNetworkCard reloadKey={0} />);
    await screen.findByText("Default Network: Client Wi-Fi");
    expect(screen.queryByRole("button", { name: /room sign-in…/ })).toBeNull();
    unmount();
    get.mockImplementation((path: string) => route(path, { ready: { ...ready, present: false } }));
    const { container } = render(<DefaultNetworkCard reloadKey={0} />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
