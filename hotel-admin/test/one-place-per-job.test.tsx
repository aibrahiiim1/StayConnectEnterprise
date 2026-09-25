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
    expect(src).toContain('<ServiceRow title="Reporting to the Velonet cloud"');
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

describe("an activated appliance has one place to install a licence", () => {
  const setup = read("app/(app)/appliance/setup-section.tsx");
  const license = read("app/(app)/appliance/license-section.tsx");

  it("Setup offers no licence upload once the appliance is activated", () => {
    // The upload is inside the not-activated branch. Two controls that install the same thing is two places
    // to look when a renewal is refused, and an invitation to upload a renewal into the onboarding flow of
    // an appliance that finished onboarding months ago.
    const card = setup.slice(setup.indexOf("LICENCE, ONCE ONBOARDING IS DONE"));
    const gate = card.indexOf("{complete ? (");
    const upload = card.indexOf("onPackageFile(e.target.files");
    expect(gate, "the licence card no longer branches on activation").toBeGreaterThan(-1);
    expect(upload, "the onboarding licence upload has vanished entirely").toBeGreaterThan(-1);
    expect(upload, "the licence upload is not behind the not-yet-activated branch").toBeGreaterThan(gate);
    // The activated branch must point at the one place that owns renewals.
    const activated = card.slice(gate, upload);
    expect(activated).toContain("/appliance?section=license");
  });

  it("Setup keeps the initial activation package, which is not a licence upload", () => {
    // Offline ONBOARDING must stay here: the signed activation package carries assignment, trust material
    // and the first licence together, and there is nowhere else it could go.
    expect(setup).toContain("/setup/activation-package");
    expect(setup).toContain("upload the activation package");
  });

  it("Licence remains the one place a licence file is installed after activation", () => {
    expect(license).toContain("Upload licence file");
  });
});
