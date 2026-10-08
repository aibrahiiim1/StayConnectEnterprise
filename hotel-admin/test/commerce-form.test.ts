import { describe, it, expect } from "vitest";
import {
  SUPPORTED_RULE_TYPES,
  FORBIDDEN_RULE_TYPES,
  isSupportedRuleType,
  serializeRule,
  validateRule,
  validateDuration,
  serializeDuration,
  validateSaleWindow,
  tiersInOrder,
  buildPublishPayload,
  type PublishFormState,
} from "@/lib/commerce-form";

describe("supported / forbidden rule types", () => {
  // THE PMS DIMENSIONS ARE NOW OFFERED, and that is a product decision rather than a relaxation. They were
  // withheld while the engine recognised them but could not evaluate them, because a control that silently
  // does nothing is worse than an absent one. The engine evaluates every one of them against server-pinned
  // Stay evidence and refuses when that evidence is missing, so the reason for withholding them has gone.
  it("offers the non-PMS types, the audience, AND the six approved stay dimensions", () => {
    expect([...SUPPORTED_RULE_TYPES].sort()).toEqual(
      [
        "AUTH_METHOD", "DATE_WINDOW", "PRIOR_PURCHASE", "SITE_NETWORK", "SUBJECT_KIND", "CLIENT_GROUP",
        "STAY_LENGTH", "ROOM_TYPE", "RATE_PLAN", "VIP", "TRAVEL_AGENT", "PMS_INTERFACE",
      ].sort(),
    );
  });
  it("never lists a rule type the engine cannot evaluate", () => {
    // What stays forbidden is what has no evidence behind it: a package carrying one of these would be
    // ineligible for every guest, permanently, on an immutable revision.
    for (const bad of FORBIDDEN_RULE_TYPES) {
      expect(isSupportedRuleType(bad)).toBe(false);
      expect((SUPPORTED_RULE_TYPES as readonly string[]).includes(bad)).toBe(false);
    }
    expect([...FORBIDDEN_RULE_TYPES].sort()).toEqual(["LOYALTY_TIER", "STAY_NIGHTS"]);
  });
});

describe("serializeRule — only supported types", () => {
  it("AUTH_METHOD / SUBJECT_KIND split comma lists", () => {
    expect(serializeRule({ type: "AUTH_METHOD", methods: "account, voucher" }))
      .toEqual({ type: "AUTH_METHOD", value: { methods: ["account", "voucher"] } });
    expect(serializeRule({ type: "SUBJECT_KIND", kinds: "ACCOUNT" }))
      .toEqual({ type: "SUBJECT_KIND", value: { kinds: ["ACCOUNT"] } });
  });
  it("PRIOR_PURCHASE emits exactly one boolean when nothing else was set", () => {
    expect(serializeRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior" }))
      .toEqual({ type: "PRIOR_PURCHASE", value: { forbids_prior: true } });
  });
  // THE FREE ALLOWANCE (ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §5): a window and a device switch, sent
  // only when set -- and an explicit false IS set, because the server's default for a free package is true.
  it("PRIOR_PURCHASE carries the window and the device switch, and only on forbids_prior", () => {
    expect(serializeRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "24", also_by_device: true }))
      .toEqual({ type: "PRIOR_PURCHASE", value: { forbids_prior: true, within_hours: 24, also_by_device: true } });
    expect(serializeRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "", also_by_device: false }))
      .toEqual({ type: "PRIOR_PURCHASE", value: { forbids_prior: true, also_by_device: false } });
    expect(serializeRule({ type: "PRIOR_PURCHASE", mode: "requires_prior", within_hours: "24", also_by_device: true }))
      .toEqual({ type: "PRIOR_PURCHASE", value: { requires_prior: true } });
  });
  it("CLIENT_GROUP names the groups and widens to the public only when asked", () => {
    expect(serializeRule({ type: "CLIENT_GROUP", group_ids: ["g1", "g2"], public: false }))
      .toEqual({ type: "CLIENT_GROUP", value: { group_ids: ["g1", "g2"] } });
    expect(serializeRule({ type: "CLIENT_GROUP", group_ids: [], public: true }))
      .toEqual({ type: "CLIENT_GROUP", value: { public: true } });
    expect(serializeRule({ type: "CLIENT_GROUP", group_ids: ["g1"], public: true }))
      .toEqual({ type: "CLIENT_GROUP", value: { group_ids: ["g1"], public: true } });
  });
  it("refuses an audience that names nobody, and a window outside one hour to one year", () => {
    expect(validateRule({ type: "CLIENT_GROUP", group_ids: [], public: false })).toMatch(/at least one client group/);
    expect(validateRule({ type: "CLIENT_GROUP", group_ids: [], public: true })).toBeNull();
    expect(validateRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "0" })).toMatch(/between 1 and 8760/);
    expect(validateRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "8761" })).toMatch(/between 1 and 8760/);
    expect(validateRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "1.5" })).toMatch(/whole number/);
    expect(validateRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", within_hours: "24" })).toBeNull();
    expect(validateRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior" })).toBeNull();
  });
  it("throws on a forbidden/unknown type (defensive)", () => {
    // @ts-expect-error — intentionally forcing a forbidden type
    expect(() => serializeRule({ type: "ROOM_TYPE" })).toThrow();
  });
});

