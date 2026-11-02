import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as React from "react";
import { ConfirmDialog, ConfirmPolicyContext } from "@/components/ui/dialog";
import { useNetworkChangeStepUp } from "@/components/network/change-step-up";
import { cidrsOverlap, overlapping, suggestSubnet } from "@/lib/network-trunk";
import { vlanPlanProblems, type VlanRow } from "@/components/network/vlan-plan";

function NetworkChange({ run }: { run: (password: string) => Promise<void> }) {
  const { ask, dialog } = useNetworkChangeStepUp();
  return (
    <>
      <button type="button" onClick={() => ask("apply", run)}>open</button>
      {dialog}
    </>
  );
}

describe("client-network changes in a confirm-only console", () => {
  it("apply with one confirmation and no hidden password requirement", async () => {
    const run = vi.fn().mockResolvedValue(undefined);
    render(
      <ConfirmPolicyContext.Provider value={{ confirmOnly: true }}>
        <NetworkChange run={run} />
      </ConfirmPolicyContext.Provider>,
    );
    await userEvent.click(screen.getByRole("button", { name: "open" }));
    expect(screen.queryByLabelText(/password/i)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Apply changes" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith(""));
    expect(screen.queryByText(/Confirm your password to change/)).toBeNull();
  });

  it("keep the password where the console is not confirm-only", async () => {
    render(<NetworkChange run={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "open" }));
    expect(screen.getByLabelText(/Confirm your password/)).toBeInTheDocument();
  });
});

describe("strict actions keep their password in a confirm-only console", () => {
  it("shows the field and will not confirm without it", async () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmPolicyContext.Provider value={{ confirmOnly: true }}>
        <ConfirmDialog open onOpenChange={() => {}} title="Remove this key holder?" requirePassword strictPassword onConfirm={onConfirm} />
      </ConfirmPolicyContext.Provider>,
    );
    expect(screen.getByLabelText(/Confirm your password/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Confirm" })).toBeDisabled();
  });
});

describe("/19 suggestions and validation use CIDR overlap, not string equality", () => {
  it("knows when one subnet is inside another", () => {
    expect(cidrsOverlap("10.111.0.0/19", "10.111.0.0/24")).toBe(true);
    expect(cidrsOverlap("10.111.0.0/19", "10.111.31.0/24")).toBe(true);
    expect(cidrsOverlap("10.111.0.0/19", "10.111.32.0/24")).toBe(false);
    expect(cidrsOverlap("10.20.0.0/20", "10.20.0.0/19")).toBe(true);
    expect(overlapping("10.5.0.0/19", ["192.168.1.0/24", "10.5.16.0/20"])).toBe("10.5.16.0/20");
  });

  it("skips a /19 that holds a legacy /24 or the /20 Default Network", () => {
    expect(suggestSubnet(111, new Set(["10.111.0.0/24"]))).toBe("10.1.0.0/19");
    expect(suggestSubnet(20, ["10.20.0.0/20"])).toBe("10.1.0.0/19");
    expect(suggestSubnet(20, ["10.20.0.0/20", "10.1.8.0/24"])).toBe("10.2.0.0/19");
  });

  it("refuses a VLAN row that overlaps an existing network or another row", () => {
    const row = (key: number, vlan: string, subnet: string): VlanRow => ({
      key, vlan, name: `V${vlan}`, subnet, gateway: "10.0.0.1", poolStart: "10.0.0.10", poolEnd: "10.0.0.20",
      dnsMode: "appliance", dnsServers: "",
    } as VlanRow);
    const p = vlanPlanProblems([row(1, "30", "10.30.0.0/19"), row(2, "31", "10.30.8.0/24")], new Set(), new Set(["10.30.0.0/20"]));
    expect(p[1]).toMatch(/overlaps 10\.30\.0\.0\/20/);
    const q = vlanPlanProblems([row(1, "40", "10.40.0.0/19"), row(2, "41", "10.40.8.0/24")], new Set(), new Set());
    expect(q[2]).toMatch(/overlaps another row/);
  });
});

void React;
