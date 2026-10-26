import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() },
}));

import { ImpactPanel } from "@/components/network/impact-panel";
import { ReplaceTopologyDialog } from "@/components/network/replace-topology-dialog";
import type { NetworkConsequence } from "@/lib/api/pms-routing";

const affected: NetworkConsequence[] = [{
  network_id: "n1", name: "Rooms", enabled: true, active_clients: 12, active_sessions: 12, live_room_entitlements: 9,
  room_sign_in: { policy: "SHARED_STRICT", state: "READY", summary: "ok", interfaces: ["Protel A", "Protel B"] },
  sign_in_methods_switched_off: ["voucher"], packages_limited_to_this_network: ["Lobby pass"],
  notes: ["Its room sign-in routing (Protel A, Protel B) stays with this network. It is never merged with another network's routing, and no guest moves to a different PMS."],
}];

beforeEach(() => get.mockReset());

describe("the impact of a topology change", () => {
  it("names the clients, the routing it keeps, the sign-in methods and what the switch and hypervisor must do", () => {
    render(<ImpactPanel affected={affected} external={["Switch: VLAN 60 must be carried tagged.", "OneGate does not change your switch, access points or hypervisor."]} />);
    expect(screen.getByText("Rooms")).toBeTruthy();
    expect(screen.getByText(/12 clients online/)).toBeTruthy();
    expect(screen.getByText(/9 with room access/)).toBeTruthy();
    expect(screen.getByText(/Shared strict set — Protel A, Protel B/)).toBeTruthy();
    expect(screen.getByText(/never merged with another network's routing/)).toBeTruthy();
    expect(screen.getByText(/Sign-in methods switched off here: voucher/)).toBeTruthy();
    expect(screen.getByText(/Outside OneGate/)).toBeTruthy();
    expect(screen.getByText(/does not change your switch/)).toBeTruthy();
  });

  it("renders nothing when there is nothing to say", () => {
    const { container } = render(<ImpactPanel affected={[]} external={[]} />);
    expect(container.textContent).toBe("");
  });

  it("the replacement dialog reads the consequences of the topology as entered, before anything is recorded", async () => {
    get.mockImplementation((p?: string) => { return Promise.resolve((p ?? "").startsWith("/network/interfaces") ? { interfaces: [{ name: "ens192", role: "guest_access" }] }
      : { affected, external_requirements: ["Hypervisor (ESXi): the port group must pass tagged frames (VLAN ID 4095)."] }); });
    render(<ReplaceTopologyDialog open onOpenChange={() => {}} onReplaced={() => {}}
      network={{ id: "n1", name: "Rooms", network_type: "untagged", parent_interface: "ens192", vlan_id: null, subnet_cidr: "10.20.0.0/20", gateway_ip: "10.20.0.1" }} />);
    await screen.findByText(/Hypervisor \(ESXi\)/);
    expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/network\/guest-networks\/n1\/impact\?.*to_type=untagged/));
    fireEvent.click(screen.getByRole("checkbox", { name: /tagged vlan/i }));
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/network\/guest-networks\/n1\/impact\?.*to_type=vlan/)));
    await waitFor(() => expect(screen.getByText("Rooms")).toBeTruthy());
  });
});
