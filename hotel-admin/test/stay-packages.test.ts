// STAY-LENGTH TIERS AND THE PER-NIGHT ALLOWANCE, as the Product Owner specified them.
//
//   1–7 nights → Package A, 8–10 → Package B, 11+ → Package C
//   1 GB a night, at least 5 GB, at most 20 GB
//
// The preview arithmetic here must agree with the server's exactly. It is duplicated deliberately — the
// operator has to see the result before publishing an immutable revision — so the same worked example is
// asserted on both sides, and a divergence fails a test rather than a guest's entitlement.

import { describe, it, expect } from "vitest";
import {
  readStayLength, rangesOverlap, describeRange, stayLengthWarnings,
  allocationFromPolicy, validateAllocation, serializeAllocation, previewAllocation,
  type PackageStayRange,
} from "@/lib/stay-packages";
import { serializeRule, SUPPORTED_RULE_TYPES, FORBIDDEN_RULE_TYPES, isPMSRuleType } from "@/lib/commerce-form";

describe("the approved stay-length tiers", () => {
  const tiers: PackageStayRange[] = [
    { package_id: "a", name: "Package A", active: true, range: { min: 1, max: 7 } },
    { package_id: "b", name: "Package B", active: true, range: { min: 8, max: 10 } },
    { package_id: "c", name: "Package C", active: true, range: { min: 11 } },
  ];

  it("does not warn about the approved 1–7 / 8–10 / 11+ split", () => {
    expect(stayLengthWarnings(tiers)).toEqual([]);
  });

  it("describes each tier the way an operator wrote it", () => {
    expect(describeRange({ min: 1, max: 7 })).toBe("1–7 nights");
    expect(describeRange({ min: 11 })).toBe("11+ nights");
    expect(describeRange({ max: 7 })).toBe("up to 7 nights");
    expect(describeRange({ min: 1, max: 1 })).toBe("1 night");
  });

  it("warns when two enabled packages claim the same nights, naming both and what will happen", () => {
    const w = stayLengthWarnings([...tiers,
      { package_id: "d", name: "Package D", active: true, range: { min: 5, max: 9 } }]);
    // D overlaps both A (1–7) and B (8–10).
    expect(w).toHaveLength(2);
    expect(w[0]).toContain("Package A");
    expect(w[0]).toContain("Package D");
    expect(w[0]).toContain("asked to choose");
  });

  it("does NOT warn about a disabled package — no guest can reach it", () => {
    expect(stayLengthWarnings([...tiers,
      { package_id: "d", name: "Off", active: false, range: { min: 1, max: 30 } }])).toEqual([]);
  });

  it("ignores packages with no stay-length rule at all", () => {
    expect(stayLengthWarnings([...tiers,
      { package_id: "e", name: "Everyone", active: true, range: null }])).toEqual([]);
  });

  it("treats open-ended ranges as reaching infinity when checking overlap", () => {
    expect(rangesOverlap({ min: 11 }, { min: 8, max: 10 })).toBe(false);
    expect(rangesOverlap({ min: 11 }, { min: 8, max: 12 })).toBe(true);
    expect(rangesOverlap({ max: 7 }, { min: 7 })).toBe(true); // both claim night 7
  });

  it("reads the bounds back out of stored rules, and ignores a rule that sets neither", () => {
    expect(readStayLength([{ type: "STAY_LENGTH", value: { min_nights: 8, max_nights: 10 } }]))
      .toEqual({ min: 8, max: 10 });
    expect(readStayLength([{ type: "STAY_LENGTH", value: { min_nights: 11 } }])).toEqual({ min: 11 });
    expect(readStayLength([{ type: "AUTH_METHOD", value: {} }])).toBeNull();
    expect(readStayLength([{ type: "STAY_LENGTH", value: {} }])).toBeNull();
  });
});

