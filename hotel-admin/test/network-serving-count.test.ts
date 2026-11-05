import { describe, it, expect } from "vitest";
import { servingCount } from "@/lib/api/network-overview";

describe("the Serving clients count", () => {
  it("counts what is running, not what a status label suggests", () => {
    const rows = [{ id: "staged", enabled: true }, { id: "failed-change", enabled: true }, { id: "no-overview", enabled: true }, { id: "off", enabled: false }];
    const overview = { staged: { running: false }, "failed-change": { running: true } };
    // A staged activation is not serving yet; a failed change leaves the confirmed configuration serving; no overview entry falls back to the saved switch.
    expect(servingCount(rows, overview)).toBe(2);
  });
});
