// THE RULE THAT DECIDES WHICH REVISIONS A SAVE CREATES.
//
// This is where "revisions are an internal detail" is actually true or not. An operator pressing Save must
// get: a new package revision always, a new service-plan revision only when they changed a plan field, the
// existing plan revision re-pinned when they did not — and never a silent no-op, which is the failure that
// left a published 10/5 Mbps plan revision unused while guests kept getting 2 Mbps.

import { describe, it, expect } from "vitest";
import {
  changedPlanFields,
  decideSave,
  saveOutcomeMessage,
  PLAN_FIELDS,
  type PlanFields,
} from "@/lib/package-save";

const pinned: PlanFields = {
  down_kbps: 2000,
  up_kbps: 2000,
  max_concurrent_devices: 4,
  device_limit_policy: "REJECT_NEW_DEVICE",
  idle_timeout_seconds: null,
  max_continuous_session_seconds: null,
  time_quota_seconds: null,
  data_quota_bytes: 100000000,
  time_accounting_mode: "VALIDITY_WINDOW",
  speed_allocation: "PER_DEVICE",
};

describe("changedPlanFields", () => {
  it("sees no change when the form matches the pinned revision", () => {
    expect(changedPlanFields(pinned, { ...pinned })).toEqual([]);
  });

  it("treats null, undefined and empty string as the same absent value", () => {
    // An operator who never touched "idle timeout" must not publish a revision because the form returned "".
    const next = { ...pinned, idle_timeout_seconds: undefined, max_continuous_session_seconds: null };
    expect(changedPlanFields(pinned, next as PlanFields)).toEqual([]);
  });

  it("compares numerically, so 2000 and '2000' are not a change", () => {
    const next = { ...pinned, down_kbps: "2000" as unknown as number };
    expect(changedPlanFields(pinned, next)).toEqual([]);
  });

  it("names exactly the fields that moved", () => {
    const next = { ...pinned, down_kbps: 10000, up_kbps: 5000 };
    expect(changedPlanFields(pinned, next).sort()).toEqual(["down_kbps", "up_kbps"]);
  });

  it("covers every plan field an operator can edit", () => {
    for (const f of PLAN_FIELDS) {
      const next = { ...pinned, [f]: f === "speed_allocation" ? "SHARED" : 12345 } as PlanFields;
      expect(changedPlanFields(pinned, next)).toContain(f);
    }
  });
});

describe("decideSave", () => {
  const base = { planCode: "Free Internet", pinnedPlanRevisionID: "b2f84f70", currentPlan: pinned };

  it("re-pins the SAME plan revision when only commercial fields changed", () => {
    const d = decideSave({ ...base, nextPlan: { ...pinned } });
    expect(d.plan).toBeUndefined();
    expect(d.reusePlanRevisionID).toBe("b2f84f70");
    expect(d.publishesPackageRevision).toBe(true);
    expect(d.changedPlanFields).toEqual([]);
  });

  it("THE 2 Mbps CASE: a speed change publishes a plan revision AND a package revision", () => {
    // Exactly what was attempted by hand and half-completed: the plan revision existed, the package was
    // never repinned, and the guest kept the old speed. One save must now do both.
    const d = decideSave({ ...base, nextPlan: { ...pinned, down_kbps: 10000, up_kbps: 5000 } });
    expect(d.plan).toEqual({
      code: "Free Internet",
      fields: { ...pinned, down_kbps: 10000, up_kbps: 5000 },
    });
    expect(d.reusePlanRevisionID).toBeUndefined();
    expect(d.publishesPackageRevision).toBe(true);
    expect(d.changedPlanFields.sort()).toEqual(["down_kbps", "up_kbps"]);
  });

  it("publishes the plan revision under the SAME plan code, so it is a revision and not a new plan", () => {
    const d = decideSave({ ...base, nextPlan: { ...pinned, data_quota_bytes: 500000000 } });
    expect(d.plan?.code).toBe("Free Internet");
  });

  it("always publishes a package revision — save is never a silent no-op", () => {
    expect(decideSave({ ...base, nextPlan: { ...pinned } }).publishesPackageRevision).toBe(true);
    expect(decideSave({ ...base, nextPlan: { ...pinned, up_kbps: 1 } }).publishesPackageRevision).toBe(true);
  });
});

describe("saveOutcomeMessage", () => {
  it("always tells the operator existing guests are unaffected", () => {
    const msg = saveOutcomeMessage(decideSave({
      planCode: "p", pinnedPlanRevisionID: "r", currentPlan: pinned, nextPlan: { ...pinned },
    }));
    expect(msg).toContain("Existing guest access is unchanged");
    expect(msg).toContain("future grants");
  });

  it("mentions the speed/allowance update when one happened", () => {
    const msg = saveOutcomeMessage(decideSave({
      planCode: "p", pinnedPlanRevisionID: "r", currentPlan: pinned,
      nextPlan: { ...pinned, down_kbps: 10000 },
    }));
    expect(msg).toContain("speed and allowance");
  });

  it("never mentions revisions, ids or pinning", () => {
    for (const next of [{ ...pinned }, { ...pinned, down_kbps: 10000 }]) {
      const msg = saveOutcomeMessage(decideSave({
        planCode: "p", pinnedPlanRevisionID: "r", currentPlan: pinned, nextPlan: next,
      })).toLowerCase();
      for (const word of ["revision", "pin", "uuid", "id "]) expect(msg).not.toContain(word);
    }
  });
});
