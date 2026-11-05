import { describe, it, expect } from "vitest";
import { clearExpiredLock } from "@/lib/sysnet-lock";

describe("the WAN / LAN pending lock after a read with no pending change", () => {
  const now = 1_800_000_000_000;
  it("ends a pending apply whose deadline has passed", () => {
    expect(clearExpiredLock({ state: "pending_confirmation", deadline_unix: now / 1000 - 5 }, now)).toBeNull();
  });
  it("keeps one still inside its window, and anything that is not a pending apply", () => {
    const live = { state: "pending_confirmation", deadline_unix: now / 1000 + 60 };
    expect(clearExpiredLock(live, now)).toBe(live);
    const failed = { state: "failed" };
    expect(clearExpiredLock(failed, now)).toBe(failed);
    expect(clearExpiredLock(null, now)).toBeNull();
  });
});
