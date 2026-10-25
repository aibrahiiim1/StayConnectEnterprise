import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() },
}));

import { NetworkStatusBadge, PendingChangeNote, DiagnosticsPanel } from "@/components/network/status";
import { STATUS_LABEL, type NetworkStatusValue, type NetworkDiagnostics } from "@/lib/api/network-overview";

const vlan60: NetworkDiagnostics = {
  network: {
    network_id: "n60",
    name: "VLAN 60",
    checks: [
      { id: "port_link", label: "Port link", state: "ok", detail: "ens192 has link (up)", where: "appliance" },
      { id: "frames", label: "Frames delivered to the interface", state: "fail", detail: "0 frames received and 18 sent on ens192.60 since it was created", where: "appliance" },
      { id: "dns", label: "DNS resolver", state: "unknown", detail: "the socket table could not be read", where: "appliance" },
    ],
    findings: ["No frames were delivered to ens192.60 by this appliance's network stack. That does not show where they were lost."],
    outside: ["The switch port facing this appliance allows VLAN 60 (trunk allowed list)."],
  },
  server: [{ id: "kea_file", label: "DHCP configuration saved for a restart", state: "fail", detail: "the file differs", where: "appliance" }],
};

beforeEach(() => {
  get.mockReset();
});

describe("network status", () => {
  it("words every status the appliance can derive, and the reason is the tooltip", () => {
    const all = Object.keys(STATUS_LABEL) as NetworkStatusValue[];
    expect(all).toHaveLength(7);
    for (const s of all) {
      const { unmount } = render(<NetworkStatusBadge status={s} reason={`why ${s}`} />);
      expect(screen.getByText(STATUS_LABEL[s])).toBeTruthy();
      expect(screen.getByTitle(`why ${s}`)).toBeTruthy();
      unmount();
    }
  });

  it("says which saved settings are not applied", () => {
    render(<PendingChangeNote changes={[{ field: "gateway_ip", running: "10.0.0.1", saved: "10.0.0.254" }, { field: "pools", running: "a", saved: "b" }]} />);
    expect(screen.getByText(/Not applied: Gateway address, Address pools/)).toBeTruthy();
  });

  it("shows nothing when nothing is pending", () => {
    const { container } = render(<PendingChangeNote changes={[]} />);
    expect(container.textContent).toBe("");
  });
});

describe("diagnostics panel", () => {
  it("runs nothing until asked, then shows what was observed and what cannot be observed", async () => {
    get.mockResolvedValue(vlan60);
    render(<DiagnosticsPanel networkId="n60" />);
    expect(get).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Run checks/ }));
    await waitFor(() => expect(screen.getByText(/does not show where they were lost/)).toBeTruthy());
    expect(get).toHaveBeenCalledWith("/network/guest-networks/n60/diagnostics");
    expect(screen.getAllByText("Problem")).toHaveLength(2);
    expect(screen.getByText("Could not be read")).toBeTruthy();
    expect(screen.getByText("Check outside the appliance")).toBeTruthy();
    expect(screen.getByText(/places to look, not findings/)).toBeTruthy();
    expect(screen.getByText(/switch port facing this appliance allows VLAN 60/)).toBeTruthy();
    expect(screen.getByText("DHCP configuration saved for a restart")).toBeTruthy();
  });

  it("can watch the frames for five seconds", async () => {
    get.mockResolvedValue(vlan60);
    render(<DiagnosticsPanel networkId="n60" />);
    fireEvent.click(screen.getByRole("button", { name: /Watch frames for 5 seconds/ }));
    await waitFor(() => expect(get).toHaveBeenCalledWith("/network/guest-networks/n60/diagnostics?sample_ms=5000"));
  });

  it("does not invent checks for a network that is not running yet", async () => {
    get.mockResolvedValue({ network: { network_id: "n", name: "x", checks: [], findings: [], outside: [] }, server: [], not_running: true });
    render(<DiagnosticsPanel networkId="n" />);
    fireEvent.click(screen.getByRole("button", { name: /Run checks/ }));
    await waitFor(() => expect(screen.getByText("Not part of the confirmed configuration yet")).toBeTruthy());
    expect(screen.queryByText("Network checks")).toBeNull();
  });

  it("says so when the appliance could not be asked", async () => {
    get.mockRejectedValue(new Error("the appliance's network service did not answer, so nothing could be observed"));
    render(<DiagnosticsPanel networkId="n" />);
    fireEvent.click(screen.getByRole("button", { name: /Run checks/ }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/nothing could be observed/));
  });
});
