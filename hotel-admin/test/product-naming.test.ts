import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The product serves hotels, offices and campuses: the console is "the Admin Console", never "Hotel Admin".
// Comments are skipped; this guards what a person can read on screen.
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
  it("never calls the console 'Hotel Admin' in user-visible source", () => {
    const hits: string[] = [];
    for (const dir of ["app", "components", "lib"]) {
      for (const file of sources(join(ROOT, dir))) {
        readFileSync(file, "utf8").split("\n").forEach((line, i) => {
          const t = line.trim();
          if (t.startsWith("//") || t.startsWith("*") || t.startsWith("/*")) return;
          if (/Hotel[ -]Admin/.test(line)) hits.push(`${file.slice(ROOT.length + 1)}:${i + 1}`);
        });
      }
    }
    expect(hits).toEqual([]);
  });
});
