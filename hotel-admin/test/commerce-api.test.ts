import { describe, it, expect, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() }, ApiError: class extends Error {} }));

import { formatMoney, priceText, activityPath, statusWords, whoWords, endReasonText } from "@/lib/api/commerce";

describe("formatMoney — minor units with the recorded exponent", () => {
  it("uses the exponent it is given, never a blind /100", () => {
    expect(formatMoney(500, "usd", 2)).toEqual({ text: "5.00 USD", assumed: false });
    expect(formatMoney(500, "JPY", 0)).toEqual({ text: "500 JPY", assumed: false });
    expect(formatMoney(1500, "BHD", 3)).toEqual({ text: "1.500 BHD", assumed: false });
  });
  it("says Free for zero and a dash for nothing", () => {
    expect(formatMoney(0, "USD", 2).text).toBe("Free");
    expect(formatMoney(null, "USD", 2).text).toBe("—");
  });
  it("assumes 2 only when the record carries no exponent, and says so", () => {
    expect(formatMoney(250, "USD", null)).toEqual({ text: "2.50 USD", assumed: true });
    expect(priceText(250, "USD", undefined)).toBe("2.50 USD (assumed 2 decimals)");
    expect(priceText(250, "USD", 2)).toBe("2.50 USD");
  });
});

describe("activityPath — what the activity view asks the server for", () => {
  it("sends the period, filters and paging, and drops what was not chosen", () => {
    const p = activityPath({ range: "7d", status: "all", q: "  ", limit: 25, offset: 50 });
    expect(p).toBe("/commercial-packages/activity?range=7d&limit=25&offset=50");
    const q = new URLSearchParams(activityPath({
      range: "custom", from: "2026-09-01T00:00:00.000Z", to: "2026-09-02T00:00:00.000Z",
      package_id: "pk", source: "VOUCHER_REDEMPTION", status: "ended", q: " 4202 ",
    }).split("?")[1]);
    expect(q.get("from")).toBe("2026-09-01T00:00:00.000Z");
    expect(q.get("package_id")).toBe("pk");
    expect(q.get("source")).toBe("VOUCHER_REDEMPTION");
    expect(q.get("status")).toBe("ended");
    expect(q.get("q")).toBe("4202");
    expect(q.get("offset")).toBe("0");
  });
  it("never sends a tenant or site — those are the appliance's own", () => {
    expect(activityPath({ range: "24h" })).not.toMatch(/tenant|site/);
  });
});

describe("the words", () => {
  it("names status, who and why-it-ended without identifiers", () => {
    expect(statusWords("NOT_GRANTED").label).toBe("No access given");
    expect(whoWords({ room: "4202", sign_in_kind: "STAY" })).toBe("Room 4202");
    expect(whoWords({ sign_in_kind: "VOUCHER" })).toBe("A voucher guest");
    expect(endReasonText("DATA")).toBe("Data allowance used up");
    expect(endReasonText(null)).toBeNull();
  });
});
