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

  it("does not list the cloud as a runtime dependency while it is licensing-only", () => {
    // Central serves this property's LICENCE and nothing else, by decision. A row in "Services this
    // appliance depends on" reported the health of something that does not run, and sat a few centimetres
    // below an Appliance & licence card that had already answered the question. A dashboard earns attention
    // by spending it only on what changed.
    expect(src).toContain('outbox.headline !== "Licensing only"');

    // And the old explanatory box, which was honest but still permanent, must not come back.
    expect(src).not.toContain("Used for this appliance");
    expect(src).not.toMatch(/Operational reporting is intentionally\s*\n?\s*off/);
  });

  it("still shows the cloud as a service when it IS a live dependency", () => {
    // If the mode is ever something other than licensing-only, the cloud is a real runtime dependency and
    // belongs in the list like any other.
    expect(src).toContain('<ServiceRow title="Reporting to the OneGate cloud"');
  });

  it("keeps the real runtime dependencies", () => {
    expect(src).toContain('<ServiceRow title="Site database"');
    expect(src).toContain('<ServiceRow title="Session controller"');
  });

  it("routes a genuine licensing problem through the attention list, not a permanent row", () => {
    expect(src).toContain('attention.push({ text: outbox.summary');
    // ...and never raises one for the licensing-only state, where the "repair" is the one thing that must
    // not happen.
    expect(src).toContain('health?.sync_outbox?.mode !== "LICENSING_ONLY"');
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
