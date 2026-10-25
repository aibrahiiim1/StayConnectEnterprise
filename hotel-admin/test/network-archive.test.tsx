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
    { id: "pms", label: "PMS routing", state: "unresolved", detail: "Room sign-in is not offered on this network." },
    { id: "packages", label: "Package eligibility", state: "unresolved", detail: "1 package(s) are limited to other networks." },
  ],
  pms_candidates: [{ pms_interface_id: "p1", label: "Protel", routed_from: ["PRELIVE Test Guest"], preview: "Room sign-in on the Default Network would check Protel." }],
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
  it("names what is unresolved and routes PMS only after the operator confirms the preview", async () => {
    get.mockResolvedValue(ready);
    put.mockResolvedValue({});
    render(<DefaultNetworkCard writable reloadKey={0} />);
    await screen.findByText("Default Network: Client Wi-Fi");
    expect(screen.getAllByText("Needs your decision")).toHaveLength(2);
    expect(screen.getByText(/Lobby pass \(revision 2\) is limited to VLAN 60/)).toBeTruthy();
    expect(screen.getByText(/not offered on the Default Network and were not widened/)).toBeTruthy();
    expect(put).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /Route to this network…/ }));
    await screen.findByText("Room sign-in on the Default Network would check Protel.");
    expect(screen.getByText(/No PMS traffic is started or stopped, and nothing is posted/)).toBeTruthy();
    expect(put).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Route to the Default Network" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/pms-routing/d1", { pms_interface_id: "p1" }));
  });

  it("offers no routing action to a read-only role, and shows nothing without a Default Network", async () => {
    get.mockResolvedValue(ready);
    const { unmount } = render(<DefaultNetworkCard writable={false} reloadKey={0} />);
    await screen.findByText("Default Network: Client Wi-Fi");
    expect(screen.queryByRole("button", { name: /Route to this network…/ })).toBeNull();
    unmount();
    get.mockResolvedValue({ ...ready, present: false });
    const { container } = render(<DefaultNetworkCard writable reloadKey={0} />);
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(container.textContent).toBe("");
  });
});
