import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";

// next/link + next/navigation are mocked so the Nav renders in jsdom without the Next runtime.
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("next/navigation", () => ({ usePathname: () => "/" }));

async function renderNav(flag: string | undefined, roles: string[]) {
  vi.resetModules();
  if (flag === undefined) vi.stubEnv("NEXT_PUBLIC_PHASE2_ADMIN", "");
  else vi.stubEnv("NEXT_PUBLIC_PHASE2_ADMIN", flag);
  const { Nav } = await import("@/components/nav");
  render(<Nav roles={roles} email="a@b.c" onLogout={() => {}} />);
}

describe("Nav — Commercial Packages visibility", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => vi.unstubAllEnvs());

  it("hides Commercial Packages when NEXT_PUBLIC_PHASE2_ADMIN is absent/OFF (even for site_admin)", async () => {
    await renderNav(undefined, ["site_admin"]);
    expect(screen.queryByText("Commercial packages")).toBeNull();
  });

  it("hides it when the flag is '0'", async () => {
    await renderNav("0", ["site_admin"]);
    expect(screen.queryByText("Commercial packages")).toBeNull();
  });

  it("shows it when the flag is '1' AND the role can read the resource", async () => {
    await renderNav("1", ["site_admin"]);
    expect(screen.getByText("Commercial packages")).toBeInTheDocument();
  });

  it("still hides it when the flag is '1' but the role cannot read the resource", async () => {
    await renderNav("1", ["payments_operator"]); // no commercial-packages grant
    expect(screen.queryByText("Commercial packages")).toBeNull();
  });
});

// Phase 3 (DARK): the PMS stay/grace nav items follow the same rule as Phase 2 — hidden unless the deployment
// flag is on AND the operator's role can read the resource. edged remains the authority (its routes are absent
// while the backend flags are off), so this only governs what an operator is offered.
describe("Nav — Phase 3 (DARK) stay/grace visibility", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => vi.unstubAllEnvs());

  async function renderWithPhase3(flag: string | undefined, roles: string[]) {
    vi.resetModules();
    vi.stubEnv("NEXT_PUBLIC_PHASE2_ADMIN", "");
    vi.stubEnv("NEXT_PUBLIC_PHASE3_ADMIN", flag === undefined ? "" : flag);
    const { Nav } = await import("@/components/nav");
    render(<Nav roles={roles} email="a@b.c" onLogout={() => {}} />);
  }

  it("hides every Phase-3 item when the flag is absent (even for site_admin)", async () => {
    await renderWithPhase3(undefined, ["site_admin"]);
    for (const label of ["Stays", "Stay events", "Checkout grace", "Operational alerts"]) {
      expect(screen.queryByText(label)).toBeNull();
    }
  });

  it("shows them when the flag is '1' and the role can read them", async () => {
    await renderWithPhase3("1", ["site_admin"]);
    for (const label of ["Stays", "Stay events", "Checkout grace", "Operational alerts"]) {
      expect(screen.getByText(label)).toBeTruthy();
    }
  });

  it("still hides items a role cannot read, even with the flag on", async () => {
    await renderWithPhase3("1", ["payments_operator"]);
    for (const label of ["Stays", "Stay events", "Checkout grace", "Operational alerts"]) {
      expect(screen.queryByText(label)).toBeNull();
    }
  });
});
