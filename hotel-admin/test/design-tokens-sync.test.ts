import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// THE TWO CONSOLES ARE ONE FAMILY BECAUSE THEY READ ONE PALETTE. app/tokens.css is a generated copy of
// design-system/tokens.css; if somebody edits the copy, or the source without re-running the sync, this fails.
const ROOT = join(__dirname, "..");
const REPO = join(ROOT, "..");

describe("OneGate design tokens", () => {
  it("app/tokens.css is an exact copy of design-system/tokens.css", () => {
    expect(() =>
      execFileSync(process.execPath, [join(REPO, "tools/sync-design-tokens.mjs"), "--check"], { stdio: "pipe" }),
    ).not.toThrow();
  });

  it("globals.css imports the tokens and the self-hosted Inter face, and no remote font", () => {
    const css = readFileSync(join(ROOT, "app/globals.css"), "utf8");
    expect(css).toMatch(/@import "\.\/tokens\.css";/);
    expect(css).toMatch(/@fontsource-variable\/inter/);
    // No remote stylesheet, font or image. (An SVG namespace inside a data: URI is not a fetch.)
    expect(css).not.toMatch(/fonts\.googleapis|@import\s+(url\()?["']?https?:|url\(\s*["']?https?:/);
  });

  it("every text token pair used on a card clears 4.5:1 in both themes", () => {
    const css = readFileSync(join(ROOT, "app/tokens.css"), "utf8");
    const block = (from: string, to?: string) => css.slice(css.indexOf(from), to ? css.indexOf(to) : undefined);
    const light = block(":root {", "[data-product");
    const dark = block(".dark {", ".dark[data-product");
    const hsl = (b: string, name: string) => {
      const m = new RegExp(`--${name}:\\s*([\\d.]+) ([\\d.]+)% ([\\d.]+)%`).exec(b);
      if (!m) throw new Error(`missing --${name}`);
      return [Number(m[1]), Number(m[2]) / 100, Number(m[3]) / 100] as const;
    };
    const lum = ([h, s, l]: readonly [number, number, number]) => {
      const a = s * Math.min(l, 1 - l);
      const f = (n: number) => {
        const k = (n + h / 30) % 12;
        return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
      };
      const lin = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
      return 0.2126 * lin(f(0)) + 0.7152 * lin(f(8)) + 0.0722 * lin(f(4));
    };
    const ratio = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    const pairs: [string, string][] = [
      ["foreground", "card"], ["muted-foreground", "card"], ["muted-foreground", "background"],
      ["primary-foreground", "primary"], ["primary-subtle-foreground", "primary-subtle"],
      ["success-subtle-foreground", "success-subtle"], ["warning-subtle-foreground", "warning-subtle"],
      ["destructive-subtle-foreground", "destructive-subtle"], ["info-subtle-foreground", "info-subtle"],
      ["sidebar-foreground", "sidebar"], ["sidebar-muted", "sidebar"],
    ];
    for (const [theme, b] of [["light", light], ["dark", dark]] as const) {
      for (const [fg, bg] of pairs) {
        const r = ratio(lum(hsl(b, fg)), lum(hsl(b, bg)));
        expect(r, `${theme}: ${fg} on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
      // A control boundary must be visible: 3:1 against the card it sits on.
      expect(ratio(lum(hsl(b, "input")), lum(hsl(b, "card"))), `${theme}: input edge`).toBeGreaterThanOrEqual(3);
    }
  });
});
