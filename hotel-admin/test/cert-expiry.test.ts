import { describe, it, expect } from "vitest";
import { expiryState, DAY_MS } from "@/lib/cert-expiry";

const now = Date.parse("2026-11-05T12:00:00Z");
const at = (ms: number) => new Date(now + ms).toISOString();

describe("certificate expiry state comes from the expiry time", () => {
  it("tells an hour past expiry from a day still left, which a truncated day count cannot", () => {
    expect(expiryState(at(-3_600_000), 0, now)).toEqual({ kind: "expired", days: 0 });
    expect(expiryState(at(23 * 3_600_000), 0, now)).toEqual({ kind: "within_day", days: 0 });
    expect(expiryState(at(DAY_MS + 3_600_000), 0, now)).toEqual({ kind: "days", days: 1 });
    expect(expiryState(at(-3 * DAY_MS), -3, now)).toEqual({ kind: "expired", days: 3 });
  });
  it("falls back to the day count only when no expiry time is known", () => {
    expect(expiryState(undefined, 12, now)).toEqual({ kind: "days", days: 12 });
    expect(expiryState(undefined, -2, now)).toEqual({ kind: "expired", days: 2 });
    expect(expiryState(undefined, null, now)).toBeNull();
  });
});
