import { describe, expect, it } from "vitest";
import { SUBNET_SIZE_HINT, subnetSizeProblem, usableAddresses, WIDEST_PREFIX } from "@/lib/network-subnet";
import { newVlanRow, vlanPlanProblems } from "@/components/network/vlan-plan";

describe("client network subnet size (/19 is the widest)", () => {
  it("accepts /19 through /30 and refuses everything else, for every prefix from /8 to /32", () => {
    for (let p = 8; p <= 32; p++) {
      const problem = subnetSizeProblem(`10.20.0.0/${p}`);
      if (p < 19) expect(problem, `/${p}`).toMatch(/at most \/19/);
      else if (p > 30) expect(problem, `/${p}`).toMatch(/at least \/30/);
      else expect(problem, `/${p}`).toBeNull();
    }
  });

  it("names the capacity honestly and does not promise concurrent devices", () => {
    expect(WIDEST_PREFIX).toBe(19);
    expect(usableAddresses(19)).toBe(8190);
    expect(usableAddresses(20)).toBe(4094);
    expect(usableAddresses(24)).toBe(254);
    expect(subnetSizeProblem("10.20.0.0/18")).toMatch(/8,190 addresses/);
    expect(SUBNET_SIZE_HINT).toMatch(/not the number of devices/);
  });

  it("leaves unfinished input to the other checks", () => {
    expect(subnetSizeProblem("")).toBeNull();
    expect(subnetSizeProblem("10.20.0.0")).toBeNull();
    expect(subnetSizeProblem("10.20.0.0/")).toBeNull();
  });

  it("blocks a VLAN row whose subnet is wider than /19, and lets a /19 through", () => {
    const row = (subnet: string) => newVlanRow({
      vlan: "20", name: "Rooms", subnet, gateway: "10.20.0.1", poolStart: "10.20.0.10", poolEnd: "10.20.0.200",
    });
    const wide = row("10.20.0.0/18");
    expect(vlanPlanProblems([wide], new Set(), new Set())[wide.key]).toMatch(/at most \/19/);
    const ok = row("10.20.0.0/19");
    expect(vlanPlanProblems([ok], new Set(), new Set())[ok.key]).toBeUndefined();
  });
});