describe("the PMS eligibility dimensions are selectable and serialize correctly", () => {
  it("offers every dimension the Product Owner asked for", () => {
    for (const t of ["STAY_LENGTH", "ROOM_TYPE", "RATE_PLAN", "VIP", "TRAVEL_AGENT", "PMS_INTERFACE"]) {
      expect(SUPPORTED_RULE_TYPES as readonly string[]).toContain(t);
      expect(isPMSRuleType(t)).toBe(true);
    }
    // ...and still refuses the ones the engine cannot answer, which would make a package eligible for nobody.
    expect(FORBIDDEN_RULE_TYPES as readonly string[]).toContain("LOYALTY_TIER");
    for (const f of FORBIDDEN_RULE_TYPES) {
      expect(SUPPORTED_RULE_TYPES as readonly string[]).not.toContain(f);
    }
  });

  it("omits an empty stay-length bound instead of sending zero", () => {
    // {min_nights: 0} reads as "at least zero nights" — a rule that constrains nothing while looking like one
    // that does, which is how an 11+ package silently becomes available to everybody.
    expect(serializeRule({ type: "STAY_LENGTH", min_nights: "11", max_nights: "" }))
      .toEqual({ type: "STAY_LENGTH", value: { min_nights: 11 } });
    expect(serializeRule({ type: "STAY_LENGTH", min_nights: "", max_nights: "7" }))
      .toEqual({ type: "STAY_LENGTH", value: { max_nights: 7 } });
    expect(serializeRule({ type: "STAY_LENGTH", min_nights: "1", max_nights: "7" }))
      .toEqual({ type: "STAY_LENGTH", value: { min_nights: 1, max_nights: 7 } });
  });

  it("serializes the other five dimensions in the shapes the engine reads", () => {
    expect(serializeRule({ type: "ROOM_TYPE", room_types: "DLX, SUITE" }))
      .toEqual({ type: "ROOM_TYPE", value: { room_types: ["DLX", "SUITE"] } });
    expect(serializeRule({ type: "RATE_PLAN", rate_plans: "BAR" }))
      .toEqual({ type: "RATE_PLAN", value: { rate_plans: ["BAR"] } });
    expect(serializeRule({ type: "VIP", is_vip: "true" }))
      .toEqual({ type: "VIP", value: { is_vip: true } });
    expect(serializeRule({ type: "VIP", is_vip: "false" }))
      .toEqual({ type: "VIP", value: { is_vip: false } });
    expect(serializeRule({ type: "TRAVEL_AGENT", travel_agents: "EXPEDIA" }))
      .toEqual({ type: "TRAVEL_AGENT", value: { travel_agents: ["EXPEDIA"] } });
    expect(serializeRule({ type: "PMS_INTERFACE", pms_interface_ids: "ddff5d07-f588-4f1f-8133-a0f393524476" }))
      .toEqual({ type: "PMS_INTERFACE", value: { pms_interface_ids: ["ddff5d07-f588-4f1f-8133-a0f393524476"] } });
  });
});

describe("the per-night data allowance", () => {
  const approved = { mode: "PER_STAY_NIGHT" as const, gb_per_night: "1", min_gb: "5", max_gb: "20" };

  it("previews the Product Owner's worked example exactly", () => {
    expect(previewAllocation(approved, [2, 5, 8, 10, 12, 25])).toEqual([
      { nights: 2, gb: 5 },   // below the floor
      { nights: 5, gb: 5 },   // on the floor
      { nights: 8, gb: 8 },
      { nights: 10, gb: 10 },
      { nights: 12, gb: 12 },
      { nights: 25, gb: 20 }, // capped
    ]);
  });

  it("sends the policy the server expects, and sends NOTHING for a fixed allowance", () => {
    expect(serializeAllocation(approved))
      .toEqual({ mode: "PER_STAY_NIGHT", gb_per_night: 1, min_gb: 5, max_gb: 20 });
    // FIXED publishes no policy at all, so the revision is byte-identical to one published before this
    // feature existed.
    expect(serializeAllocation({ mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" })).toBeUndefined();
    // An omitted ceiling is omitted, not zero.
    expect(serializeAllocation({ ...approved, max_gb: "" }))
      .toEqual({ mode: "PER_STAY_NIGHT", gb_per_night: 1, min_gb: 5 });
  });

  it("refuses the combinations that cannot mean anything", () => {
    expect(validateAllocation(approved)).toBeNull();
    expect(validateAllocation({ ...approved, gb_per_night: "" })).toMatch(/how many GB/i);
    expect(validateAllocation({ ...approved, gb_per_night: "0" })).toMatch(/how many GB/i);
    expect(validateAllocation({ ...approved, min_gb: "30", max_gb: "20" })).toMatch(/below the minimum/i);
    expect(validateAllocation({ ...approved, max_gb: "0" })).toMatch(/greater than zero/i);
    // A per-night rate on its own is legitimate: no floor and no ceiling is a valid product.
    expect(validateAllocation({ mode: "PER_STAY_NIGHT", gb_per_night: "2", min_gb: "", max_gb: "" })).toBeNull();
    // FIXED is never validated against per-night fields.
    expect(validateAllocation({ mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" })).toBeNull();
  });

  it("reads an existing package's policy back into the form, and an absent one as FIXED", () => {
    expect(allocationFromPolicy({ mode: "PER_STAY_NIGHT", gb_per_night: 1, min_gb: 5, max_gb: 20 }))
      .toEqual(approved);
    for (const absent of [null, undefined, {}, { mode: "FIXED" }]) {
      expect(allocationFromPolicy(absent).mode).toBe("FIXED");
    }
  });

  it("previews nothing while the numbers are incomplete, rather than showing a wrong answer", () => {
    expect(previewAllocation({ ...approved, gb_per_night: "" }, [5])).toEqual([]);
    expect(previewAllocation({ mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" }, [5])).toEqual([]);
  });
});
