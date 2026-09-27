import { describe, it, expect } from "vitest";
import {
  REASON_CHOICES,
  REASON_CODE_PATTERN,
  changedTerms,
  describePublishFailure,
  draftFromTerms,
  draftToTerms,
  fmtData,
  fmtDuration,
  graceWarnings,
  guestReceivesSentence,
  normaliseReasonCode,
  validateDraft,
} from "@/lib/api/checkout-grace";

// The server's pattern, restated here on purpose: if edged's graceReasonCode ever changes, this test must be
// revisited rather than silently following the client constant.
const SERVER = /^[A-Z][A-Z0-9_]{0,63}$/;

describe("reason codes", () => {
  it("the client pattern is the server's", () => {
    expect(REASON_CODE_PATTERN.source).toBe(SERVER.source);
  });

  it("every preset reason is a valid code", () => {
    for (const r of REASON_CHOICES) expect(r.code).toMatch(SERVER);
  });

  it.each([
    ["LATE CHECKOUT", "LATE_CHECKOUT"],
    ["late checkout", "LATE_CHECKOUT"],
    ["  late   checkout  ", "LATE_CHECKOUT"],
    ["late-checkout/season", "LATE_CHECKOUT_SEASON"],
    ["2nd try", "ND_TRY"],
    ["Über café!", "UBER_CAFE"],
    ["__x__", "X"],
  ])("normalises %j to %j", (input, out) => {
    expect(normaliseReasonCode(input)).toBe(out);
  });

  it.each(["", "   ", "123", "!!!", "___", "٣٤٥"])("yields nothing usable for %j", (input) => {
    expect(normaliseReasonCode(input)).toBe("");
  });

  it("never produces an invalid code, whatever is typed", () => {
    const samples = ["a", "A b C", "x".repeat(200), "é".repeat(70), "9lives", "tab\tsep", "UPPER_lower 42", "-_-"];
    for (const s of samples) {
      const c = normaliseReasonCode(s);
      expect(c === "" || SERVER.test(c), `${JSON.stringify(s)} → ${c}`).toBe(true);
      expect(c.length).toBeLessThanOrEqual(64);
    }
  });
});

describe("the guest sentence", () => {
  it("is built from every field", () => {
    const s = guestReceivesSentence({
      grace_duration_seconds: 5400,
      grace_down_kbps: 2500,
      grace_up_kbps: 800,
      grace_data_quota_bytes: 1536 * 1024 * 1024,
      grace_device_limit: 3,
      grace_device_limit_policy: "REJECT_NEW_DEVICE",
    });
    expect(s).toBe(
      "A guest who still has internet access when they check out keeps it for 1 hour 30 minutes after checkout, " +
        "at up to 2.5 Mbps down and 800 kbps up, with 1.5 GB of data. Devices already connected at checkout stay " +
        "online; new devices are refused.",
    );
  });

  it("says so when there is no data allowance instead of printing zero", () => {
    expect(guestReceivesSentence({ grace_duration_seconds: 60, grace_down_kbps: 1000, grace_up_kbps: 1000, grace_data_quota_bytes: 0 })).toMatch(
      /with no data allowance\./,
    );
  });
});

describe("formatting", () => {
  it("durations and data", () => {
    expect(fmtDuration(3600)).toBe("1 h");
    expect(fmtDuration(1800)).toBe("30 min");
    expect(fmtDuration(604800)).toBe("7 d");
    expect(fmtData(500 * 1024 * 1024)).toBe("500 MB");
    expect(fmtData(1024 * 1024 * 1024)).toBe("1 GB");
  });
});

