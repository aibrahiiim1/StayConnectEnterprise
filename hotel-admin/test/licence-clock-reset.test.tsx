import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), postRaw: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));

beforeEach(() => { get.mockReset(); post.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

const STATUS = {
  activation: "activated", serial: "SC-7F3A-19", appliance_id: "ap-1", customer_name: "Coral Sea", site_name: "Aqua",
  license: { state: "active", valid_until: "2027-06-01T00:00:00Z", grace_ends_at: "2027-06-15T00:00:00Z", days_left: 247,
    max_concurrent_online_guests: 200, current_online_guests: 40 },
  central: { state: "connected", last_contact_at: new Date().toISOString(), last_error: null },
  details: { identity_key_fingerprint: "a1b2", cert_fingerprint: "ff", assignment_version: 4, license_version: 2,
    wan_mac: "00:11:22:33:44:55", lan_mac: "00:11:22:33:44:66", central_endpoint: "https://c.example", software_version: "1.2.3" },
};

function serve(roles: string[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/central/status") return Promise.resolve(STATUS);
    return Promise.resolve({});
  });
}

async function renderPage() {
  const Page = (await import("@/app/(app)/appliance/page")).default;
  render(<Page />);
  await screen.findByRole("region", { name: "Activation" });
}

describe("licence clock reset", () => {
  it("is offered only with the licence write permission", async () => {
    serve(["hotel_it_manager"]); // licence READ only
    await renderPage();
    expect(screen.queryByRole("button", { name: /Reset licence clock/ })).toBeNull();
    cleanup();
    serve(["site_viewer"]);
    await renderPage();
    expect(screen.queryByRole("button", { name: /Reset licence clock/ })).toBeNull();
    cleanup();
    serve(["site_admin"]);
    await renderPage();
    expect(screen.getByRole("button", { name: /Reset licence clock/ })).toBeTruthy();
  });

  it("needs a reason, sends it, and shows previous -> new", async () => {
    serve(["site_admin"]);
    post.mockResolvedValue({ status: "reset", previous_high_water: "2030-01-01T00:00:00Z", high_water: "2026-10-29T12:00:00Z", state: "active" });
    const user = userEvent.setup();
    await renderPage();
    await user.click(screen.getByRole("button", { name: /Reset licence clock/ }));
    const go = await screen.findByRole("button", { name: "Reset licence clock" });
    expect((go as HTMLButtonElement).disabled).toBe(true);
    await user.type(screen.getByLabelText(/Reason/), "Clock was set a year ahead during setup");
    await user.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/license/clock-reset", { reason: "Clock was set a year ahead during setup" }));
    const done = await screen.findByText("Licence clock reset");
    expect(done.parentElement?.textContent).toMatch(/moved from .* to /);
    expect(done.parentElement?.textContent).toContain("2030");
  });

  it("shows the appliance's refusal and keeps the dialog open", async () => {
    serve(["site_admin"]);
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(409, { error: "the local clock still disagrees with Central by 3h; correct the clock first" }));
    const user = userEvent.setup();
    await renderPage();
    await user.click(screen.getByRole("button", { name: /Reset licence clock/ }));
    await user.type(await screen.findByLabelText(/Reason/), "Clock was wrong after power cut");
    await user.click(screen.getByRole("button", { name: "Reset licence clock" }));
    expect(await screen.findByText(/still disagrees with Central by 3h/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reset licence clock" })).toBeTruthy();
    expect(screen.queryByText("Licence clock reset")).toBeNull();
  });
});
