// WHICH SERVICE-PLAN REVISION A SAVE PINS.
//
// The operator picks a Service Plan by name. The revision is worked out here, and the two rules that matter
// are opposites: choosing a different plan pins that plan's CURRENT revision, and leaving the choice alone
// keeps the pin exactly where it was — because advancing it is a decision the Service Plans screen asks for
// explicitly, package by package, and a package rename must never carry a technical change with it.

import { describe, it, expect } from "vitest";
import { decideSave, saveOutcomeMessage, planSummary } from "@/lib/package-save";

const gold = { plan_id: "plan-gold", current_revision_id: "rev-gold-4" };
const silver = { plan_id: "plan-silver", current_revision_id: "rev-silver-1" };

describe("decideSave", () => {
  it("keeps the existing pin when the plan is unchanged, even if the plan has moved on", () => {
    // The package is on revision 3; the plan's current is 4. Saving a name change must NOT repin it.
    const d = decideSave({
      pinnedPlanRevisionID: "rev-gold-3",
      selected: gold,
      currentPlanID: "plan-gold",
    });
    expect(d.pinPlanRevisionID).toBe("rev-gold-3");
    expect(d.planChanged).toBe(false);
    expect(d.publishesPackageRevision).toBe(true);
  });

  it("pins the newly chosen plan's CURRENT revision when the operator switches", () => {
    const d = decideSave({
      pinnedPlanRevisionID: "rev-gold-3",
      selected: silver,
      currentPlanID: "plan-gold",
    });
    expect(d.pinPlanRevisionID).toBe("rev-silver-1");
    expect(d.planChanged).toBe(true);
  });

  it("pins the chosen plan's current revision when adding, where there is no existing pin", () => {
    const d = decideSave({ pinnedPlanRevisionID: "", selected: gold });
    expect(d.pinPlanRevisionID).toBe("rev-gold-4");
    expect(d.planChanged).toBe(true);
  });

  it("always publishes a package revision — save is never a silent no-op", () => {
    expect(decideSave({ pinnedPlanRevisionID: "rev-gold-3", selected: gold, currentPlanID: "plan-gold" })
      .publishesPackageRevision).toBe(true);
    expect(decideSave({ pinnedPlanRevisionID: "rev-gold-3", selected: silver, currentPlanID: "plan-gold" })
      .publishesPackageRevision).toBe(true);
  });
});

describe("saveOutcomeMessage", () => {
  it("always tells the operator existing guests are unaffected", () => {
    const msg = saveOutcomeMessage(decideSave({
      pinnedPlanRevisionID: "r", selected: gold, currentPlanID: "plan-gold",
    }));
    expect(msg).toContain("Existing guest access is unchanged");
    expect(msg).toContain("future grants");
  });

  it("says so when the service plan changed", () => {
    const msg = saveOutcomeMessage(decideSave({
      pinnedPlanRevisionID: "r", selected: silver, currentPlanID: "plan-gold",
    }));
    expect(msg).toContain("service plan");
  });

  it("never mentions revisions, ids or pinning", () => {
    for (const args of [
      { pinnedPlanRevisionID: "r", selected: gold, currentPlanID: "plan-gold" },
      { pinnedPlanRevisionID: "r", selected: silver, currentPlanID: "plan-gold" },
    ]) {
      const msg = saveOutcomeMessage(decideSave(args)).toLowerCase();
      for (const word of ["revision", "pin", "uuid"]) expect(msg).not.toContain(word);
    }
  });
});

describe("planSummary", () => {
  it("reads the way the Product Owner asked for", () => {
    expect(planSummary({
      down_kbps: 10000, up_kbps: 5000, data_quota_bytes: 100_000_000, max_concurrent_devices: 1,
    })).toBe("10 Mbps down · 5 Mbps up · 100 MB · 1 device");
  });

  it("uses GB for large allowances and names shared speed", () => {
    expect(planSummary({ down_kbps: 2000, data_quota_bytes: 5_000_000_000, speed_allocation: "SHARED" }))
      .toBe("2 Mbps down · 5 GB · shared speed");
  });

  it("describes a day-long time allowance in days", () => {
    expect(planSummary({ time_quota_seconds: 86400 })).toBe("1 day");
  });

  it("says something honest when a plan sets no limits", () => {
    expect(planSummary({})).toBe("no limits set");
  });

  it("never emits an id", () => {
    const s = planSummary({ down_kbps: 10000, max_concurrent_devices: 2 });
    expect(s).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}/);
  });
});
