import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
const post = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: (...a: unknown[]) => del(...a) },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));

import { FactoryNetworkCard, PortModeCard, StagedChangesList } from "@/components/network/lifecycle";

const factory = {
  state: "prepared",
  reason: "The prepared Access network has not been activated.",
  template: {
    name: "Access", mode: "Access (untagged)", subnet_cidr: "10.20.0.0/20", gateway_ip: "10.20.0.1", dhcp: "Local DHCP",
    pool: { start_ip: "10.20.0.10", end_ip: "10.20.15.254" }, services: ["DNS", "Captive portal", "NAT"], pms: "Not connected",
  },
  lan_interface: "ens192", conflicts: [], checklist: ["The LAN port is cabled to the access switch."],
};

beforeEach(() => {
  get.mockReset(); post.mockReset(); del.mockReset();
});

describe("factory network", () => {
  it("cannot be staged until the no-other-DHCP-server acknowledgement is given, and posts it", async () => {
    get.mockResolvedValue(factory);
    post.mockResolvedValue({});
    const onStaged = vi.fn();
    render(<FactoryNetworkCard writable onStaged={onStaged} />);
    fireEvent.click(await screen.findByText(/Activate…/));
    const stage = await screen.findByRole("button", { name: /Stage activation/ });
    expect((stage as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByLabelText(/No other DHCP server/));
    fireEvent.click(stage);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/factory/activate", { no_other_dhcp_server: true }));
    await waitFor(() => expect(onStaged).toHaveBeenCalled());
  });

  it("is not shown once activated, and is read-only for a role that cannot change networks", async () => {
    get.mockResolvedValue({ ...factory, state: "activated" });
    const { container, unmount } = render(<FactoryNetworkCard writable onStaged={vi.fn()} />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    unmount();
    get.mockResolvedValue(factory);
    render(<FactoryNetworkCard writable={false} onStaged={vi.fn()} />);
    await screen.findByText("10.20.0.0/20 (gateway 10.20.0.1)");
    expect(screen.queryByText(/Activate…/)).toBeNull();
  });
});

describe("staged changes", () => {
  it("lists a group and cancels it", async () => {
    get.mockResolvedValue({ groups: [{ group_id: "g1", group_kind: "PORT_MODE", state: "PENDING", created_at: "", changes: [{ id: "c", network_id: "n", network: "VLAN 60", kind: "ENABLE", state: "PENDING" }] }] });
    del.mockResolvedValue({});
    const onCancelled = vi.fn();
    render(<StagedChangesList writable reloadKey={0} onCancelled={onCancelled} />);
    await screen.findByText(/switch on VLAN 60/);
    fireEvent.click(screen.getByRole("button", { name: /Cancel Port mode change/ }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/network/changes/g1"));
    await waitFor(() => expect(onCancelled).toHaveBeenCalled());
  });
});

describe("port mode", () => {
  const ports = { ports: [
    { name: "ens160", protected: true, mode: "access", vlans: [], staged_changes: 0 },
    { name: "ens192", protected: false, mode: "access", native: { id: "a", name: "Access", subnet_cidr: "10.20.0.0/20", enabled: true, active_clients: 3 }, vlans: [], staged_changes: 0 },
  ] };
  const plan = {
    mode: "trunk", port: "ens192", conflicts: [], switch_off: [],
    create: [{ name: "VLAN 60", vlan_id: 60, subnet_cidr: "10.60.0.0/24", gateway_ip: "10.60.0.1", pools: [] }],
    impact: ["Connected clients stay on the Access network."], switch_requirements: ["The switch port is a trunk allowing VLAN 60."],
  };

  it("never offers the protected WAN/Management port, previews before staging, and separates what the appliance cannot verify", async () => {
    get.mockResolvedValue(ports);
    post.mockResolvedValueOnce(plan).mockResolvedValueOnce({});
    render(<PortModeCard writable reloadKey={0} onStaged={vi.fn()} />);
    await screen.findByText("ens192");
    expect(screen.queryByText("ens160")).toBeNull();
    fireEvent.click(screen.getByText(/Convert to Trunk…/));
    fireEvent.change(screen.getByLabelText("VLAN id 1"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText(/Check outside the appliance/);
    expect(post).toHaveBeenLastCalledWith("/network/ports/ens192/mode", { mode: "trunk", preview: true, vlans: [{ vlan_id: 60, name: "" }] });
    fireEvent.click(screen.getByRole("button", { name: /Stage this change/ }));
    await waitFor(() => expect(post).toHaveBeenLastCalledWith("/network/ports/ens192/mode", { mode: "trunk", preview: false, vlans: [{ vlan_id: 60, name: "" }] }));
  });

  it("blocks staging while the plan reports a conflict", async () => {
    get.mockResolvedValue(ports);
    post.mockResolvedValueOnce({ ...plan, conflicts: ["10.60.0.0/24 overlaps Access"] });
    render(<PortModeCard writable reloadKey={0} onStaged={vi.fn()} />);
    fireEvent.click(await screen.findByText(/Convert to Trunk…/));
    fireEvent.change(screen.getByLabelText("VLAN id 1"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    const stage = await screen.findByRole("button", { name: /Stage this change/ });
    expect((stage as HTMLButtonElement).disabled).toBe(true);
  });
});
