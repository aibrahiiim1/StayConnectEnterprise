import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// CHANGING A NETWORK'S VLAN OR PORT, AND LAYING OUT SEVERAL VLANS ON A TRUNK.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver ??= ResizeObserverStub;

vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    body?: { error?: string };
    constructor(status: number, body?: { error?: string }) { super(body?.error ?? `HTTP ${status}`); this.status = status; this.body = body; }
  }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useParams: () => ({ id: "net-1" }),
}));

import { api } from "@/lib/api";
import GuestNetworkDetailPage from "@/app/(app)/network/[id]/page";
import TrunkVLANsPage from "@/app/(app)/network/trunk/page";
import { addressingFor, suggestSubnet } from "@/lib/network-trunk";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });
const IT = { roles: ["hotel_it_manager"] };

const NET = {
  id: "net-1", name: "Lobby", enabled: true, network_type: "untagged", vlan_id: null,
  parent_interface: "ens192", bridge_name: "br-g-1a2b3c4d", gateway_ip: "10.20.0.1", subnet_cidr: "10.20.0.0/24",
  dhcp_mode: "local", dns_mode: "appliance", domain_name: "guest.local",
  lease_default_seconds: 3600, lease_min_seconds: 900, lease_max_seconds: 7200,
  captive_portal_enabled: true, internet_access_enabled: true, nat_enabled: true, client_isolation_enabled: false,
  portal_url: "http://10.20.0.1:8380", pools: [{ start_ip: "10.20.0.100", end_ip: "10.20.0.250" }],
};
const IFACES = { interfaces: [
  { name: "ens192", role: "guest_trunk", mac: "aa", link_state: "up", mtu: 1500, ips: [], kind: "physical" },
  { name: "ens160", role: "wan", mac: "bb", link_state: "up", mtu: 1500, ips: [], kind: "physical" },
] };

function route(map: Record<string, unknown>) {
  get.mockImplementation((path: string) => {
    for (const [k, v] of Object.entries(map)) if (path === k || path.startsWith(k + "?")) return Promise.resolve(v);
    return Promise.reject(new Error(`unexpected GET ${path}`));
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); });

