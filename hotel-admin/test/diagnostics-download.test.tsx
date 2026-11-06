import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";

const get = vi.fn();
let roles: string[] = ["hotel_it_manager"];
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() } };
});
vi.mock("@/lib/whoami-context", async () => {
  const actual = await vi.importActual<typeof import("@/lib/whoami-context")>("@/lib/whoami-context");
  return { ...actual, useOperatorRoles: () => roles };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/health",
}));

const SUMMARY = { overall: "healthy", counts: { healthy: 1 }, services: [], boot: null, checks: [], generated_at: new Date().toISOString() };

beforeEach(() => {
  get.mockReset();
  roles = ["hotel_it_manager"];
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage(bundle: () => Promise<unknown>) {
  get.mockImplementation((path: string) => (path === "/diagnostics/bundle" ? bundle() : Promise.resolve(SUMMARY)));
  const Page = (await import("@/app/(app)/health/page")).default;
  render(<Page />);
}

describe("Download diagnostics", () => {
  it("names the file by its UTC time", async () => {
    const { diagnosticsFileName } = await import("@/lib/diagnostics-bundle");
    expect(diagnosticsFileName(new Date(Date.UTC(2026, 10, 6, 7, 30, 5)))).toBe("onegate-diagnostics-20261106-073005Z.json");
  });

  it("downloads the file and says what to do with it", async () => {
    await renderPage(() => Promise.resolve({ format: "onegate-diagnostics/1" }));
    fireEvent.click(await screen.findByRole("button", { name: "Download diagnostics" }));
    expect(await screen.findByText("Diagnostics downloaded")).toBeTruthy();
    expect(screen.getByText(/onegate-diagnostics-\d{8}-\d{6}Z\.json/)).toBeTruthy();
    expect(screen.getByText(/when you ask your OneGate vendor for help/)).toBeTruthy();
    expect(URL.createObjectURL).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Diagnostics downloaded")).toBeNull();
  });

  it("says so in words when the file cannot be made", async () => {
    const { ApiError } = await import("@/lib/api");
    await renderPage(() => Promise.reject(new ApiError(500, { error: "bundle_failed", message: "x" })));
    fireEvent.click(await screen.findByRole("button", { name: "Download diagnostics" }));
    expect(await screen.findByText("The diagnostics file could not be made. Check the appliance is reachable, then try again.")).toBeTruthy();
    expect(screen.queryByText("Diagnostics downloaded")).toBeNull();
  });

  it("is not offered to a role that cannot read the logs", async () => {
    roles = ["site_viewer"];
    await renderPage(() => Promise.resolve({}));
    expect(await screen.findByText(/or download the diagnostics file/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Download diagnostics" })).toBeNull();
  });
});
