import { describe, expect, it } from "vitest";
import { periodBounds, periodWords } from "@/components/ui/date-range";

// Thursday 6 November 2026, mid-afternoon, local time.
const NOW = new Date(2026, 10, 6, 15, 30);
const d = (y: number, m: number, day: number) => new Date(y, m - 1, day);

describe("the period presets", () => {
  it.each([
    ["today", d(2026, 11, 6), d(2026, 11, 7)],
    ["yesterday", d(2026, 11, 5), d(2026, 11, 6)],
    ["this_week", d(2026, 11, 2), d(2026, 11, 9)],
    ["last_week", d(2026, 10, 26), d(2026, 11, 2)],
    ["this_month", d(2026, 11, 1), d(2026, 12, 1)],
    ["last_month", d(2026, 10, 1), d(2026, 11, 1)],
  ] as const)("%s covers local midnights, end excluded", (preset, from, to) => {
    expect(periodBounds({ preset }, NOW)).toEqual({ from, to });
  });

  it("starts the week on Monday, including on a Sunday and a Monday", () => {
    expect(periodBounds({ preset: "this_week" }, new Date(2026, 10, 8, 23, 59))!.from).toEqual(d(2026, 11, 2));
    expect(periodBounds({ preset: "this_week" }, new Date(2026, 10, 9, 0, 1))!.from).toEqual(d(2026, 11, 9));
  });

  it("crosses the year for last month in January", () => {
    expect(periodBounds({ preset: "last_month" }, new Date(2027, 0, 10))).toEqual({ from: d(2026, 12, 1), to: d(2027, 1, 1) });
  });

  it("is everything for All time", () => {
    expect(periodBounds({ preset: "all" }, NOW)).toBeNull();
    expect(periodWords({ preset: "all" }, NOW)).toBe("");
  });
});

describe("a custom range", () => {
  it("includes both days", () => {
    expect(periodBounds({ preset: "custom", from: "2026-09-01", to: "2026-09-30" }, NOW))
      .toEqual({ from: d(2026, 9, 1), to: d(2026, 10, 1) });
    expect(periodBounds({ preset: "custom", from: "2026-09-15", to: "2026-09-15" }, NOW))
      .toEqual({ from: d(2026, 9, 15), to: d(2026, 9, 16) });
  });

  it("is no range while incomplete, backwards or not a real day", () => {
    for (const [from, to] of [["2026-09-01", ""], ["2026-09-30", "2026-09-01"], ["2026-02-30", "2026-03-01"], ["soon", "2026-03-01"]]) {
      expect(periodBounds({ preset: "custom", from, to }, NOW)).toBeNull();
    }
  });
});
