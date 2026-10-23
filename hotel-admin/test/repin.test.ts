
import { describe, it, expect } from "vitest";
import {
  stalePackagesFor, repinPayload, repinOutcomeMessage, repinSkippedMessage, commercialTermsOf,
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
      // Grant tiers may contain speed fields; inspect only the package's top level.
      const top = JSON.parse(raw);
      expect(Object.keys(top)).not.toContain(planField.replace('":', ""));
    }
    expect(raw.match(/rev4/g)?.length).toBe(1);
  });

  it("carries a per-night data allowance across a repin instead of flattening it", () => {
    const perNight = { ...cur, data_allocation_policy: { mode: "PER_STAY_NIGHT", gb_per_night: 1, min_gb: 5 } };
    expect(repinPayload(perNight, "rev4").data_allocation_policy)
      .toEqual({ mode: "PER_STAY_NIGHT", gb_per_night: 1, min_gb: 5 });
    expect("data_allocation_policy" in repinPayload(cur, "rev4")).toBe(false);
    expect("data_allocation_policy" in repinPayload({ ...cur, data_allocation_policy: {} }, "rev4")).toBe(false);
  });

  it("falls back to a readable name only when the package has no display", () => {
    expect(repinPayload({ code: "X" }, "rev4", "Nice name").display).toEqual({ name: "Nice name" });
    expect(repinPayload({ code: "X" }, "rev4").display).toEqual({ name: "X" });
  });
});

// Preserve commercial fields when repinning; omitted values default to a free package.
describe("repinPayload — the package's commercial terms travel with it", () => {
  const card: PackageCurrentDTO = {
    code: "DAY",
    display: { name: "Day pass", description: "24 hours", badge: "Popular" },
    duration_policy: { end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 },
    eligibility_rules: [{ Type: "STAY_STATUS", Value: { statuses: ["IN_HOUSE"] } }],
    grant_tiers: [{ Order: 10, Value: { down_kbps: 5000, match: { type: "VIP", value: { is_vip: true } } } }],
    price_minor: 1500, currency: "eur", currency_exponent: 2,
    settlement_methods: ["ONLINE_PAYMENT", "PMS_POSTING", "PREPAID"],
    room_charge_mappings: [{ pms_interface_id: "if-1", posting_code: "WIFI", tax_code: null, tax_rate_bp: 1400 }],
    package_type: "STAY_BUNDLE", plan_overrides: { max_concurrent_devices: 3 }, renewable: true, max_purchases_per_stay: 2,
  };

  it("carries price, currency, exponent, methods and room-charge mappings exactly", () => {
    const p = repinPayload(card, "rev4") as Record<string, unknown>;
    expect(p.price_minor).toBe(1500);
    expect(p.currency).toBe("EUR");
    expect(p.currency_exponent).toBe(2);
    expect(p.acquisition_methods).toEqual(["ONLINE_PAYMENT", "PMS_POSTING", "PREPAID"]);
    expect(p.room_charge_mappings).toEqual([{ pms_interface_id: "if-1", posting_code: "WIFI", tax_rate_bp: 1400 }]);
  });

  it("carries the fields no screen edits, the whole display, unknown rules and tier match keys", () => {
    const p = repinPayload(card, "rev4") as Record<string, unknown>;
    expect(p).toMatchObject({
      package_type: "STAY_BUNDLE", plan_overrides: { max_concurrent_devices: 3 }, renewable: true, max_purchases_per_stay: 2,
    });
    expect(p.display).toEqual({ name: "Day pass", description: "24 hours", badge: "Popular" });
    expect(p.eligibility_rules).toEqual([{ type: "STAY_STATUS", value: { statuses: ["IN_HOUSE"] } }]);
    expect(p.grant_tiers).toEqual([{ order: 10, grant: { down_kbps: 5000, match: { type: "VIP", value: { is_vip: true } } } }]);
  });

  it("states a Free legacy revision the way the server stores it, and null mappings as none", () => {
    const { terms, missing } = commercialTermsOf({ code: "F", price_minor: 0, currency: "", settlement_methods: null, room_charge_mappings: null });
    expect(missing).toEqual([]);
    expect(terms).toEqual({
      price_minor: 0, currency: "USD", currency_exponent: 2, acquisition_methods: ["NOT_REQUIRED"], room_charge_mappings: [],
    });
  });

  it("guesses nothing for a priced package whose terms are incomplete, and names what is missing", () => {
    expect(commercialTermsOf({ code: "P", price_minor: 900, currency: "", settlement_methods: [], room_charge_mappings: null }).missing)
      .toEqual(["currency", "currency_exponent", "acquisition_methods"]);
    expect(commercialTermsOf({ code: "P", currency: "USD", settlement_methods: ["PREPAID"], room_charge_mappings: [] }).missing)
      .toEqual(["price_minor"]);
    // Missing mappings mean incomplete data; null explicitly means no mappings.
    expect(commercialTermsOf({ code: "P", price_minor: 0, currency: "USD", settlement_methods: ["NOT_REQUIRED"] }).missing)
      .toEqual(["room_charge_mappings"]);
    const p = repinPayload({ code: "P", price_minor: 900 }, "rev4") as Record<string, unknown>;
    expect("price_minor" in p).toBe(false);
  });

  it("a currency this build does not list keeps the exponent it was published with", () => {
    const { terms } = commercialTermsOf({ code: "X", price_minor: 100, currency: "CHF", currency_exponent: 2, settlement_methods: ["PREPAID"], room_charge_mappings: [] });
    expect(terms?.currency_exponent).toBe(2);
    expect(commercialTermsOf({ code: "X", price_minor: 100, currency: "CHF", settlement_methods: ["PREPAID"], room_charge_mappings: [] }).missing)
      .toEqual(["currency_exponent"]);
  });

  it("names the packages it did not move", () => {
    expect(repinSkippedMessage([])).toBeNull();
    expect(repinSkippedMessage([{ name: "Day pass", missing: ["currency"] }])).toContain("Not moved: Day pass (missing currency)");
  });
});

describe("repinOutcomeMessage", () => {
  it("says existing guests are unaffected when packages were updated", () => {
    expect(repinOutcomeMessage(2)).toContain("2 packages updated");
    expect(repinOutcomeMessage(2)).toContain("Existing client access is unchanged");
    expect(repinOutcomeMessage(1)).toContain("1 package updated");
  });

  it("is honest when nothing was selected", () => {
    expect(repinOutcomeMessage(0)).toContain("Nothing was changed");
  });
});
