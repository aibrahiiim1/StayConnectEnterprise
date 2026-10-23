import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("next/navigation", () => ({ usePathname: () => "/vouchers" }));

const ALL_MODULES = Object.fromEntries(
  ["hospitality", "paid_access", "card_payment", "room_charge"].map(
    (id) => [id, { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true }]),
);
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return {
    ...actual,
    useCapabilities: () => ({ surfaces: ["commercial-packages", "vouchers", "sessions", "usage", "operators", "audit"], modules: ALL_MODULES as never }),
  };
});

async function renderNav() {
  const { Nav } = await import("@/components/nav");
  render(<Nav roles={["site_admin"]} email="a@b.c" onLogout={() => {}} />);
}

const sectionButton = (name: string) => screen.getByRole("button", { name });

describe("collapsible navigation sections", () => {
  afterEach(() => cleanup());

  it("opens only the section holding the current screen", async () => {
    await renderNav();
    expect(sectionButton("Internet offering")).toHaveAttribute("aria-expanded", "true");
    expect(sectionButton("System")).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("link", { name: "Vouchers" })).toBeVisible();
    expect(screen.queryByRole("link", { name: "Operators" })).toBeNull();
  });

  it("opens and closes a section from its heading", async () => {
    await renderNav();
    fireEvent.click(sectionButton("System"));
    expect(sectionButton("System")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "Operators" })).toBeVisible();
    fireEvent.click(sectionButton("System"));
    expect(screen.queryByRole("link", { name: "Operators" })).toBeNull();
  });

  it("shows every match while filtering", async () => {
    await renderNav();
    fireEvent.change(screen.getByLabelText("Filter navigation"), { target: { value: "o" } });
    expect(screen.getByRole("link", { name: "Operators" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Vouchers" })).toBeVisible();
  });
});
