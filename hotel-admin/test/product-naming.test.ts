import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The product serves hotels, offices and campuses: the console is "the Admin Console", never "Hotel Admin", and a
// site's own domain is its "portal domain" (appliance) or "custom domain" (Central), never a "hotel domain".
// Comments are skipped; this guards what a person can read on screen.
const RETIRED = /Hotel[ -]Admin|yourhotel|[Hh]otel (portal )?domain/;
const ROOT = join(__dirname, "..");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    if (name === "node_modules" || name.startsWith(".")) return [];
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(ts|tsx)$/.test(name) ? [p] : [];
  });
}

describe("product naming", () => {
  it("keeps retired hotel-only names out of user-visible source", () => {
    const hits: string[] = [];
    for (const dir of ["app", "components", "lib"]) {
      for (const file of sources(join(ROOT, dir))) {
        readFileSync(file, "utf8").split("\n").forEach((line, i) => {
          const t = line.trim();
          if (t.startsWith("//") || t.startsWith("*") || t.startsWith("/*")) return;
          if (RETIRED.test(line)) hits.push(`${file.slice(ROOT.length + 1)}:${i + 1}`);
        });
      }
    }
    expect(hits).toEqual([]);
  });
});
