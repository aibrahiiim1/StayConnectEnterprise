import { describe, it, expect } from "vitest";
import { certCheckDetail } from "@/lib/cert-check";

// A FAILED CHECK SAYS WHY. It used to say only "Validation reported a problem (exit 10)".
describe("certCheckDetail", () => {
  it("returns the validator's reason for a failure", () => {
    expect(certCheckDetail("log line\nINVALID: the served chain does not verify against the Caddy local root\n"))
      .toBe("the served chain does not verify against the Caddy local root");
  });
  it("explains a valid certificate that is due for renewal", () => {
    expect(certCheckDetail("OK (renewal due: within 2d of expiry in the served chain: CN = X)"))
      .toMatch(/^Renewal is due \(within 2d/);
  });
  it("is empty for a plain OK or nothing", () => {
    expect(certCheckDetail("OK")).toBe("");
    expect(certCheckDetail(undefined)).toBe("");
  });
});