describe("validateDuration — only capability-enabled modes", () => {
  it("accepts MANUAL_END", () => expect(validateDuration({ end_mode: "MANUAL_END" })).toBeNull());
  it("accepts a valid VALIDITY_WINDOW", () => expect(validateDuration({ end_mode: "VALIDITY_WINDOW", duration_seconds: 3600 })).toBeNull());
  it("rejects VALIDITY_WINDOW without a positive duration", () => {
    expect(validateDuration({ end_mode: "VALIDITY_WINDOW" })).toMatch(/positive/);
    expect(validateDuration({ end_mode: "VALIDITY_WINDOW", duration_seconds: 0 })).toMatch(/positive/);
    expect(validateDuration({ end_mode: "VALIDITY_WINDOW", duration_seconds: -5 })).toMatch(/positive/);
  });
  it("accepts a FIXED_AT with a date, rejects without", () => {
    expect(validateDuration({ end_mode: "FIXED_AT", ends_at: "2026-08-01T00:00" })).toBeNull();
    expect(validateDuration({ end_mode: "FIXED_AT" })).toMatch(/end date/);
  });
  it("rejects a PMS/checkout mode that is not representable", () => {
    // @ts-expect-error — AT_CHECKOUT is not a supported EndMode
    expect(validateDuration({ end_mode: "AT_CHECKOUT" })).toMatch(/unsupported end mode/);
  });
  it("serializeDuration emits only the mode's fields", () => {
    expect(serializeDuration({ end_mode: "MANUAL_END" })).toEqual({ end_mode: "MANUAL_END" });
    expect(serializeDuration({ end_mode: "VALIDITY_WINDOW", duration_seconds: 60 })).toEqual({ end_mode: "VALIDITY_WINDOW", duration_seconds: 60 });
  });
});

describe("validateSaleWindow", () => {
  it("rejects from >= until", () => {
    expect(validateSaleWindow("2026-08-02T00:00", "2026-08-01T00:00")).toMatch(/before/);
  });
  it("accepts from < until or a single bound", () => {
    expect(validateSaleWindow("2026-08-01T00:00", "2026-08-02T00:00")).toBeNull();
    expect(validateSaleWindow(undefined, undefined)).toBeNull();
  });
});

describe("tiersInOrder — deterministic ordering", () => {
  it("sorts ascending by order regardless of input order", () => {
    const out = tiersInOrder([{ order: 30, down_kbps: 3 }, { order: 10, down_kbps: 1 }, { order: 20, down_kbps: 2 }]);
    expect(out.map((t) => t.order)).toEqual([10, 20, 30]);
    expect(out[0].grant).toEqual({ down_kbps: 1 });
  });
});

describe("buildPublishPayload — free-only, no PMS/settlement fields", () => {
  const base: PublishFormState = {
    code: "FREEWIFI", name: "Free WiFi", service_plan_revision_id: "plan-rev-1",
    rules: [{ type: "AUTH_METHOD", methods: "account,voucher" }],
    tiers: [{ order: 10, down_kbps: 5000 }],
    duration: { end_mode: "MANUAL_END" },
  };
  it("builds a valid payload with no price/settlement/PMS keys anywhere", () => {
    const { payload, error } = buildPublishPayload(base);
    expect(error).toBeUndefined();
    const json = JSON.stringify(payload);
    for (const forbidden of ["price", "settlement", "pms", "tax", "amount", "currency"]) {
      expect(json.toLowerCase()).not.toContain(forbidden);
    }
    expect(payload!.grant_tiers).toEqual([{ order: 10, grant: { down_kbps: 5000 } }]);
    expect(payload!.eligibility_rules).toEqual([{ type: "AUTH_METHOD", value: { methods: ["account", "voucher"] } }]);
  });
  it("rejects missing code / plan / tiers / bad duration / bad window", () => {
    expect(buildPublishPayload({ ...base, code: "" }).error).toBeTruthy();
    expect(buildPublishPayload({ ...base, service_plan_revision_id: "" }).error).toBeTruthy();
    expect(buildPublishPayload({ ...base, tiers: [] }).error).toBeTruthy();
    expect(buildPublishPayload({ ...base, duration: { end_mode: "VALIDITY_WINDOW" } }).error).toMatch(/positive/);
    expect(buildPublishPayload({ ...base, visible_from: "2026-08-02T00:00", visible_until: "2026-08-01T00:00" }).error).toMatch(/before/);
  });
});
