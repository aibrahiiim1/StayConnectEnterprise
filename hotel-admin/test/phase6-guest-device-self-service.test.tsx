// THE PHASE-6 "CLIENT DEVICES" SCREEN IS RETIRED (decision D-K, audit C-1).
//
// Its switch controlled nothing since migration 0118 (the portal's "Your devices" list never reads it), its copy
// was false, and it duplicated Devices in the menu. These tests hold the retirement: the menu never offers it,
// even on an appliance that mounts the dark surface, and the old route explains where devices are managed and
// asks the API for nothing.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("next/navigation", () => ({ usePathname: () => "/dashboard" }));

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a) } };
});

beforeEach(() => { get.mockReset(); put.mockReset(); vi.resetModules(); });

describe("the retired Client devices screen", () => {
  it("is not offered in the menu, even where the appliance serves the dark surface", async () => {
    get.mockImplementation((p: string) =>
      p === "/capabilities"
        ? Promise.resolve({ surfaces: ["sessions", "guest-device-self-service", "reports"], modules: null })
        : Promise.resolve({}));
    const { Nav } = await import("@/components/nav");
    render(<Nav email="a@b.c" roles={["site_admin"]} onLogout={() => {}} />);
    const hrefs = () => Array.from(document.querySelectorAll("a[href]")).map((a) => a.getAttribute("href"));
    await waitFor(() => expect(hrefs()).toContain("/devices"));
    expect(hrefs()).not.toContain("/guest-device-self-service");
    // One menu item for client devices, not two.
    expect(screen.queryByText("Client devices")).toBeNull();
  });

  it("sends an old bookmark to Devices and calls nothing", async () => {
    const Page = (await import("@/app/(app)/guest-device-self-service/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { name: /client devices has moved/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /open devices/i })).toHaveAttribute("href", "/devices");
    // The false claim the old screen made is gone.
    expect(document.body.textContent).not.toMatch(/never be removed/i);
    expect(get).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
  });
});
