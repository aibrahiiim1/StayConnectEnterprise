import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

// The menu must not offer what this appliance cannot serve (the bundle compiles every capability in; the appliance
// mounts a subset).

vi.mock("next/navigation", () => ({ usePathname: () => "/dashboard" }));

// The deployed bundle has every capability compiled in; without these flags the assertions would pass vacuously.
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

const PRELIVE_SURFACES = [
  "reports", "sessions", "guest-accounts", "auth-methods", "walled-garden", "portal-branding",
  "notification-providers", "social-providers", "network", "diagnostics", "audit", "license", "backups",
  "operators", "commercial-packages", "checkout-grace", "pms-stays", "pms-events", "pms-resolutions",
  "guest-signin-attempts", "pms-interfaces", "pms-routing", "pms-source-conflicts", "operational-alerts",
];

function mockCaps(surfaces: string[], modules: Record<string, unknown> | null = null) {
  get.mockImplementation((p: string) => {
    if (p === "/capabilities") return Promise.resolve({ surfaces, modules });
    return Promise.resolve({});
  });
}

const HOTEL = { hospitality: { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true } };

describe("navigation follows the appliance, not the build", () => {
  it("hides destinations this appliance does not serve", async () => {
    mockCaps(PRELIVE_SURFACES, HOTEL);
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);

    // Asserted on links by destination: label text is ambiguous in a rail that also renders collapsed spans.
    const hrefs = () =>
      Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));

    await waitFor(() => expect(hrefs()).toContain("/sessions"));
    expect(hrefs()).toContain("/stays");

    for (const gone of [
      "/guest-device-self-service", "/online-time", "/post-stay", "/stay-transfers",
      "/financial-health", "/financial-review", "/financial-settlements", "/financial-recovery",
    ]) {
      await waitFor(() => expect(hrefs()).not.toContain(gone));
    }
  });

  it("shows everything when the appliance serves everything", async () => {
    mockCaps([...PRELIVE_SURFACES, "guest-device-self-service", "post-stay-profiles", "stay-transfers", "financial-review"], HOTEL);
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);
    const hrefs = () =>
      Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    await waitFor(() => expect(hrefs()).toContain("/post-stay"));
    expect(hrefs()).toContain("/financial-health");
  });

  it("without Hospitality licensed there is no Hotel section, even where hotel records are served", async () => {
    mockCaps(PRELIVE_SURFACES, { hospitality: { deployed: true, licensed: false, enabled: true, ready: true, effective: false, manageable: true } });
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);
    const hrefs = () => Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    await waitFor(() => expect(hrefs()).toContain("/sessions"));
    for (const h of ["/stays", "/stay-events", "/pms-resolutions", "/guest-signin-attempts", "/pms-interfaces", "/room-sign-in"]) {
      expect(hrefs()).not.toContain(h);
    }
    expect(document.body.textContent).not.toMatch(/Hotel/);
  });

  it("fails open when the appliance cannot say", async () => {
    // An appliance that cannot answer is not one with no features: do not empty the menu.
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
    get.mockResolvedValue({ surfaces: PRELIVE_SURFACES });
    const { SurfaceNotEnabled } = await import("@/components/surface-not-enabled");
    render(<SurfaceNotEnabled label="Charge health" />);

    expect(screen.getByRole("heading", { name: "Charge health" })).toBeTruthy();
    expect(screen.getByText("Not enabled on this appliance")).toBeTruthy();
    expect(screen.getByText(/configuration of the appliance, not a fault/)).toBeTruthy();
    expect(screen.getByText(/Client internet, sign-in, sessions and accounting are unaffected/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/PMS/);
    expect(screen.getByRole("link", { name: /Back to the dashboard/ })).toBeTruthy();
  });

  it("names the PMS connection among what is unaffected only where hospitality is licensed", async () => {
    const on = { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true };
    get.mockImplementation((p: string) =>
      Promise.resolve(p === "/capabilities" ? { surfaces: PRELIVE_SURFACES, modules: { hospitality: on } } : {}));
    const { SurfaceNotEnabled } = await import("@/components/surface-not-enabled");
    render(<SurfaceNotEnabled label="Charge health" />);
    expect(await screen.findByText(/Client internet, sign-in, the PMS connection, sessions and accounting are unaffected/)).toBeTruthy();
  });
});
