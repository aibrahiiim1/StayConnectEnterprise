import { describe, it, expect } from "vitest";
import { serviceStateOf, splitByState, topologyWords, isServing } from "@/lib/network-state";
import { eligibilityWords } from "@/lib/package-eligibility";

describe("one reading of where a client network stands", () => {
  it("serving, switched off and archived, with archived winning over a stale enabled flag", () => {
    expect(serviceStateOf({ enabled: true })).toBe("serving");
    expect(serviceStateOf({ enabled: false })).toBe("off");
    expect(serviceStateOf({ enabled: true, archived: true })).toBe("archived");
    expect(serviceStateOf({ enabled: false, archived: true })).toBe("archived");
    expect(serviceStateOf({ enabled: true, service_state: "off" })).toBe("off");
    expect(serviceStateOf({})).toBe("serving");
  });

  it("splits a list so only serving networks are choices", () => {
    const g = splitByState([{ enabled: true }, { enabled: false }, { archived: true, enabled: false }, { service_state: "serving" as const }]);
    expect([g.serving.length, g.off.length, g.archived.length]).toEqual([2, 1, 1]);
    expect(isServing({ enabled: false })).toBe(false);
  });

  it("names a native network and a tagged VLAN by how clients reach them, not by the port", () => {
    expect(topologyWords({ network_type: "untagged", parent_interface: "ens192" }).label).toBe("Untagged (native)");
    expect(topologyWords({ network_type: "untagged", parent_interface: "ens192" }).detail).toContain("native VLAN");
    expect(topologyWords({ network_type: "vlan", vlan_id: 60, parent_interface: "ens192" }).label).toBe("VLAN 60");
  });
});

describe("what ties a package to a place", () => {
  const names = { networks: new Map([["n1", "Rooms"]]), connections: new Map([["c1", "Aqua"]]) };
  it("spells out networks and PMS connections and counts the rest", () => {
    expect(eligibilityWords([], names)).toEqual([]);
    expect(eligibilityWords([
      { type: "SITE_NETWORK", value: { guest_network_ids: ["n1", "gone"] } },
      { type: "PMS_INTERFACE", value: { pms_interface_ids: ["c1"] } },
      { Type: "VIP", Value: { is_vip: true } },
      { type: "STAY_LENGTH", value: {} },
    ], names)).toEqual(["Only on Rooms, a removed network", "Only guests of Aqua", "2 other conditions"]);
  });
});
