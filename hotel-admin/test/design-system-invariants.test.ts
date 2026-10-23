// Source-level design-system invariants: the only way to prove nobody reintroduced a banned pattern anywhere.

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
  // Pages must not set their own gutter (`p-6 max-w-7xl mx-auto`); it lives on the scrolling container in
  // app/(app)/layout.tsx.
  it("no page sets its own page-level padding", () => {
    const offenders: string[] = [];
    for (const f of PAGES) {
      const src = read(f);
      for (const m of src.matchAll(/className="([^"]*)"/g)) {
        const classes = m[1].split(/\s+/);
        // A page-level gutter is padding on all sides plus a centring measure; `p-3` in a card or `p-6` on an overlay
        // is not.
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
    // The gutter and measure sit inside main so the scrollbar stays at the window edge.
    expect(layout).toMatch(/mx-auto w-full max-w-\[\d+rem\] px-4 py-5 sm:px-6 sm:py-6 lg:px-8/);
  });
});

describe("colour only ever comes from a theme token", () => {
  // A literal palette class is one fixed colour, correct in at most one theme.
  const LITERAL = /\b(?:text|bg|border|ring|from|to|via|fill|stroke)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}\b/g;

  it("no component uses a literal Tailwind palette colour", () => {
    const EXEMPT = new Set<string>();
    const offenders: string[] = [];
    for (const f of SOURCES) {
      if (EXEMPT.has(rel(f))) continue;
      for (const m of read(f).matchAll(LITERAL)) offenders.push(`${rel(f)} :: ${m[0]}`);
    }
    expect(offenders, `literal palette colours are not theme-aware:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("no component hard-codes a hex colour", () => {
    // Exemptions, scoped to single files so a hex added anywhere else still fails: the portal branding designer (edits
    // the GUEST portal's
    // palette, so admin tokens would preview the wrong colours) and the OneGate wordmark in components/brand.tsx (must
    // not be recoloured by a theme).
    const EXEMPT = new Set(["app/(app)/portal-branding/page.tsx", "components/brand.tsx"]);
    const offenders: string[] = [];
    for (const f of SOURCES) {
      if (EXEMPT.has(rel(f))) continue;
      for (const m of read(f).matchAll(/#[0-9a-fA-F]{6}\b/g)) offenders.push(`${rel(f)} :: ${m[0]}`);
    }
    expect(offenders, `hard-coded hex colours bypass the token system:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("every role token is defined for BOTH themes", () => {
    const css = read(join(ROOT, "app/globals.css"));
    const lightBlock = css.slice(css.indexOf(":root {"), css.indexOf(".dark {"));
    const darkBlock = css.slice(css.indexOf(".dark {"));
    const names = (block: string) => new Set([...block.matchAll(/--([a-z0-9-]+):/g)].map((m) => m[1]));
    const dark = names(darkBlock);

    // Classify by value, not name: every token whose value is an HSL triple needs a dark counterpart; geometry tokens
    // (radius, sidebar widths) are theme-invariant.
    const HSL_TRIPLE = /^\s*[\d.]+\s+[\d.]+%\s+[\d.]+%/;
    const lightColours = [...lightBlock.matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)]
      .filter((m) => HSL_TRIPLE.test(m[2]))
      .map((m) => m[1]);
    const missing = lightColours.filter((n) => !dark.has(n));
    expect(missing, `defined for light but never redefined for dark — these would render with the light value on a dark surface: ${missing.join(", ")}`).toEqual([]);
  });

  it("the dark chart series are stepped independently, not reused from light", () => {
    // A dark palette identical to the light one fails its lightness band (chart legibility). Token values come from
    // design-system/tokens.css, copied as app/tokens.css.
    const css = read(join(ROOT, "app/tokens.css"));
    const grab = (block: string) =>
      [...block.matchAll(/--chart-(\d):\s*([^;]+);/g)].map((m) => `${m[1]}:${m[2].trim()}`).join("|");
    const light = grab(css.slice(css.indexOf(":root {"), css.indexOf("[data-product")));
    const dark = grab(css.slice(css.indexOf(".dark {"), css.indexOf(".dark[data-product")));
    expect(light).not.toBe("");
    expect(dark).not.toBe("");
    expect(dark).not.toBe(light);
  });
});

describe("no browser dialog collects a password or guards a destructive action", () => {
  // `window.prompt` renders a plain-text field (an admin password would show) and `window.confirm` cannot say what an
  // action does.
  const SCREENS = SOURCES.filter((f) => /[\\/](app|components)[\\/]/.test(f) && !/[\\/]ui[\\/]dialog\.tsx$/.test(f));

  it("window.prompt is never used", () => {
    const offenders = SCREENS.filter((f) => /(?:^|[^.\w])(?:window\.)?prompt\s*\(/.test(stripComments(read(f))));
    expect(offenders.map(rel), "window.prompt shows what is typed into it").toEqual([]);
  });

  it("no password is collected outside a masked field", () => {
    const offenders: string[] = [];
    for (const f of SCREENS) {
      const src = stripComments(read(f));
      if (/(?:prompt|confirm)\s*\([^)]*[Pp]assword/.test(src)) offenders.push(rel(f));
    }
    expect(offenders, "a password must be typed into type=\"password\", never into a browser dialog").toEqual([]);
  });
});

describe("form controls are programmatically labelled", () => {
  it("Field binds its label to the control it wraps", () => {
    const src = read(join(ROOT, "components/ui/input.tsx"));
    // The id is generated and injected into the child, so a caller that forgets htmlFor still gets a bound pair.
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
    // next-themes stamps the stored choice onto <html> before hydration, so that attribute legitimately differs;
    // hydration is suppressed for it.
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
    // A nav item whose resource is missing from the matrix is hidden from every role but site_admin.
    const nav = read(join(ROOT, "components/nav.tsx"));
    // "Known" means some role holds the key in the matrix GENERATED from edged's rolePerms.
    const generated = JSON.parse(read(join(ROOT, "lib/role-permissions.generated.json"))) as {
      roles: Record<string, Record<string, string>>;
    };
    const resources = new Set([...nav.matchAll(/resource:\s*"([^"]+)"/g)].map((m) => m[1]));
    const known = new Set(Object.values(generated.roles).flatMap((row) => Object.keys(row)));
    const adminOnly = new Set(["operators"]);
    const missing = [...resources].filter((r) => !known.has(r) && !adminOnly.has(r));
    expect(missing, `nav resources absent from lib/roles.ts — unreachable for every non-admin role: ${missing.join(", ")}`).toEqual([]);
  });
});

/** Strip comments so a rule that DESCRIBES a banned pattern does not trip the rule. */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
}
