import { describe, it, expect } from "vitest";
import { formatBytes, quotaPercent } from "@/lib/bytes";
import { formatBytes as sessionsBytes } from "@/lib/utils";

// One unit system on every screen: 1 GB = 1,000,000,000 bytes, as allowances are entered.
describe("decimal data units", () => {
  it("shows an allowance and its use the same way as Active sessions", () => {
    expect(formatBytes(7_000_000_000)).toBe("7.00 GB");
    expect(formatBytes(3_713_014_847)).toBe("3.71 GB");
    expect(formatBytes(7_000_000_000)).toBe(sessionsBytes(7_000_000_000));
    expect(quotaPercent(3_713_014_847, 7_000_000_000)).toBe(53);
  });

  it("uses decimal steps below a gigabyte and keeps the empty cases", () => {
    expect(formatBytes(1_500_000)).toBe("1.50 MB");
    expect(formatBytes(999)).toBe("999 B");
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(null)).toBe("—");
    expect(formatBytes(undefined)).toBe("—");
  });
});
