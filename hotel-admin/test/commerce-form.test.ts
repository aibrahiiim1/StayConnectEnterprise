import { describe, it, expect } from "vitest";
import {
  SUPPORTED_RULE_TYPES,
  FORBIDDEN_RULE_TYPES,
  isSupportedRuleType,
  serializeRule,
  validateDuration,
  serializeDuration,
  validateSaleWindow,
  tiersInOrder,
  buildPublishPayload,
  type PublishFormState,
} from "@/lib/commerce-form";

describe("supported / forbidden rule types", () => {
  it("offers only the five non-PMS Phase-2 rule types", () => {
    expect([...SUPPORTED_RULE_TYPES].sort()).toEqual(
      ["AUTH_METHOD", "DATE_WINDOW", "PRIOR_PURCHASE", "SITE_NETWORK", "SUBJECT_KIND"].sort(),
    );
  });
  it("never lists a PMS/Stay-dependent rule type as supported", () => {
    for (const bad of FORBIDDEN_RULE_TYPES) {
      expect(isSupportedRuleType(bad)).toBe(false);
      expect((SUPPORTED_RULE_TYPES as readonly string[]).includes(bad)).toBe(false);
    }
  });
});

describe("serializeRule — only supported types", () => {
  it("AUTH_METHOD / SUBJECT_KIND split comma lists", () => {
    expect(serializeRule({ type: "AUTH_METHOD", methods: "account, voucher" }))
      .toEqual({ type: "AUTH_METHOD", value: { methods: ["account", "voucher"] } });
    expect(serializeRule({ type: "SUBJECT_KIND", kinds: "ACCOUNT" }))
      .toEqual({ type: "SUBJECT_KIND", value: { kinds: ["ACCOUNT"] } });
  });
  it("PRIOR_PURCHASE emits exactly one boolean", () => {
    expect(serializeRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior" }))
      .toEqual({ type: "PRIOR_PURCHASE", value: { forbids_prior: true } });
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
