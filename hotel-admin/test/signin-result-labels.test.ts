import { describe, it, expect } from "vitest";
import { resultLabel } from "@/lib/api/dashboard";

describe("sign-in results in the console", () => {
  it("names a refusal at the package's device limit, never as an internal failure", () => {
    expect(resultLabel("DEVICE_LIMIT_REACHED")).toBe("Package device limit reached");
    expect(resultLabel("DEVICE_LIMIT_REACHED")).not.toMatch(/unavailable|failure/i);
  });
  it("names a new device refused after check-out, never as an internal failure", () => {
    expect(resultLabel("AFTER_CHECKOUT_NEW_DEVICE_REJECTED")).toBe("New device refused after check-out");
    expect(resultLabel("AFTER_CHECKOUT_NEW_DEVICE_REJECTED")).not.toMatch(/unavailable|failure/i);
  });
  it("names a stale network address as such, never as an internal failure", () => {
    expect(resultLabel("ADDRESS_CONFLICT")).toMatch(/old network address/i);
    expect(resultLabel("ADDRESS_CONFLICT")).not.toMatch(/unavailable|failure/i);
  });
});
