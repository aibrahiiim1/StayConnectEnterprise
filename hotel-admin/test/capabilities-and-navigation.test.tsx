import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

// THE MENU MUST NOT OFFER WHAT THIS APPLIANCE CANNOT SERVE.
//
// The operator sweep of PRE-LIVE found eight destinations that answered 404 and rendered a blank page:
// Guest devices, Online-time budgets, Post-stay access, Cross-PMS transfer, Charge health, Manual review,
// Settlements and Recovery. The bundle was built with every capability compiled in; the appliance mounts a
// subset; nothing reconciled the two. An operator cannot tell "this hotel does not have that" from "this is
// broken", and the second reading is the reasonable one from where they stand.

// The Nav asks where it is so it can mark the active item; jsdom has no router.
vi.mock("next/navigation", () => ({ usePathname: () => "/dashboard" }));

// THE DEPLOYED BUNDLE HAS EVERY CAPABILITY COMPILED IN -- that is the whole premise of the defect. Without
// these the build flags are off here, every gated item is hidden for the wrong reason, and the assertions
// below would pass while proving nothing.
for (const f of ["PHASE2", "PHASE3", "PHASE4", "PHASE5", "PHASE6"]) {
  vi.stubEnv(`NEXT_PUBLIC_${f}_ADMIN`, "1");
}

const get = vi.fn();
vi.mock("@/lib/api", () => ({
  api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => { get.mockReset(); vi.resetModules(); });
afterEach(() => vi.resetModules());

/** Every surface a fully-enabled appliance mounts, minus the ones PRE-LIVE does not. */
const PRELIVE_SURFACES = [
  "reports", "sessions", "guest-accounts", "auth-methods", "walled-garden", "portal-branding",
  "notification-providers", "social-providers", "network", "diagnostics", "audit", "license", "backups",
  "operators", "commercial-packages", "checkout-grace", "pms-stays", "pms-events", "pms-resolutions",
  "guest-signin-attempts", "pms-interfaces", "pms-routing", "pms-source-conflicts", "operational-alerts",
];

function mockCaps(surfaces: string[]) {
  get.mockImplementation((p: string) => {
    if (p === "/capabilities") return Promise.resolve({ surfaces });
    return Promise.resolve({});
  });
}

describe("navigation follows the appliance, not the build", () => {
  it("hides destinations this appliance does not serve", async () => {
    mockCaps(PRELIVE_SURFACES);
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);

    // Asserted on the LINKS, by destination. Matching on label text is ambiguous in a rail that also renders
    // collapsed spans, and what matters is whether the operator can reach the page at all.
    const hrefs = () =>
      Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));

    await waitFor(() => expect(hrefs()).toContain("/sessions"));
    expect(hrefs()).toContain("/stays");

    // Absent: the eight that 404'd on PRE-LIVE.
    for (const gone of [
      "/guest-device-self-service", "/online-time", "/post-stay", "/stay-transfers",
      "/financial-health", "/financial-review", "/financial-settlements", "/financial-recovery",
    ]) {
      await waitFor(() => expect(hrefs()).not.toContain(gone));
    }
  });

  it("shows everything when the appliance serves everything", async () => {
    mockCaps([...PRELIVE_SURFACES, "guest-device-self-service", "post-stay-profiles", "stay-transfers", "financial-review"]);
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);
    const hrefs = () =>
      Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    await waitFor(() => expect(hrefs()).toContain("/post-stay"));
    expect(hrefs()).toContain("/financial-health");
  });

  it("fails open when the appliance cannot say", async () => {
    // An appliance that cannot answer is not an appliance with no features. Emptying the menu on a failed
    // request would turn one unavailable endpoint into a product with no navigation.
    get.mockImplementation(() => Promise.reject(new Error("offline")));
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);
    const hrefs = () =>
      Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    await waitFor(() => expect(hrefs()).toContain("/sessions"));
    expect(hrefs()).toContain("/dashboard");
  });
});

describe("a destination that is not enabled explains itself", () => {
  it("says so, says what is unaffected, and does not look like a fault", async () => {
    const { SurfaceNotEnabled } = await import("@/components/surface-not-enabled");
    render(<SurfaceNotEnabled label="Charge health" />);

    expect(screen.getByText(/Charge health is not enabled on this appliance/)).toBeTruthy();
    expect(screen.getByText(/configuration of the appliance, not a fault/)).toBeTruthy();
    // The question behind every unexpected screen in an admin.
    expect(screen.getByText(/Guest internet, sign-in, the PMS connection, sessions and accounting are unaffected/)).toBeTruthy();
    // And a way out, rather than a dead end.
    expect(screen.getByRole("link", { name: /Back to the dashboard/ })).toBeTruthy();
  });
});
