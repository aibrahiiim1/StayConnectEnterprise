import { describe, expect, it } from "vitest";
import { AUDIT_CATEGORIES, auditActor, auditTarget, auditWords } from "@/lib/audit-words";

describe("Activity wording for client data", () => {
  it("files every guest_data action under Client data", () => {
    expect(AUDIT_CATEGORIES).toContain("Client data");
    expect(auditWords("guest_data.viewed").title).toBe("Opened a client's record");
    expect(auditWords("guest_data.searched").title).toBe("Searched client data");
    expect(auditWords("guest_data.listed").title).toBe("Viewed a list of client data");
    for (const a of ["guest_data.viewed", "guest_data.searched", "guest_data.listed", "guest_data.exported", "guest_data.erased",
      "guest_data.erase_refused", "guest_data.some_future_action"]) {
      expect(auditWords(a).category).toBe("Client data");
    }
  });

  it("files staff, voucher and payment actions under their own categories, with security flagged", () => {
    expect(auditWords("operator.mfa_disabled")).toMatchObject({ category: "Staff & roles", severity: "security" });
    expect(auditWords("role.updated")).toMatchObject({ category: "Staff & roles", severity: "security" });
    expect(auditWords("voucher.code_revealed")).toMatchObject({ category: "Vouchers & codes", severity: "security" });
    expect(auditWords("otp_code.revealed").severity).toBe("security");
    expect(auditWords("payment_account.save").category).toBe("Payments");
    expect(auditWords("guest_account.created").category).toBe("Client accounts");
    expect(auditWords("operational_alert.resolved").title).toBe("Resolved an alert");
    expect(auditWords("pms_event.link").title).toBe("Linked a PMS event to a stay");
    expect(auditWords("voucher.some_future_action").category).toBe("Vouchers & codes");
    expect(auditWords("something.unheard_of")).toMatchObject({ title: "something.unheard_of", category: "Other" });
  });

  it("names the actor honestly", () => {
    const id = "11111111-2222-3333-4444-555555555555";
    expect(auditActor("operator", id, null)).toBe("An operator");
    expect(auditActor("operator", id, new Map())).toBe("A removed operator");
    expect(auditActor("operator", null, new Map([[id, "Mona"]]), { type: "operator", id })).toBe("Mona");
    expect(auditActor("guest", "aa:bb", null)).toBe("A client device");
    expect(auditActor("system", null, null)).toBe("The appliance");
    expect(auditTarget("network_guest")).toBeNull();
    expect(auditTarget("guest_network")).toBe("Client network");
  });
});
