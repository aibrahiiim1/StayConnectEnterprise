import { describe, it, expect } from "vitest";
import { resultLabel } from "@/lib/api/dashboard";

describe("sign-in results in the console", () => {
  it("names a refusal at the package's device limit, never as an internal failure", () => {
    expect(resultLabel("DEVICE_LIMIT_REACHED")).toBe("Package device limit reached");
    expect(resultLabel("DEVICE_LIMIT_REACHED")).not.toMatch(/unavailable|failure/i);
  });
});
