import { describe, expect, it } from "vitest";
import { licenseState, statusWord } from "@/lib/license-state";
import type { License } from "@/lib/api";

const day = 86400000;
const now = Date.parse("2026-09-25T12:00:00Z");
const base: License = {
  id: "l", tenant_id: "t", site_id: "s", commercial_plan_code: "", status: "active",
  issued_at: "2026-01-01T00:00:00Z", valid_until: new Date(now + 10 * day).toISOString(),
  offline_grace_days: 0, appliance_ids: ["a"], key_id: "k", created_at: "", grace_period_days: 30,
};

describe("licenseState", () => {
  it.each([
    [{}, "Active", "ok"],
    [{ valid_until: new Date(now - 1 * day).toISOString() }, "Grace", "warn"],
    [{ valid_until: new Date(now - 40 * day).toISOString() }, "Expired", "err"],
    [{ status: "suspended" }, "Suspended", "warn"],
    [{ status: "revoked" }, "Revoked", "err"],
    [{ status: "superseded" }, "Superseded", "default"],
    [{ appliance_ids: [] }, "Awaiting appliance binding", "warn"],
  ])("%j → %s", (over, label, tone) => {
    const s = licenseState({ ...base, ...(over as Partial<License>) }, now);
    expect(s.label).toBe(label);
    expect(s.tone).toBe(tone);
  });

  it("words machine statuses", () => {
    expect(statusWord("false_positive")).toBe("False positive");
    expect(statusWord(undefined)).toBe("—");
  });
});
