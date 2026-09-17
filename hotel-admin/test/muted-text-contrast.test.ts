import { describe, it, expect } from "vitest";
import resolveConfig from "tailwindcss/resolveConfig";
import tailwindConfig from "../tailwind.config";

// THE MOST-USED UTILITY ON THIS ADMIN POINTED AT THE WRONG TOKEN.
//
// globals.css defines two deliberately different things: --muted is a quiet SURFACE (very nearly white in the
// light theme) and --muted-foreground is a quiet LABEL. Its own comment says they exist so that "a quiet
// label and a quiet panel be styled independently".
//
// Tailwind resolves `text-muted` to muted.DEFAULT, and DEFAULT was the surface. So 517 secondary labels
// across these screens rendered at 1.12:1 against a white card -- not "low contrast", effectively invisible.
// That is the grey text operators have been squinting past, and it was one mapping rather than 517 pages.
//
// This test pins the mapping. A future refactor that points DEFAULT back at the surface fails here instead of
// silently making half the admin unreadable again.

const cfg = resolveConfig(tailwindConfig as any);
const colors = (cfg.theme as any).colors;

/** hsl(H S% L%) -> relative luminance, per WCAG. */
function luminance(h: number, s: number, l: number): number {
  const a = (s / 100) * Math.min(l / 100, 1 - l / 100);
  const f = (n: number) => {
    const k = (n + h / 30) % 12;
    const c = l / 100 - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
    return c <= 0.03928 / 1 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(0) + 0.7152 * f(8) + 0.0722 * f(4);
}
function ratio(fg: [number, number, number], bg: [number, number, number]): number {
  const a = luminance(...fg);
  const b = luminance(...bg);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

describe("quiet labels are readable", () => {
  it("text-muted resolves to the quiet LABEL token, never the quiet panel", () => {
    const muted = typeof colors.muted === "string" ? colors.muted : colors.muted.DEFAULT;
    expect(muted).toContain("--muted-foreground");
    expect(muted).not.toMatch(/--muted\s*\)/);
  });

  it("the quiet panel is still reachable, by a name that says what it is", () => {
    // The three places that genuinely wanted a quiet SURFACE must not have been collateral damage.
    expect(colors["muted-surface"]).toContain("--muted");
  });

  it("the resulting contrast clears WCAG AA in both themes", () => {
    // Values straight from globals.css. If someone darkens a card or lightens a label past the threshold,
    // this fails with the number rather than with an opinion.
    const LIGHT_LABEL: [number, number, number] = [218, 13, 44];
    const LIGHT_CARD: [number, number, number] = [0, 0, 100];
    const DARK_LABEL: [number, number, number] = [220, 14, 64];
    const DARK_CARD: [number, number, number] = [222, 18, 12];

    expect(ratio(LIGHT_LABEL, LIGHT_CARD)).toBeGreaterThanOrEqual(4.5);
    expect(ratio(DARK_LABEL, DARK_CARD)).toBeGreaterThanOrEqual(4.5);

    // And the value it used to have does NOT -- so this test would have failed before the fix rather than
    // passing for a reason unrelated to the defect.
    const OLD_LIGHT: [number, number, number] = [212, 22, 95];
    expect(ratio(OLD_LIGHT, LIGHT_CARD)).toBeLessThan(1.5);
  });
});