describe("Change VLAN or port", () => {
  it("replaces an untagged network with a tagged one, with a reason, and says what was carried", async () => {
    route({
      "/auth/whoami": IT,
      "/network/guest-networks/net-1": NET,
      "/network/guest-networks/net-1/status": { id: "net-1", enabled: true, active_clients: 2 },
      "/network/dhcp/reservations": list([]),
      "/network/interfaces": IFACES,
    });
    post.mockResolvedValue({
      id: "net-2", replaces: "net-1", bridge_name: "br-g30",
      carried: { pools: 1, reservations: 1, pms_routes: 1 },
      packages_limited_to_old_network: ["Lobby only"], active_sessions_on_old_network: 2,
    });
    render(<GuestNetworkDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: /change vlan or port/i }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: /create replacement/i });
    expect((submit as HTMLButtonElement).disabled).toBe(true); // no VLAN, no reason yet

    fireEvent.change(within(dialog).getByLabelText("VLAN id"), { target: { value: "30" } });
    fireEvent.change(within(dialog).getByLabelText("Why"), { target: { value: "port moved to a trunk" } });
    await waitFor(() => expect((submit as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(submit);

    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/guest-networks/net-1/replace", {
      network_type: "vlan", parent_interface: "ens192", vlan_id: 30, reason: "port moved to a trunk",
    }));
    expect(await screen.findByText(/Replacement created — not applied yet/)).toBeTruthy();
    expect(screen.getByText(/1 PMS route/)).toBeTruthy();
    expect(screen.getByText(/Lobby only/)).toBeTruthy();
    expect(screen.getByText(/2 client\(s\) are online/)).toBeTruthy();
  });

  it("will not 'replace' a network with the topology it already has", async () => {
    route({
      "/auth/whoami": IT,
      "/network/guest-networks/net-1": { ...NET, network_type: "vlan", vlan_id: 20, bridge_name: "br-g20" },
      "/network/guest-networks/net-1/status": { id: "net-1", enabled: true },
      "/network/dhcp/reservations": list([]),
      "/network/interfaces": IFACES,
    });
    render(<GuestNetworkDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: /change vlan or port/i }));
    const dialog = await screen.findByRole("dialog");
    // The dialog starts on the OTHER type; choosing the network's own type, port and VLAN again is no change.
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /tagged vlan/i }));
    fireEvent.change(within(dialog).getByLabelText("Why"), { target: { value: "no real change" } });
    expect(await within(dialog).findByText(/That is the current topology/)).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: /create replacement/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("Add VLANs on a trunk", () => {
  it("suggests a distinct subnet per VLAN and sends ONE batch", async () => {
    route({
      "/auth/whoami": IT,
      "/network/interfaces": IFACES,
      "/network/guest-networks": list([{ ...NET, subnet_cidr: "10.111.0.0/24" }]),
    });
    post.mockResolvedValue({ networks: [{ name: "VLAN 111", bridge_name: "br-g111" }, { name: "VLAN 112", bridge_name: "br-g112" }] });
    render(<TrunkVLANsPage />);
    fireEvent.change(await screen.findByLabelText("Trunk port"), { target: { value: "ens192" } });
    fireEvent.change(screen.getByLabelText("VLAN 1 id"), { target: { value: "111" } });
    fireEvent.change(screen.getByLabelText("VLAN 2 id"), { target: { value: "112" } });

    // 10.111.0.0/24 is taken by an existing network, so VLAN 111 gets another /24; VLAN 112 its own.
    const s1 = (screen.getByLabelText("VLAN 1 subnet") as HTMLInputElement).value;
    const s2 = (screen.getByLabelText("VLAN 2 subnet") as HTMLInputElement).value;
    expect(s1).not.toBe("10.111.0.0/24");
    expect(s2).toBe("10.112.0.0/24");
    expect(s1).not.toBe(s2);

    fireEvent.click(screen.getByRole("button", { name: /create 2 networks/i }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/network/guest-networks/batch");
    expect(body.networks).toHaveLength(2);
    expect(body.networks[1]).toMatchObject({ network_type: "vlan", parent_interface: "ens192", vlan_id: 112,
      subnet_cidr: "10.112.0.0/24", gateway_ip: "10.112.0.1", pools: [{ start_ip: "10.112.0.100", end_ip: "10.112.0.250" }] });
    expect(await screen.findByText(/2 client networks created — not applied yet/)).toBeTruthy();
  });

  it("refuses the same VLAN twice and a reused subnet before sending anything", async () => {
    route({ "/auth/whoami": IT, "/network/interfaces": IFACES, "/network/guest-networks": list([]) });
    render(<TrunkVLANsPage />);
    fireEvent.change(await screen.findByLabelText("Trunk port"), { target: { value: "ens192" } });
    fireEvent.change(screen.getByLabelText("VLAN 1 id"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("VLAN 2 id"), { target: { value: "40" } });
    expect(await screen.findByText(/VLAN 40 is listed twice/)).toBeTruthy();
    expect((screen.getByRole("button", { name: /create 2 networks/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(post).not.toHaveBeenCalled();
  });

  it("addressing helpers", () => {
    expect(suggestSubnet(20, new Set())).toBe("10.20.0.0/24");
    expect(suggestSubnet(20, new Set(["10.20.0.0/24"]))).toBe("10.1.0.0/24");
    expect(addressingFor("10.7.0.0/24")).toEqual({ gateway: "10.7.0.1", poolStart: "10.7.0.100", poolEnd: "10.7.0.250" });
    expect(addressingFor("10.7.0.0/22")).toEqual({ gateway: "", poolStart: "", poolEnd: "" });
  });
});
