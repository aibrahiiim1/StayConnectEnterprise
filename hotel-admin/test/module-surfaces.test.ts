import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { MODULE_SURFACES, surfaceAvailable } from "@/lib/capabilities";

// THE MENU'S LIST OF LICENCE-CONTROLLED SURFACES IS edged's LIST, NOT A SECOND OPINION.
//
// edged gates every module-owned surface per request (data-plane/cmd/edged/modules.go, surfaceModules). The
// menu keeps its own copy only so it can fail closed before edged has answered; a copy that drifts would
// either flash a licence-controlled destination or hide a core one. This reads the Go map and compares.
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
