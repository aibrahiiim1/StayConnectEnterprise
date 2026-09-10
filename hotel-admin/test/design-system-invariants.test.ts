// THE INVARIANTS THE REBUILT DESIGN SYSTEM DEPENDS ON, ASSERTED AGAINST THE SOURCE.
//
// Every rule here was broken in the admin before this delivery, and every one of them is invisible in a rendered
// component test: a page that sets its own `p-6` looks fine on its own and produces double padding only inside
// the app shell; a `text-red-600` renders perfectly in light mode and is unreadable in dark; a `window.prompt`
// for a password passes every functional test there is while displaying the password on a front-desk screen.
//
// These are source assertions on purpose. They are the only kind that can say "nobody has reintroduced this
// anywhere", which is the actual requirement — a component test proves one screen, and there are thirty-four.

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";

const ROOT = join(__dirname, "..");

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === ".next" || entry === ".deploy" || entry === "test-results") continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) walk(full, out);
    else if (/\.(tsx|ts)$/.test(entry)) out.push(full);
  }
  return out;
}

const SOURCES = [...walk(join(ROOT, "app")), ...walk(join(ROOT, "components")), ...walk(join(ROOT, "lib"))];
const PAGES = SOURCES.filter((f) => /[\\/]app[\\/].*[\\/]page\.tsx$/.test(f));
const rel = (f: string) => relative(ROOT, f).split(sep).join("/");
const read = (f: string) => readFileSync(f, "utf8");

