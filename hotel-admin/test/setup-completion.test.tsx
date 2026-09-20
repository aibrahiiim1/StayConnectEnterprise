import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";

// SETUP MUST NOT WAIT FOR A TRANSPORT THAT IS DELIBERATELY NEVER OPENED.
//
// PRE-LIVE sat on "License active — finishing up…" indefinitely. Nothing was wrong with it: enrolled,
// assigned, adopted, certificate issued, API mTLS ready, licence Active. The page required
// `nats_mtls.connected` to declare setup complete, and this appliance serves Central for LICENSING ONLY
// (T0071) — the NATS transport is not opened at all, by decision. So the condition could never become true
// and the wizard reported an unfinished setup forever.
//
// The fixture below is the EXACT shape the live appliance returns, read from it. If someone reinstates a
// NATS term in the completion rule, this test fails with the same symptom the operator saw.

const get = vi.fn();
vi.mock("@/lib/api", () => ({
  api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  ApiError: class ApiError extends Error {
    status?: number;
  },
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));

beforeEach(() => get.mockReset());
afterEach(() => vi.resetModules());

/** Read from PRE-LIVE 172.21.60.25 on 2026-09-17: a correctly activated, licensing-only appliance. */
const PRE_LIVE = {
  serial: "SC-TEST",
  activation_status: "activated",
  enrolled: true,
  locked: true,
  appliance_id: "ap-1",
  assignment: { assigned: true, adopted_at: "2026-09-01T00:00:00Z" },
  api_mtls: { mtls_ready: true, cert_fingerprint: "ab:cd" },
  nats_mtls: { connected: false },
  license: { state: "Active" },
  network: {},
};

function mock(status: Record<string, any>) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/setup/status") return Promise.resolve(status);
    return Promise.resolve({});
  });
}

async function renderSetup() {
  // Activation is a SECTION of the consolidated Appliance & licence screen now, not its own page.
  const Page = (await import("@/app/(app)/appliance/setup-section")).ApplianceSetupSection;
  render(<Page />);
}

describe("appliance setup reports completion honestly", () => {
  it("is COMPLETE on a licensing-only appliance, where NATS never connects", async () => {
    mock(PRE_LIVE);
    await renderSetup();

    // The symptom, asserted as absent. This string is what PRE-LIVE showed forever.
    expect(await screen.findByText(/All set|connected/i)).toBeTruthy();
    expect(screen.queryByText(/finishing up/i)).toBeNull();
  });

  it("still reports IN PROGRESS when the licence really has not arrived", async () => {
    // The fix must not turn the wizard into something that always says "done". Remove the licence and the
    // page must go back to reporting an unfinished setup.
    mock({ ...PRE_LIVE, activation_status: "pending_activation", license: { state: "Unlicensed" } });
    await renderSetup();
    await screen.findByText(/Appliance setup|Connect/i);
    // The page must NOT claim completion. Asserted on the completion sentence itself rather than on the
    // phase labels, which also appear in the navigation and would match for the wrong reason.
    expect(screen.queryByText(/All set — this appliance is connected/i)).toBeNull();
  });

  it("does not report a deliberately closed real-time channel as a fault", async () => {
    // A red "Disconnected" badge sent operators looking for a network problem that does not exist. The row
    // now states what is true: the channel is not used at this site, on purpose.
    mock(PRE_LIVE);
    await renderSetup();
    // The real control, by the label it actually carries.
    const details = await screen.findByRole("button", { name: /Advanced \/ recovery/i });
    details.click();
    const diag = await screen.findByRole("button", { name: /Diagnostics \(/i });
    diag.click();

    expect(await screen.findByText(/Not used at this site/i)).toBeTruthy();
    expect(screen.queryByText(/NATS mTLS connected/i)).toBeNull();
    expect(screen.getByText(/licensing only/i)).toBeTruthy();
  });
});
