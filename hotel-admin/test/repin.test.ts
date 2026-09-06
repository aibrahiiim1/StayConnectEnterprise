// MOVING A STALE PACKAGE ONTO ITS PLAN'S CURRENT SETTINGS.
//
// The real state this exists for: service plan "Free Internet" is on revision 4 (10/5 Mbps) and the "Freee"
// package still pins revision 3 (2/2 Mbps). The operator could see that and had no way to act on it without
// publishing yet another plan revision, purely to be offered the repin prompt.

import { describe, it, expect } from "vitest";
import {
  stalePackagesFor, repinPayload, repinOutcomeMessage,
  type PlanRef, type PackageRef, type PackageCurrentDTO,
} from "@/lib/repin";

const freeInternet: PlanRef = {
  plan_id: "plan-free", code: "Free Internet", name: "Free Internet", current_revision_id: "rev4",
};

const freee: PackageRef = {
  package_id: "pkg-freee", code: "Freee", name: "Free internet", active: true,
  service_plan_id: "plan-free", service_plan_revision_id: "rev3",
};
const upToDate: PackageRef = {
  package_id: "pkg-current", code: "Current", active: true,
  service_plan_id: "plan-free", service_plan_revision_id: "rev4",
};

describe("stalePackagesFor", () => {
  it("detects the real case: a package pinned to an older revision of its plan", () => {
    expect(stalePackagesFor(freeInternet, [freee, upToDate]).map((p) => p.package_id))
      .toEqual(["pkg-freee"]);
  });

  it("does NOT offer a package already on the current revision", () => {
    // Republishing it would add an immutable revision that changes nothing.
    expect(stalePackagesFor(freeInternet, [upToDate])).toEqual([]);
  });

  it("ignores packages belonging to a different service plan", () => {
    const other: PackageRef = {
      package_id: "pkg-other", code: "OneDay", active: true,
      service_plan_id: "plan-oneday", service_plan_revision_id: "revX",
    };
    expect(stalePackagesFor(freeInternet, [other])).toEqual([]);
  });

  it("ignores disabled packages — they are offered to nobody", () => {
    expect(stalePackagesFor(freeInternet, [{ ...freee, active: false }])).toEqual([]);
  });

  it("ignores a package with no pin at all rather than guessing one", () => {
    expect(stalePackagesFor(freeInternet, [{ ...freee, service_plan_revision_id: null }])).toEqual([]);
  });

  it("offers nothing when the plan itself has no current revision", () => {
    expect(stalePackagesFor({ ...freeInternet, current_revision_id: "" }, [freee])).toEqual([]);
  });
});

describe("repinPayload", () => {
  const cur: PackageCurrentDTO = {
    code: "Freee",
    display: { name: "Free internet" },
    duration_policy: { end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 },
    eligibility_rules: [{ type: "AUTH_METHOD", value: { methods: ["pms"] } }],
    grant_tiers: [{ order: 10, value: { down_kbps: 5000 } }],
    visible_from: "2026-01-01T00:00:00Z",
    visible_until: null,
  };

  it("changes ONLY the pinned service-plan revision", () => {
    const p = repinPayload(cur, "rev4");
    expect(p.service_plan_revision_id).toBe("rev4");
    expect(p.code).toBe("Freee");
    expect(p.display).toEqual({ name: "Free internet" });
    expect(p.duration_policy).toEqual({ end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 });
    expect(p.visible_from).toBe("2026-01-01T00:00:00Z");
    expect(p.visible_until).toBeUndefined();
  });

  it("carries eligibility rules and grant tiers across unchanged", () => {
    const p = repinPayload(cur, "rev4");
    expect(p.eligibility_rules).toEqual([{ type: "AUTH_METHOD", value: { methods: ["pms"] } }]);
    expect(p.grant_tiers).toEqual([{ order: 10, grant: { down_kbps: 5000 } }]);
  });

  it("accepts the capitalised keys Go marshals, so nothing is dropped by spelling", () => {
    const goShape: PackageCurrentDTO = {
      code: "Freee",
      eligibility_rules: [{ Type: "SUBJECT_KIND", Value: { kinds: ["ACCOUNT"] } }],
      grant_tiers: [{ Order: 5, Value: { up_kbps: 1000 } }],
    };
    const p = repinPayload(goShape, "rev4");
    expect(p.eligibility_rules).toEqual([{ type: "SUBJECT_KIND", value: { kinds: ["ACCOUNT"] } }]);
    expect(p.grant_tiers).toEqual([{ order: 5, grant: { up_kbps: 1000 } }]);
  });

  it("NEVER emits anything that would author a service plan", () => {
    const raw = JSON.stringify(repinPayload(cur, "rev4"));
    for (const planField of [
      "down_kbps\":", "up_kbps\":", "data_quota_bytes", "time_quota_seconds",
      "max_concurrent_devices", "device_limit_policy", "speed_allocation", "time_accounting_mode",
    ]) {
      // grant tiers legitimately carry down_kbps/up_kbps INSIDE grant, so check the top level only.
      const top = JSON.parse(raw);
      expect(Object.keys(top)).not.toContain(planField.replace('":', ""));
    }
    // and the only revision it names is the one it is pinning to
    expect(raw.match(/rev4/g)?.length).toBe(1);
  });

  it("falls back to a readable name only when the package has no display", () => {
    expect(repinPayload({ code: "X" }, "rev4", "Nice name").display).toEqual({ name: "Nice name" });
    expect(repinPayload({ code: "X" }, "rev4").display).toEqual({ name: "X" });
  });
});

describe("repinOutcomeMessage", () => {
  it("says existing guests are unaffected when packages were updated", () => {
    expect(repinOutcomeMessage(2)).toContain("2 packages updated");
    expect(repinOutcomeMessage(2)).toContain("Existing guest access is unchanged");
    expect(repinOutcomeMessage(1)).toContain("1 package updated");
  });

  it("is honest when nothing was selected", () => {
    expect(repinOutcomeMessage(0)).toContain("Nothing was changed");
  });
});
