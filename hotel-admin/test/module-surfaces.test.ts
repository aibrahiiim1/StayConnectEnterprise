import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { MODULE_SURFACES, surfaceAvailable } from "@/lib/capabilities";

// The menu's copy of licence-controlled surfaces must equal edged's surfaceModules (it exists only to fail closed
// before edged answers).
const EDGED = readFileSync(join(process.cwd(), "..", "data-plane", "cmd", "edged", "modules.go"), "utf8");

function edgedSurfaces(): string[] {
  const start = EDGED.indexOf("var surfaceModules = map[string][]string{");
  const body = EDGED.slice(start, EDGED.indexOf("\n}", start));
  return [...body.matchAll(/^\s*"([a-z0-9-]+)":/gm)].map((m) => m[1]).sort();
}

describe("module-owned surfaces", () => {
  it("mirror edged's surfaceModules exactly", () => {
    expect([...MODULE_SURFACES].sort()).toEqual(edgedSurfaces());
  });

  it("fail closed while the capability answer is unknown, and core surfaces do not", () => {
    expect(surfaceAvailable(null, "payment-providers")).toBe(false);
    expect(surfaceAvailable({ surfaces: [] }, "pms-stays")).toBe(false);
    expect(surfaceAvailable(null, "commercial-packages")).toBe(true);
    expect(surfaceAvailable({ surfaces: ["payment-providers"] }, "payment-providers")).toBe(true);
  });
});
