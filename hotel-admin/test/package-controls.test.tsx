import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup, fireEvent } from "@testing-library/react";

// Staff End Package / Reset Package (0202): the shared control on the Stay, session, Client Account and device views.

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() } };
});
let roles: string[] = ["hotel_it_manager"];
vi.mock("@/lib/whoami-context", () => ({ useOperatorRoles: () => roles }));

const ENT = "0f0e0d0c-0b0a-4908-8706-050403020100";
const row = { entitlement_id: ENT, status: "ACTIVE", package_name: "Room Wi-Fi", end_mode: "VALIDITY_WINDOW", window_ends_at: "2026-11-02T10:00:00Z" };
const preview = {
  entitlement_id: ENT, status: "ACTIVE", subject_kind: "STAY", subject_id: "s1", stay_id: "s1", package_name: "Room Wi-Fi",
  package_code: "ROOM", plan_name: "Standard", down_kbps: 20000, up_kbps: 5000, max_devices: 3, end_mode: "VALIDITY_WINDOW",
  data_quota_bytes: 1e9, bytes_used: 1234, window_ends_at: "2026-11-02T10:00:00Z", devices_online: 2, sessions_live: 2,
  purchase_trigger: "GUEST_SELECTION", paid_minor: 500, currency: "EUR",
  stay: { status: "IN_HOUSE", room: "301", arrival: "2026-10-30", departure: "2026-11-03", pms_interface: "Protel" },
  end: { allowed: true }, reset: { allowed: true, window_ends_at: "2026-11-02T15:00:00Z", data_quota_bytes: 1e9 },
};

beforeEach(() => {
  get.mockReset(); post.mockReset();
  roles = ["hotel_it_manager"];
  get.mockImplementation((path: string) =>
    Promise.resolve(path.startsWith("/package-controls?") ? { packages: [row] } : { package: preview, history: [] }));
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderControls() {
  const { PackageControls } = await import("@/components/package-controls");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><PackageControls subject={{ kind: "stay", id: "s1" }} /></ToastProvider>);
  await screen.findByText("Room Wi-Fi");
}

function fill(reason: string, password: string) {
  fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: reason } });
  fireEvent.change(screen.getByLabelText(/Confirm your password/), { target: { value: password } });
}

describe("Package controls", () => {
  it("lists the subject's packages and offers End and Reset to a writer", async () => {
    await renderControls();
    expect(get).toHaveBeenCalledWith("/package-controls?stay=s1");
    expect(screen.getByRole("button", { name: "End package" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reset package" })).toBeTruthy();
  });

  it("shows a read-only role the packages without any action", async () => {
    roles = ["site_viewer"];
    await renderControls();
    expect(screen.queryByRole("button", { name: "End package" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset package" })).toBeNull();
  });

  it("End names the stay and impact, needs a reason and the password, and sends one request id", async () => {
    post.mockResolvedValue({ outcome: "ENDED", sessions_ended: 2, kernel: { summary: "withdrawn", withdrawn: 2, requested: 2 } });
    await renderControls();
    fireEvent.click(screen.getByRole("button", { name: "End package" }));
    await screen.findByText("End this package?");
    expect(screen.getByText(/room 301/)).toBeTruthy();
    expect(screen.getByText(/The whole package ends now/)).toBeTruthy();
    expect(screen.getByText(/A free package is not taken again automatically/)).toBeTruthy();
    fill("guest asked", "operator-pw");
    fireEvent.click(screen.getAllByRole("button", { name: "End package" }).at(-1)!);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe(`/package-controls/${ENT}/end`);
    expect(body).toMatchObject({ reason: "guest asked", password: "operator-pw" });
    expect(body.request_id).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("Reset says it is free and never charges, posts or refunds", async () => {
    post.mockResolvedValue({ window_ends_at: "2026-11-02T15:00:00Z", sessions_rebound: 2, devices: 2 });
    await renderControls();
    fireEvent.click(screen.getByRole("button", { name: "Reset package" }));
    await screen.findByText("Reset this package?");
    expect(screen.getByText(/Free of charge: no payment, charge, room posting, refund or voucher redemption/)).toBeTruthy();
    expect(screen.getByText(/the reset is not charged and nothing is refunded/)).toBeTruthy();
    fill("connection problems", "operator-pw");
    fireEvent.click(screen.getByRole("button", { name: "Reset package (free)" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/package-controls/${ENT}/reset`, expect.objectContaining({ reason: "connection problems" })));
  });

  it("a reset the server refuses is shown, not sent", async () => {
    get.mockImplementation((path: string) => Promise.resolve(path.startsWith("/package-controls?") ? { packages: [row] }
      : { package: { ...preview, reset: { allowed: false, reason: "PKG_STAY_NOT_IN_HOUSE: the stay has checked out" } }, history: [] }));
    await renderControls();
    fireEvent.click(screen.getByRole("button", { name: "Reset package" }));
    await screen.findAllByText(/the stay has checked out/);
    fill("try anyway", "operator-pw");
    fireEvent.click(screen.getByRole("button", { name: "Reset package (free)" }));
    await waitFor(() => expect(screen.getAllByText(/the stay has checked out/).length).toBeGreaterThan(0));
    expect(post).not.toHaveBeenCalled();
  });
});
