import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Invariant: a destination the appliance serves and the role may read is offered. Asserted as a property over the whole
// menu, not named items.

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("next/navigation", () => ({ usePathname: () => "/" }));

// The capability answer comes from a file-scoped vi.mock, not vi.resetModules, which slows later files in the worker
// and leaks DOM between tests.
const ALL_MODULES = Object.fromEntries(
  ["hospitality", "paid_access", "card_payment", "room_charge", "social_login", "email_otp", "sms_otp", "whatsapp_otp"].map(
    (id) => [id, { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true }]),
);
const CAPS: { surfaces: string[] | null; modules: Record<string, unknown> | null } = { surfaces: null, modules: ALL_MODULES };
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => (CAPS.surfaces === null ? null : { surfaces: CAPS.surfaces, modules: CAPS.modules as never }) };
});

const NAV_SRC = readFileSync(join(process.cwd(), "components/nav.tsx"), "utf8");
const CONTRACT = JSON.parse(readFileSync(join(process.cwd(), "capability-contract.json"), "utf8"));

const SERVED = [
  "audit", "auth-methods", "backups", "checkout-grace", "client-groups",
  "commercial-packages", "diagnostics", "guest-accounts", "guest-signin-attempts", "guest-signin-credentials",
  "guest-signin-protection", "guest-signin-restrictions", "license", "network", "notification-providers",
  "operational-alerts", "operators", "pms-events", "pms-interfaces", "pms-reconciliation", "pms-resolutions",
  "pms-roster-reconciliation", "pms-routing", "pms-source-conflicts", "pms-stays", "portal-assets",
  "portal-branding", "portal-https", "reports", "sessions", "social-providers", "payment-providers", "modules", "pms-financial-onboarding", "usage", "walled-garden",
];

async function renderNavWith(surfaces: string[], roles: string[]) {
  CAPS.surfaces = surfaces;
  const { Nav } = await import("@/components/nav");
  render(<Nav roles={roles} email="a@b.c" onLogout={() => {}} />);
}

describe("the navigation contract", () => {
  beforeEach(() => { CAPS.surfaces = null; CAPS.modules = ALL_MODULES; });
  afterEach(() => { cleanup(); CAPS.surfaces = null; CAPS.modules = ALL_MODULES; });

  it("optional sign-in provider screens appear only with their module", async () => {
    CAPS.modules = { hospitality: ALL_MODULES.hospitality };
    await renderNavWith(SERVED, ["site_admin"]);
    expect(screen.queryByText("Identity providers")).toBeNull();
    expect(screen.queryByText("Delivery")).toBeNull();
    expect(screen.getByText("Sign-in methods")).toBeInTheDocument();
    expect(screen.getByText("Client groups")).toBeInTheDocument();
  });

  it("orders Client access as the job is done", async () => {
    await renderNavWith(SERVED, ["site_admin"]);
    const links = Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    const access = ["/sign-in-methods", "/client-groups", "/social-providers", "/notifications", "/portal-branding", "/walled-garden"];
    expect(links.filter((h) => access.includes(h ?? ""))).toEqual(access);
  });

  it("offers every destination the contract requires, on an appliance that serves it", async () => {
    await renderNavWith(SERVED, ["site_admin"]);
    const missing = CONTRACT.required_navigation.entries
      .filter((e: any) => SERVED.includes(e.surface))
      .filter((e: any) => screen.queryAllByText(e.label).length === 0)
      .map((e: any) => `${e.label} (${e.route})`);
    expect(missing, "contract-required destinations missing from the sidebar").toEqual([]);
  });

  it("offers Portal HTTPS under Networking to the roles that may read it", async () => {
    await renderNavWith(SERVED, ["hotel_it_manager"]);
    const links = Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    expect(links).toContain("/portal-https");
    cleanup();
    await renderNavWith(SERVED, ["front_office_operator"]);
    expect(screen.queryByText("Portal HTTPS")).toBeNull();
  });

  it("hides a destination whose surface this appliance does not serve", async () => {
    // Only the appliance saying "I do not run that" may remove a destination, not a build that forgot it.
    await renderNavWith(SERVED.filter((s) => s !== "pms-stays"), ["site_admin"]);
    expect(screen.queryByText("Stays")).toBeNull();
    expect(screen.getByText("Internet packages")).toBeInTheDocument(); // the rest is unaffected
  });

  it("still hides what the role may not read, even when the appliance serves it", async () => {
    await renderNavWith(SERVED, ["payments_operator"]);
    expect(screen.queryByText("Client accounts")).toBeNull();
    expect(screen.queryByText("Operators")).toBeNull();
  });

  it("keeps core destinations while the capability answer is still unknown", async () => {
    // Unknown must not empty the menu: optimistic for the core, and every page enforces its own state.
    await renderNavWith([], ["site_admin"]);
    expect(screen.getByText("Internet packages")).toBeInTheDocument();
    expect(screen.getByText("Modules")).toBeInTheDocument();
  });

  it("offers no licence-controlled destination until the appliance says it serves it", async () => {
    await renderNavWith([], ["site_admin"]);
    expect(screen.queryByText("Stays")).toBeNull();
    expect(screen.queryByText("Room charge")).toBeNull();
  });
});

