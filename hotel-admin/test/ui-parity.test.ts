import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The two consoles share one design system: the same component files with the same APIs. A change made in one
// console only is the start of two design systems. (docs/ui/DESIGN_SYSTEM.md)
const HERE = join(__dirname, "..");
const REPO = join(HERE, "..");
const CONSOLES = [join(REPO, "hotel-admin"), join(REPO, "cloud-admin")];
// Only the Admin Console draws charts and edits code.
const ONLY_HOTEL = new Set(["chart.tsx", "code-editor.tsx"]);
const SHARED = ["components/help.tsx", "app/globals.css", "tailwind.config.ts"];

describe("the shared design system is identical in both consoles", () => {
  const present = CONSOLES.every((c) => existsSync(c));

  it.runIf(present)("components/ui holds the same files, byte for byte", () => {
    const [hotel, cloud] = CONSOLES.map((c) => join(c, "components/ui"));
    const names = new Set([...readdirSync(hotel), ...readdirSync(cloud)].filter((n) => !ONLY_HOTEL.has(n)));
    const differ = [...names].filter((n) => {
      const a = join(hotel, n);
      const b = join(cloud, n);
      return !existsSync(a) || !existsSync(b) || readFileSync(a, "utf8") !== readFileSync(b, "utf8");
    });
    expect(differ, "copy the change to the other console").toEqual([]);
  });

  it.runIf(present)("help, globals and the Tailwind config are the same", () => {
    const differ = SHARED.filter(
      (f) => readFileSync(join(CONSOLES[0], f), "utf8") !== readFileSync(join(CONSOLES[1], f), "utf8"),
    );
    expect(differ).toEqual([]);
  });
});
