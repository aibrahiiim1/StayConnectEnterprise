import { describe, expect, it } from "vitest";
import { reasonLabel } from "@/lib/api/checkout-grace";

describe("the default Grace Period change reason", () => {
  it("reads as the Admin Console, for new and for already stored changes", () => {
    expect(reasonLabel("ADMIN_CONSOLE_UPDATE")).toBe("Admin Console update");
    expect(reasonLabel("HOTEL_ADMIN_UPDATE")).toBe("Admin Console update");
  });
  it("still words the other reasons as before", () => {
    expect(reasonLabel("POLICY_CHANGE")).toBe("Planned policy change");
    expect(reasonLabel("")).toBe("No reason recorded");
    expect(reasonLabel("LATE_CHECKOUT")).toMatch(/late checkout/i);
  });
});
