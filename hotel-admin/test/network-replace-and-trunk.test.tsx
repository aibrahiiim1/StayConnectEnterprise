import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// CHANGING A NETWORK'S VLAN OR PORT, AND LAYING OUT A WHOLE TRUNK IN THE ONE WIZARD.

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
import NewGuestNetworkPage from "@/app/(app)/network/new/page";
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
  it("stages the change and says plainly that nothing has happened yet", async () => {
    route({
      "/auth/whoami": IT,
      "/network/guest-networks/net-1": NET,
      "/network/guest-networks/net-1/status": { id: "net-1", enabled: true, active_clients: 2 },
      "/network/dhcp/reservations": list([]),
      "/network/interfaces": IFACES,
    });
    post.mockResolvedValue({
      replacement_id: "r-1", state: "PENDING", original_network_id: "net-1",
      carries: { pools: 1, reservations: 1, pms_routes: 1 }, packages: ["Lobby only"],
    });
    render(<GuestNetworkDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: /change vlan or port/i }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: /create replacement/i });
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(within(dialog).getByLabelText("VLAN id"), { target: { value: "30" } });
    fireEvent.change(within(dialog).getByLabelText("Why"), { target: { value: "port moved to a trunk" } });
    await waitFor(() => expect((submit as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(submit);

    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/guest-networks/net-1/replace", {
      network_type: "vlan", parent_interface: "ens192", vlan_id: 30, reason: "port moved to a trunk",
    }));
    expect(await screen.findByText(/Change recorded — nothing has changed yet/)).toBeTruthy();
    expect(screen.getByText(/1 PMS route/)).toBeTruthy();
    expect(screen.getByText(/moved onto the\s+replacement automatically when you confirm/)).toBeTruthy();
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
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /tagged vlan/i }));
    fireEvent.change(within(dialog).getByLabelText("Why"), { target: { value: "no real change" } });
    expect(await within(dialog).findByText(/That is the current topology/)).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: /create replacement/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("New client network: one wizard for one network or a whole trunk", () => {
  async function toStep1() {
    route({
      "/auth/whoami": IT,
      "/network/interfaces": IFACES,
      "/network/guest-networks": list([
        { ...NET, id: "n-a", name: "Existing lobby", parent_interface: "ens192", network_type: "untagged", subnet_cidr: "10.111.0.0/24" },
      ]),
      "/network/revisions": list([]),
    });
    render(<NewGuestNetworkPage />);
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "Extensions" } });
    fireEvent.click(screen.getByRole("button", { name: /Next/ }));
    await screen.findByRole("radio", { name: /ens192/ });
  }

  it("shows what the chosen port already carries, and refuses a second untagged network on it", async () => {
    await toStep1();
    fireEvent.click(await screen.findByRole("radio", { name: /ens192/ }));
    expect(await screen.findByText(/What ens192 already carries/)).toBeTruthy();
    expect(screen.getByText(/Existing lobby/)).toBeTruthy();

    // untagged is the default, and this port already has one
    fireEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/already carries an untagged client network/i);
  });

  it("creates several VLANs on one trunk in a single batch, each with its own subnet", async () => {
    await toStep1();
    fireEvent.click(await screen.findByRole("radio", { name: /ens192/ }));
    fireEvent.click(screen.getByRole("switch", { name: /VLAN tagged/i }));
    fireEvent.change(screen.getByLabelText("VLAN 1 id"), { target: { value: "111" } });
    fireEvent.click(screen.getByRole("button", { name: /Add another VLAN/i }));
    fireEvent.change(screen.getByLabelText("VLAN 2 id"), { target: { value: "112" } });
    fireEvent.click(screen.getByRole("button", { name: /^Next$/ }));

    // Addressing: a distinct subnet per VLAN, avoiding the one already in use.
    const s1 = (await screen.findByLabelText("VLAN 1 subnet")) as HTMLInputElement;
    const s2 = screen.getByLabelText("VLAN 2 subnet") as HTMLInputElement;
    expect(s1.value).not.toBe("10.111.0.0/24");
    expect(s2.value).toBe("10.112.0.0/24");
    expect(s1.value).not.toBe(s2.value);
    expect(screen.getByRole("radio", { name: /Configure each VLAN separately/i })).toBeTruthy();

    // Walk to review and apply.
    fireEvent.click(screen.getByRole("button", { name: /^Next$/ }));
    fireEvent.click(await screen.findByRole("button", { name: /^Next$/ }));
    fireEvent.click(await screen.findByRole("button", { name: /^Next$/ }));
    expect(await screen.findByText(/trunk carrying VLANs 111, 112/i)).toBeTruthy();

    post.mockImplementation((path: string) => {
      if (path.endsWith("/batch")) return Promise.resolve({ networks: [{ id: "n1", bridge_name: "br-g111", portal_url: "u" }] });
      if (path === "/network/validate") return Promise.resolve({ validation: { ok: true } });
      return Promise.resolve({ revision_id: "rev-1", state: "pending_confirmation" });
    });
    fireEvent.click(screen.getByRole("button", { name: /Continue to apply/i }));
    fireEvent.click(await screen.findByRole("button", { name: /Create, validate & apply/i }));

    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/guest-networks/batch", expect.anything()));
    const [, body] = post.mock.calls.find(([p]) => String(p).endsWith("/batch"))!;
    expect(body.networks).toHaveLength(2);
    expect(body.networks[1]).toMatchObject({
      network_type: "vlan", parent_interface: "ens192", vlan_id: 112,
      subnet_cidr: "10.112.0.0/24", gateway_ip: "10.112.0.1",
      pools: [{ start_ip: "10.112.0.100", end_ip: "10.112.0.250" }],
    });
  });

  it("refuses the same VLAN twice on one trunk before anything is sent", async () => {
    await toStep1();
    fireEvent.click(await screen.findByRole("radio", { name: /ens192/ }));
    fireEvent.click(screen.getByRole("switch", { name: /VLAN tagged/i }));
    fireEvent.change(screen.getByLabelText("VLAN 1 id"), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: /Add another VLAN/i }));
    fireEvent.change(screen.getByLabelText("VLAN 2 id"), { target: { value: "40" } });
    expect(await screen.findByText(/VLAN 40 is listed twice/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Next$/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/VLAN 40 is listed twice/);
    expect(post).not.toHaveBeenCalled();
  });

  it("addressing helpers", () => {
    expect(suggestSubnet(20, new Set())).toBe("10.20.0.0/24");
    expect(suggestSubnet(20, new Set(["10.20.0.0/24"]))).toBe("10.1.0.0/24");
    expect(addressingFor("10.7.0.0/24")).toEqual({ gateway: "10.7.0.1", poolStart: "10.7.0.100", poolEnd: "10.7.0.250" });
    expect(addressingFor("10.7.0.0/22")).toEqual({ gateway: "", poolStart: "", poolEnd: "" });
  });
});
