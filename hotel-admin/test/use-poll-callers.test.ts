import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) sources(p, out);
    else if (/\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

// usePoll waits for a refresh in flight only when the callback returns its promise; `void load()` let slow reads stack.
describe("every usePoll caller returns its load promise", () => {
  it("never discards the promise with void", () => {
    const offenders = ["app", "components"].flatMap((d) => sources(join(process.cwd(), d)))
      .filter((f) => /usePoll\(\s*\(\)\s*=>\s*\{\s*void\s/.test(readFileSync(f, "utf8")));
    expect(offenders).toEqual([]);
  });
});
