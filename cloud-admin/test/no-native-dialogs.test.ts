import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, relative } from "node:path";

// Every confirmation, edit and reason prompt in the live console is a designed dialog. The browser's own
// confirm / prompt / alert boxes cannot say what an action does, show a password in clear text and are not
// themed. There are no exemptions: the two retired pages (/commercial, /subscription) are now redirects.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(tsx?|jsx?)$/.test(name)) out.push(p);
  }
  return out;
}

const files = [
  ...walk(join(root, "app")),
  ...walk(join(root, "components")),
  ...walk(join(root, "lib")),
];

// A call, not a mention: comments that name `window.prompt` are allowed, `window.prompt(` and a bare
// `confirm(` are not.
const CALL = /(^|[^\w.$])(?:window\.)?(confirm|prompt|alert)\s*\(/;
const WINDOW_CALL = /window\.(confirm|prompt|alert)\s*\(/;

describe("no native browser dialogs in the live console", () => {
  it("scans the live pages, components and lib", () => {
    expect(files.length).toBeGreaterThan(20);
    expect(files.some((f) => f.includes(join("app", "(app)", "appliances")))).toBe(true);
  });

  for (const f of files) {
    it(relative(root, f), () => {
      const code = readFileSync(f, "utf8")
        .split("\n")
        .filter((l) => !/^\s*(\/\/|\*|\/\*)/.test(l))
        .join("\n");
      expect(code).not.toMatch(WINDOW_CALL);
      expect(code).not.toMatch(CALL);
    });
  }
});
