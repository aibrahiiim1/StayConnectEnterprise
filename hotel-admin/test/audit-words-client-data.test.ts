import { describe, expect, it } from "vitest";
import { AUDIT_CATEGORIES, auditWords } from "@/lib/audit-words";

describe("Activity wording for client data", () => {
  it("files every guest_data action under Client data", () => {
    expect(AUDIT_CATEGORIES).toContain("Client data");
    expect(auditWords("guest_data.viewed").title).toBe("Opened a client's record");
    expect(auditWords("guest_data.searched").title).toBe("Searched client data");
    for (const a of ["guest_data.viewed", "guest_data.searched", "guest_data.exported", "guest_data.erased",
      "guest_data.erase_refused", "guest_data.some_future_action"]) {
      expect(auditWords(a).category).toBe("Client data");
    }
  });
});