describe("the draft", () => {
  it("round-trips the terms in force, and lifts the fallback's zero device limit to 1", () => {
    const d = draftFromTerms(
      { grace_duration_seconds: 7200, grace_down_kbps: 5000, grace_up_kbps: 2000, grace_data_quota_bytes: 500 * 1024 * 1024, grace_device_limit: 0, grace_device_limit_policy: "REJECT_NEW_DEVICE" },
      ["REJECT_NEW_DEVICE"],
    );
    expect(d.durationValue).toBe("2");
    expect(d.durationUnit).toBe("h");
    expect(d.deviceLimit).toBe("1");
    const t = draftToTerms(d);
    expect(t.grace_duration_seconds).toBe(7200);
    expect(t.grace_down_kbps).toBe(5000);
    expect(t.eligibility_window_seconds).toBe(86400);
    expect(validateDraft(d, ["REJECT_NEW_DEVICE"])).toEqual({});
  });

  it("refuses empty, zero and fractional-device drafts", () => {
    const d = draftFromTerms({}, ["REJECT_NEW_DEVICE"]);
    const e = validateDraft({ ...d, durationValue: "", downMbps: "0", deviceLimit: "1.5", allowanceMb: "0.5" }, ["REJECT_NEW_DEVICE"]);
    expect(Object.keys(e).sort()).toEqual(["allowanceMb", "deviceLimit", "downMbps", "durationValue"]);
  });

  it("refuses a device policy the appliance does not support", () => {
    const d = draftFromTerms({}, ["REJECT_NEW_DEVICE"]);
    expect(validateDraft({ ...d, devicePolicy: "DISCONNECT_OLDEST" }, ["REJECT_NEW_DEVICE"]).devicePolicy).toBeTruthy();
  });
});

describe("history diffs", () => {
  it("lists only the fields that changed", () => {
    const c = changedTerms(
      { grace_duration_seconds: 3600, grace_down_kbps: 5000 },
      { grace_duration_seconds: 1800, grace_down_kbps: 5000 },
    );
    expect(c.map((x) => [x.label, x.from, x.to])).toEqual([["Grace time", "1 h", "30 min"]]);
  });
});

describe("publish failures in plain words", () => {
  it.each([
    [{ status: 409, code: "version_conflict" }, /Someone else published a newer policy/],
    [{ status: 401, code: "reauth_required" }, /password was not accepted/],
    [{ status: 400, code: "reason_invalid" }, /reason was not accepted/],
    [{ status: 403, code: "actor_invalid" }, /not allowed to publish/],
    [{ status: 400, code: "package_invalid" }, /could not build a grace package/],
    [{ status: 400, code: "policy_unsupported" }, /not supported/],
    [{ status: 503, code: "phase2_disabled" }, /package catalogue is not available/],
    [
      { status: 400, code: "validation", message: "derived grace package does not satisfy the checkout validator: plan_mismatch" },
      /could not build a grace package.*plan_mismatch/,
    ],
  ])("%j", (e, re) => {
    const f = describePublishFailure(Object.assign(new Error((e as any).message ?? "x"), e));
    expect(f.message).toMatch(re);
    expect(f.conflict).toBe((e as any).status === 409);
  });
});

describe("warnings", () => {
  const eff = {
    source: "PUBLISHED" as const,
    duration_seconds: 1800,
    down_kbps: 1000,
    up_kbps: 1000,
    data_quota_bytes: 1,
    device_limit: 1,
    device_limit_policy: "REJECT_NEW_DEVICE",
    config_version: 5,
  };
  it("a version in force that the record does not know about is named", () => {
    const w = graceWarnings(
      { published: true, config_version: 5, effective: eff },
      [{ config_version: 4, published_at: "2026-09-01T00:00:00Z", actor: "a", reason_code: "X", policy: null }],
      true,
    );
    expect(w.map((x) => x.id)).toEqual(["version-not-in-history"]);
  });
  it("a healthy published policy has no warnings", () => {
    const w = graceWarnings(
      { published: true, config_version: 5, effective: eff, emergency_history: { count: 0 } },
      [{ config_version: 5, published_at: "2026-09-01T00:00:00Z", actor: "a", reason_code: "X", policy: null }],
      true,
    );
    expect(w).toEqual([]);
  });
});
