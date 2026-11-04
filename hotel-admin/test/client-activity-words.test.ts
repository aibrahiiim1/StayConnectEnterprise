import { describe, it, expect } from "vitest";
import { activityWords, clientWords, type ActivityEvent } from "@/lib/client-activity-words";

const ev = (over: Partial<ActivityEvent>): ActivityEvent => ({ id: "x", at: "2026-11-04T10:00:00Z", category: "connection", kind: "CONNECTED", severity: "ok", ...over });

describe("client activity wording", () => {
  it("explains a disconnection caused by leaving the Wi-Fi", () => {
    expect(activityWords(ev({ kind: "DISCONNECTED", detail: "ADDRESS_NO_LONGER_OWNED" })).detail).toMatch(/left the wi-fi/i);
  });
  it("names a refusal at the device limit, never an internal failure", () => {
    expect(activityWords(ev({ category: "sign_in", kind: "ROOM_SIGN_IN", detail: "DEVICE_LIMIT_REACHED" })).detail)
      .toBe("Package device limit reached");
  });
  it("shows a room as the client, and a voucher without its code", () => {
    expect(clientWords(ev({ client_kind: "room", client_label: "2108" }))).toBe("Room 2108");
    expect(clientWords(ev({ client_kind: "voucher", client_label: "" }))).toBe("Voucher");
    expect(clientWords(ev({ mac: "02:00:00:00:00:01" }))).toBe("02:00:00:00:00:01");
  });
  it("reads a staff action with the audit trail's own words", () => {
    expect(activityWords(ev({ category: "staff", kind: "session.disconnected", detail: "guest asked" })).detail).toBe("Reason: guest asked");
  });
});
