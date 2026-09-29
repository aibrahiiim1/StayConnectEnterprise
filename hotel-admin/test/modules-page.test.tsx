// SYSTEM → MODULES on a site licensed for nothing optional (a café that was once a hotel).
//
// Pinned: every module the appliance reports has a card (WhatsApp included, and an unknown one too); an
// unlicensed module shows "—" for switched on and ready rather than a Yes that means nothing; no configure link
// is offered for an unlicensed module; hotel records still served are reachable from the Hotel card.

import { describe, it, expect, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => ({ surfaces: ["pms-stays", "pms-events", "modules"], modules: {} }) };
});
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import ModulesPage from "@/app/(app)/modules/page";

const mod = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, requires: [], deployed: true, authorized: false, licensed: false, switchable: false,
  enabled: false, ready: true, effective: false, manageable: false, reasons: ["NOT_LICENSED"], ...extra,
});

describe("Modules on a café", () => {
  it("shows every module, dashes where the licence is missing, no configure link, and the kept hotel records", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      return Promise.resolve({
        site_type: "CAFE", license_state: "Active", deployment: {},
        modules: {
          hospitality: mod("hospitality", "Hotel", { switchable: true, enabled: true, manageable: true }),
          whatsapp_otp: mod("whatsapp_otp", "WhatsApp one-time code"),
          future_mod: mod("future_mod", "Something new"),
        },
      });
    });
    render(<ModulesPage />);
    expect(await screen.findByText("WhatsApp one-time code")).toBeInTheDocument();
    expect(screen.getByText("Something new")).toBeInTheDocument();

    const hotel = screen.getByText("Hotel").closest("div.flex.flex-col, [class*='flex-col']") as HTMLElement;
    // Available is a fact (Yes); switched on and ready answer nothing without the licence.
    expect(within(hotel).getAllByText("—", { selector: "dd" })).toHaveLength(2);
    expect(within(hotel).queryByText("PMS connection")).toBeNull();
    const records = screen.getByTestId("records-hospitality");
    expect(within(records).getByRole("link", { name: "Stays" })).toHaveAttribute("href", "/stays");
    expect(within(records).getByRole("link", { name: "PMS activity" })).toBeInTheDocument();
    expect(within(records).queryByRole("link", { name: "Guest sign-in attempts" })).toBeNull();
  });
});