describe("the mechanism that caused this cannot return", () => {
  it("navigation consults no build-time capability flag", () => {
    // Next inlines NEXT_PUBLIC_* at build time: a flag absent at `next build` is a permanent false and the menu it
    // gates vanishes silently.
    const reads = NAV_SRC.match(/process\.env\.[A-Za-z0-9_]+/g) ?? [];
    expect(reads, "nav.tsx reads a build-time variable again").toEqual([]);
    expect(NAV_SRC).not.toMatch(/enabled:\s*CAP_/);
  });

  it("the contract forbids every capability flag from appearing in the client bundle", () => {
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
    // A menu entry whose page does not exist sends an operator to a 404; this also catches a route renamed on one side
    // only.
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

describe("authorized consolidations stay reachable", () => {
  // PMS reconciliation and Roster reconciliation are deliberately not top-level but must stay discoverable under PMS
  // Connection -> Advanced,
  // with their routes, permissions and attention links intact.
  const PMS_PAGE = readFileSync(join(process.cwd(), "app/(app)/pms-interfaces/page.tsx"), "utf8");
  const DASH = readFileSync(join(process.cwd(), "app/(app)/dashboard/page.tsx"), "utf8");
  const consolidated = CONTRACT.authorized_consolidations.entries;

  it("records a Product-Owner decision, not an agent judgement", () => {
    expect(CONTRACT.authorized_consolidations.decision).toMatch(/Product Owner/i);
  });

  it("keeps each one out of the top-level sidebar, as decided", () => {
    const routes = new Set([...NAV_SRC.matchAll(/href:\s*"(\/[^"]*)"/g)].map((m) => m[1]));
    for (const e of consolidated) {
      expect(e.top_level_sidebar, `${e.label} is recorded as top-level`).toBe(false);
      expect(routes.has(e.route), `${e.label} is back in the sidebar; the decision says it should not be`).toBe(false);
    }
  });

  it("keeps each one linked, with its label, from the required Advanced section", () => {
    expect(PMS_PAGE).toContain("Advanced diagnostics");
    for (const e of consolidated) {
      expect(PMS_PAGE, `${e.label} is no longer linked from PMS Connection -> Advanced`).toContain(`href="${e.route}"`);
      expect(PMS_PAGE, `${e.label} lost its label in the Advanced area`).toContain(e.label);
    }
  });

  it("keeps a direct route from PMS health or the attention list when it matters", () => {
    // Hidden screens are almost always empty; the rare non-empty one must still reach the operator.
    expect(PMS_PAGE).toMatch(/review_events/);
    expect(PMS_PAGE + DASH).toContain('href: "/roster-reconciliation"');
  });

  it("keeps the pages and does not merely link at nothing", () => {
    const { existsSync } = require("node:fs") as typeof import("node:fs");
    for (const e of consolidated) {
      expect(existsSync(join(process.cwd(), `app/(app)${e.route}/page.tsx`)), `${e.label} page is gone`).toBe(true);
    }
  });
});

// The industry-neutral core comes first and everything hospitality/PMS-dependent sits in ONE Hotel section after it
// (docs/PRODUCT_TERMINOLOGY.md).
describe("the Hotel module in the navigation", () => {
  it("orders the sections core-first, then Hotel", async () => {
    const { NAV_SECTION_OF } = await import("@/components/nav");
    expect([...new Set(Object.values(NAV_SECTION_OF))]).toEqual([
      "Overview", "Internet offering", "Clients", "Client access", "Hotel", "Networking", "System",
    ]);
  });

  it("holds every hospitality destination in Hotel and none elsewhere", async () => {
    const { NAV_SECTION_OF } = await import("@/components/nav");
    const hotel = [
      "/pms-interfaces", "/room-sign-in", "/stays", "/stay-events", "/pms-resolutions", "/guest-signin-attempts", "/checkout-grace",
      "/post-stay", "/financial-health", "/financial-review", "/financial-settlements", "/financial-recovery",
      "/pms-routing", "/pms-source-conflicts", "/stay-transfers",
    ];
    for (const href of hotel) expect(NAV_SECTION_OF[href], href).toBe("Hotel");
    const outside = Object.entries(NAV_SECTION_OF).filter(([, s]) => s !== "Hotel").map(([h]) => h);
    expect(outside.filter((h) => /pms|stay|grace|financial|signin|room/.test(h))).toEqual([]);
  });
});