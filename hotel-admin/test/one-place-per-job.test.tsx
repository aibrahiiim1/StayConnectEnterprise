import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// TWO DUPLICATIONS THAT KEPT COMING BACK, PINNED AS SOURCE PROPERTIES.
//
// Both are about a screen saying a thing twice. Neither is catchable by rendering a component in isolation:
// the first is about what is ABSENT from a healthy dashboard, and the second about a control that must
// disappear once an appliance reaches a state the test harness would have to fake its way into. Asserting on
// the source is the honest way to hold them, and it states the reason where the next editor will read it.

const read = (p: string) => readFileSync(join(process.cwd(), p), "utf8");

describe("the dashboard states licensing once", () => {
  const src = read("app/(app)/dashboard/page.tsx");

  it("does not list the cloud as a runtime dependency", () => {
    // Central serves this property's activation and LICENCE and nothing else. The appliance reports nothing
    // to it (the cloud telemetry subsystem is removed), so there is no "reporting to the cloud" row to show,
    // healthy or otherwise -- and nothing on the dashboard may suggest reconnecting one.
    expect(src).not.toContain("Reporting to the OneGate cloud");
    expect(src).not.toContain("sync_outbox");
    expect(src).not.toContain("describeOutbox");

    // And the old explanatory box, which was honest but still permanent, must not come back.
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
