import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Source properties for two duplications that kept returning; rendering one component cannot catch an absence or an
// unreachable state.

const read = (p: string) => readFileSync(join(process.cwd(), p), "utf8");

describe("the dashboard states licensing once", () => {
  const src = read("app/(app)/dashboard/page.tsx");

  it("does not list the cloud as a runtime dependency", () => {
    // Central serves activation and licence only and the appliance reports nothing to it, so the dashboard must show no
    // "reporting to the cloud" row.
    expect(src).not.toContain("Reporting to the OneGate cloud");
    expect(src).not.toContain("sync_outbox");
    expect(src).not.toContain("describeOutbox");

    expect(src).not.toContain("Used for this appliance");
    expect(src).not.toMatch(/Operational reporting is intentionally\s*\n?\s*off/);
  });

  it("keeps the real runtime dependencies", () => {
    expect(src).toContain('<ServiceRow title="Site database"');
    expect(src).toContain('<ServiceRow title="Session controller"');
  });
});

describe("the appliance has one place for activation and licence files", () => {
  const view = read("app/(app)/appliance/appliance-status.tsx");

  it("installs a licence file only through the one licence upload", () => {
    expect(view).toContain("Upload licence file");
    expect(view).toContain('api.postRaw("/license"');
  });

  it("carries offline activation over the section 8 endpoints only", () => {
    expect(view).toContain('"/central/offline-request"');
    expect(view).toContain('"/central/offline-package"');
    for (const gone of ["/setup/activation-package", "/setup/offline-import", "/setup/enroll", "/setup/status"]) {
      expect(view, gone).not.toContain(gone);
    }
  });
});
