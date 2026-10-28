import { describe, expect, it } from "vitest";
import { DEFAULT_TERMS, termsIssue } from "@/components/license-terms";

// Backend audit Phase 1 L10: the console checks the same bounds Central enforces.
describe("licence term bounds", () => {
  it("accepts values at the bounds", () => {
    expect(termsIssue({ ...DEFAULT_TERMS, maxGuests: "1000000", validMode: "days", validDays: "3650", graceDays: "365" })).toBeNull();
  });
  it("refuses values past them", () => {
    expect(termsIssue({ ...DEFAULT_TERMS, validMode: "days", validDays: "3651" })?.field).toBe("validDays");
    expect(termsIssue({ ...DEFAULT_TERMS, graceDays: "366" })?.field).toBe("graceDays");
    expect(termsIssue({ ...DEFAULT_TERMS, maxGuests: "1000001" })?.field).toBe("maxGuests");
  });
});
