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
