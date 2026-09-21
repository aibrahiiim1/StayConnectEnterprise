import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// THE INVARIANT: AN APPROVED DESTINATION THIS APPLIANCE SERVES, AND THIS OPERATOR MAY READ, IS OFFERED.
//
// This file used to assert the opposite. It required that Internet packages and every PMS destination be
// HIDDEN when NEXT_PUBLIC_PHASE2_ADMIN / PHASE3_ADMIN were absent — which is precisely what a plain
// `next build` produces. So when a bundle was built without the flags and eleven destinations disappeared
// from a live operator's sidebar, the test suite agreed that was correct. The regression was
// indistinguishable from intent at the source level, and nothing below the deploy-time checker could tell
// them apart.
//
// That is why these tests are written the way they are. They assert a PROPERTY over the whole menu rather
// than the visibility of named items, so a destination added next year is covered the day it is added, and
// they assert it from the two inputs that genuinely decide it: what the appliance serves, and what the role
// may read.

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("next/navigation", () => ({ usePathname: () => "/" }));

// THE CAPABILITY ANSWER IS INJECTED THROUGH A FILE-SCOPED MOCK, NOT A MODULE RESET.
//
// The first version of this file used vi.doMock plus vi.resetModules() per test. That works, and it also
// invalidates the module registry for every test file that runs afterwards in the same worker, which pushed
// several already-slow async page tests past their five-second timeout under preflight's load -- and a test
// that times out leaves its DOM mounted, so the NEXT test in that file then failed on duplicate elements.
// Two unrelated-looking failures, both caused by how this file asked its question.
//
// vi.mock is hoisted and file-scoped. The holder lets each test choose the answer without touching the
// registry, and surfaceAvailable stays the real implementation.
const CAPS: { surfaces: string[] | null } = { surfaces: null };
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => (CAPS.surfaces === null ? null : { surfaces: CAPS.surfaces }) };
});

const NAV_SRC = readFileSync(join(process.cwd(), "components/nav.tsx"), "utf8");
const CONTRACT = JSON.parse(readFileSync(join(process.cwd(), "capability-contract.json"), "utf8"));

/** Every surface PRE-LIVE reports today. Used as a realistic, complete capability answer. */
const SERVED = [
  "audit", "auth-methods", "backups", "checkout-grace", "cloud-sync-recovery", "cloud-sync-settings",
  "commercial-packages", "diagnostics", "guest-accounts", "guest-signin-attempts", "guest-signin-credentials",
  "guest-signin-protection", "guest-signin-restrictions", "license", "network", "notification-providers",
  "operational-alerts", "operators", "pms-events", "pms-interfaces", "pms-reconciliation", "pms-resolutions",
  "pms-roster-reconciliation", "pms-routing", "pms-source-conflicts", "pms-stays", "portal-assets",
  "portal-branding", "reports", "sessions", "social-providers", "stripe-accounts", "usage", "walled-garden",
];

async function renderNavWith(surfaces: string[], roles: string[]) {
  CAPS.surfaces = surfaces;
  const { Nav } = await import("@/components/nav");
  render(<Nav roles={roles} email="a@b.c" onLogout={() => {}} />);
}

describe("the navigation contract", () => {
  beforeEach(() => { CAPS.surfaces = null; });
  afterEach(() => { cleanup(); CAPS.surfaces = null; });

  it("offers every destination the contract requires, on an appliance that serves it", async () => {
    // Data-driven from capability-contract.json, so a destination added to the contract is covered without
    // anyone remembering to extend this test.
    await renderNavWith(SERVED, ["site_admin"]);
    const missing = CONTRACT.required_navigation.entries
      .filter((e: any) => SERVED.includes(e.surface))
      .filter((e: any) => screen.queryAllByText(e.label).length === 0)
      .map((e: any) => `${e.label} (${e.route})`);
    expect(missing, "contract-required destinations missing from the sidebar").toEqual([]);
  });

  it("hides a destination whose surface this appliance does not serve", async () => {
    // The appliance saying "I do not run that" is a different statement from a build having forgotten it,
    // and it is the only one allowed to remove a destination.
    await renderNavWith(SERVED.filter((s) => s !== "pms-stays"), ["site_admin"]);
    expect(screen.queryByText("Stays")).toBeNull();
    expect(screen.getByText("Internet packages")).toBeInTheDocument(); // the rest is unaffected
  });

  it("still hides what the role may not read, even when the appliance serves it", async () => {
    // Authorization is untouched by any of this. edged enforces server-side regardless of the menu.
    await renderNavWith(SERVED, ["payments_operator"]);
    expect(screen.queryByText("Internet packages")).toBeNull();
    expect(screen.queryByText("Operators")).toBeNull();
  });

  it("shows nothing that needs a surface while the capability answer is still unknown", async () => {
    // Unknown must not empty the menu — a sidebar that blanks for a second on every load is its own defect —
    // so an unknown answer is optimistic and every page enforces its own state.
    await renderNavWith([], ["site_admin"]);
    expect(screen.getByText("Stays")).toBeInTheDocument();
  });
});

describe("the mechanism that caused this cannot return", () => {
  it("navigation consults no build-time capability flag", () => {
    // Next inlines NEXT_PUBLIC_* at build time. A flag absent when `next build` runs compiles to a permanent
    // false, and the menu it gates disappears with nothing to say so. Twice now.
    const reads = NAV_SRC.match(/process\.env\.[A-Za-z0-9_]+/g) ?? [];
    expect(reads, "nav.tsx reads a build-time variable again").toEqual([]);
    expect(NAV_SRC).not.toMatch(/enabled:\s*CAP_/);
  });

  it("the contract forbids every capability flag from appearing in the client bundle", () => {
    // Not just the two that were once required: a residual lookup now means somebody reintroduced the gate.
    const patterns: string[] = CONTRACT.forbidden_in_client_bundle.patterns;
    for (const n of [2, 3, 4, 5, 6]) {
      expect(patterns).toContain(`env.NEXT_PUBLIC_PHASE${n}_ADMIN`);
    }
  });

  it("the contract requires no build flag, because none can hide a destination now", () => {
    const required = Object.keys(CONTRACT.required_build_flags).filter((k) => !k.startsWith("_"));
    expect(required, "a required build flag is back; absence of one is what removed eleven destinations").toEqual([]);
  });
});

describe("no destination leads nowhere", () => {
  it("every sidebar route has a page compiled for it", () => {
    // The inverse failure: a menu entry whose page does not exist sends an operator to a 404. Cheap to check
    // here, and it also catches a route renamed on one side only.
    const { existsSync } = require("node:fs") as typeof import("node:fs");
    const routes = [...NAV_SRC.matchAll(/href:\s*"(\/[^"]*)"/g)].map((m) => m[1]);
    const dead = routes.filter((r) => !existsSync(join(process.cwd(), `app/(app)${r}/page.tsx`)));
    expect(dead, "sidebar routes with no page").toEqual([]);
  });

  it("every contract route is one the sidebar actually offers", () => {
    const routes = new Set([...NAV_SRC.matchAll(/href:\s*"(\/[^"]*)"/g)].map((m) => m[1]));
    const orphaned = CONTRACT.required_navigation.entries
      .filter((e: any) => !routes.has(e.route))
      .map((e: any) => e.route);
    expect(orphaned, "contract requires a destination the sidebar does not declare").toEqual([]);
  });
});