describe("the page gutter is owned by the shell, not by the pages", () => {
  // The fault this replaces: about half the screens opened with their own `p-6 max-w-7xl mx-auto` and the other
  // half with a bare `space-y-4`, which has no padding at all — so those pages rendered flush against the window
  // edge and the sidebar. The gutter now lives on the scrolling container in app/(app)/layout.tsx.
  it("no page sets its own page-level padding", () => {
    const offenders: string[] = [];
    for (const f of PAGES) {
      const src = read(f);
      for (const m of src.matchAll(/className="([^"]*)"/g)) {
        const classes = m[1].split(/\s+/);
        // A page-level gutter is padding on ALL sides combined with a centring measure. `p-3` inside a card or
        // `p-6` on a modal overlay is not that and must not be flagged.
        const hasAllSidePad = classes.some((c) => /^p-\d+$/.test(c));
        const centres = classes.includes("mx-auto") || classes.some((c) => /^max-w-/.test(c));
        if (hasAllSidePad && centres) offenders.push(`${rel(f)} :: ${m[1]}`);
      }
    }
    expect(offenders, `these pages re-introduced their own gutter, which double-pads inside the shell:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("the shell supplies exactly one gutter and one measure", () => {
    const layout = read(join(ROOT, "app/(app)/layout.tsx"));
    expect(layout).toMatch(/<main[^>]*className="[^"]*overflow-y-auto/);
    // The gutter and the measure are on the wrapper INSIDE main, so the scrollbar sits at the window edge while
    // the content stays inset.
    expect(layout).toMatch(/mx-auto w-full max-w-\[\d+rem\] px-4 py-5 sm:px-6 sm:py-6 lg:px-8/);
  });
});

describe("colour only ever comes from a theme token", () => {
  // A literal palette class cannot be theme-aware: it is one fixed colour, so it is correct in at most one of the
  // two themes. This is what made the old admin dark-only.
  const LITERAL = /\b(?:text|bg|border|ring|from|to|via|fill|stroke)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}\b/g;

  it("no component uses a literal Tailwind palette colour", () => {
    const offenders: string[] = [];
    for (const f of SOURCES) {
      for (const m of read(f).matchAll(LITERAL)) offenders.push(`${rel(f)} :: ${m[0]}`);
    }
    expect(offenders, `literal palette colours are not theme-aware:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("no component hard-codes a hex colour", () => {
    const offenders: string[] = [];
    for (const f of SOURCES) {
      for (const m of read(f).matchAll(/#[0-9a-fA-F]{6}\b/g)) offenders.push(`${rel(f)} :: ${m[0]}`);
    }
    expect(offenders, `hard-coded hex colours bypass the token system:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("every role token is defined for BOTH themes", () => {
    const css = read(join(ROOT, "app/globals.css"));
    const lightBlock = css.slice(css.indexOf(":root {"), css.indexOf(".dark {"));
    const darkBlock = css.slice(css.indexOf(".dark {"));
    const names = (block: string) => new Set([...block.matchAll(/--([a-z0-9-]+):/g)].map((m) => m[1]));
    const light = names(lightBlock);
    const dark = names(darkBlock);
    // `radius` is a geometry token, not a colour, and is deliberately theme-invariant.
    const missing = [...light].filter((n) => n !== "radius" && !dark.has(n));
    expect(missing, `defined for light but never redefined for dark — these would render with the light value on a dark surface: ${missing.join(", ")}`).toEqual([]);
  });

  it("the dark chart series are stepped independently, not reused from light", () => {
    // A dark palette that is literally the light one fails its own lightness band. If these ever become equal it
    // means somebody "simplified" the duplication away and silently broke dark-mode chart legibility.
    const css = read(join(ROOT, "app/globals.css"));
    const grab = (block: string) =>
      [...block.matchAll(/--chart-(\d):\s*([^;]+);/g)].map((m) => `${m[1]}:${m[2].trim()}`).join("|");
    const light = grab(css.slice(css.indexOf(":root {"), css.indexOf(".dark {")));
    const dark = grab(css.slice(css.indexOf(".dark {")));
    expect(light).not.toBe("");
    expect(dark).not.toBe("");
    expect(dark).not.toBe(light);
  });
});

describe("no browser dialog collects a password or guards a destructive action", () => {
  // `window.prompt` renders a PLAIN TEXT field. Three screens were taking an operator's own admin password
  // through one, so every character appeared on the screen. `window.confirm` cannot say what an action does,
  // which is how "Delete this guest network permanently?" was the entire explanation before removing the network
  // every guest on a VLAN is connected through.
  const SCREENS = SOURCES.filter((f) => /[\\/](app|components)[\\/]/.test(f) && !/[\\/]ui[\\/]dialog\.tsx$/.test(f));

  it("window.prompt is never used", () => {
    const offenders = SCREENS.filter((f) => /(?:^|[^.\w])(?:window\.)?prompt\s*\(/.test(stripComments(read(f))));
    expect(offenders.map(rel), "window.prompt shows what is typed into it").toEqual([]);
  });

  it("no password is collected outside a masked field", () => {
    const offenders: string[] = [];
    for (const f of SCREENS) {
      const src = stripComments(read(f));
      // Any prompt-like call whose argument mentions a password at all.
      if (/(?:prompt|confirm)\s*\([^)]*[Pp]assword/.test(src)) offenders.push(rel(f));
    }
    expect(offenders, "a password must be typed into type=\"password\", never into a browser dialog").toEqual([]);
  });
});

describe("form controls are programmatically labelled", () => {
  it("Field binds its label to the control it wraps", () => {
    const src = read(join(ROOT, "components/ui/input.tsx"));
    // The id is generated and injected into the child, so a caller that forgets htmlFor still produces a bound
    // pair. Before this, Field rendered a label pointing at nothing and looked identical on screen.
    expect(src).toMatch(/React\.cloneElement/);
    expect(src).toMatch(/htmlFor \?\? \(childProps\.id as string \| undefined\) \?\? `\$\{generated\}-control`/);
    expect(src).toMatch(/<Label htmlFor=\{controlId\}>/);
  });

  it("the confirmation dialog's reason and password fields use Field, not bare divs", () => {
    const src = read(join(ROOT, "components/ui/dialog.tsx"));
    expect(src).toMatch(/<Field label=\{reasonLabel\} htmlFor=\{reasonId\}/);
    expect(src).toMatch(/<Field label=\{passwordLabel\} htmlFor=\{passwordId\}/);
    expect(src).toMatch(/type="password"/);
  });

  it("Tooltip provides its own context so it cannot crash outside a provider", () => {
    const src = read(join(ROOT, "components/ui/tooltip.tsx"));
    expect(src).toMatch(/<TooltipPrimitive\.Provider/);
  });
});

describe("the theme is an operator choice that follows the system by default", () => {
  it("ThemeProvider defaults to system and persists the choice", () => {
    const src = read(join(ROOT, "components/theme-provider.tsx"));
    expect(src).toMatch(/attribute="class"/);
    expect(src).toMatch(/defaultTheme="system"/);
    expect(src).toMatch(/enableSystem/);
    expect(src).toMatch(/storageKey="stayconnect-admin-theme"/);
  });

  it("the root layout suppresses the hydration warning the theme script requires", () => {
    // next-themes stamps the stored choice onto <html> before React hydrates, so the server markup and the first
    // client render legitimately differ on that attribute. Without this, React logs a mismatch for something
    // that is working correctly — and a console error is a live-acceptance failure.
    const src = read(join(ROOT, "app/layout.tsx"));
    expect(src).toMatch(/<html[^>]*suppressHydrationWarning/);
    expect(src).toMatch(/ThemeProvider/);
  });

  it("all three modes are offered, and dark is one option rather than the only one", () => {
    const src = read(join(ROOT, "components/theme-toggle.tsx"));
    for (const mode of ["light", "dark", "system"]) {
      expect(src).toContain(`value: "${mode}"`);
    }
    expect(src).toMatch(/role="radiogroup"/);
  });

  it("the stylesheet sets color-scheme per theme so native controls follow it", () => {
    const css = read(join(ROOT, "app/globals.css"));
    expect(css).toMatch(/:root\s*\{\s*color-scheme:\s*light/);
    expect(css).toMatch(/\.dark\s*\{\s*color-scheme:\s*dark/);
  });
});

describe("the sidebar and the role matrix stay in step with the server", () => {
  it("every nav item names a resource the UI role matrix knows about", () => {
    // A nav item whose resource is missing from the matrix is hidden from every role but site_admin — which is
    // how PMS connection, Network routing and Duplicate sources became unreachable for the role that owns them.
    const nav = read(join(ROOT, "components/nav.tsx"));
    const roles = read(join(ROOT, "lib/roles.ts"));
    const resources = new Set([...nav.matchAll(/resource:\s*"([^"]+)"/g)].map((m) => m[1]));
    // Both spellings: lib/roles.ts quotes hyphenated keys ("pms-stays") and leaves single words bare (sessions).
    const known = new Set(
      [...roles.matchAll(/"?([a-z0-9-]+)"?:\s*"(?:read|write)"/g)].map((m) => m[1]),
    );
    // These are legitimately site_admin-only on the server too.
    const adminOnly = new Set(["operators"]);
    const missing = [...resources].filter((r) => !known.has(r) && !adminOnly.has(r));
    expect(missing, `nav resources absent from lib/roles.ts — unreachable for every non-admin role: ${missing.join(", ")}`).toEqual([]);
  });
});

/** Strip comments so a rule that DESCRIBES a banned pattern does not trip the rule. */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
}
