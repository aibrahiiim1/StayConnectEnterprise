import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// Screen behaviour: an operator without the credential permission never sees a value and is told why; one with it sees
// the comparison unmasked;
// a room the mirror does not hold gives a stated absence, not empty fields.

// jsdom implements no ResizeObserver and Radix measures dialog content on mount; scoped to this file rather than the
// shared setup.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
// ApiError is runtime surface: ErrorBanner does `err instanceof ApiError`, so a mock that omits it unmounts the tree.
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a) } };
});

const ATTEMPT = {
  id: "a1",
  occurred_at: new Date().toISOString(),
  room: "412",
  guest_network: "Guest WiFi",
  result: "CREDENTIAL_MISMATCH",
  result_label: "Room found, submitted value did not match",
  succeeded: false,
  verifier_kind: "FULL_NAME",
  mirror_age_seconds: 3600,
  pms_transport_status: "DISCONNECTED",
  device_ip: "10.77.1.25",
  device_mac: "02:00:00:aa:bb:cc",
  request_id: "11111111-1111-4111-8111-111111111111",
  latency_ms: 42,
};

const DETAIL = {
  ...ATTEMPT,
  matched_field: "",
  room_in_mirror: true,
  eligible_stay_candidates: 1,
  mirror_last_complete_sync_at: new Date(Date.now() - 3_600_000).toISOString(),
  credentials_available: true,
};

const CREDENTIALS = {
  available: true,
  submitted_verifier: "Nottheguest",
  normalized_verifier: "NOTTHEGUEST",
  accepted_first_name: "MARIA DEL CARMEN",
  accepted_family_name: "OKONKWO",
  accepted_reservation_number: "RES-4001",
  additional_accepted_guests: 1,
};

function wire(roles: string[], overrides: Record<string, unknown> = {}) {
  get.mockImplementation((raw?: unknown) => {
    // The harness invokes the spy once with no arguments during teardown; coerce first so `.startsWith` does not throw
    // in the cleanup hook.
    const path = typeof raw === "string" ? raw : "";
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path.startsWith("/guest-signin-attempts?")) {
      return Promise.resolve({ data: [ATTEMPT], meta: { has_more: false }, page: 1, page_size: 50, total: 1,
        summary: { total: 1, failed: 1, mismatch: 1, systemic: 0 } });
    }
    if (path.startsWith("/guest-signin-attempts/")) {
      return Promise.resolve({ ...DETAIL, ...overrides });
    }
    return Promise.resolve({ data: [], meta: { has_more: false } });
  });
  post.mockImplementation((path: string) =>
    path.startsWith("/guest-signin-credentials/") ? Promise.resolve(CREDENTIALS) : Promise.resolve({}));
}

async function revealWithPassword(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText("Your password"), "my-own-password");
  await user.click(screen.getByRole("button", { name: /Show what was entered/ }));
}

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-signin-attempts/page");
  render(<Page />);
  return userEvent.setup();
}

beforeEach(() => { get.mockReset(); post.mockReset(); });
afterEach(() => vi.resetModules());

describe("guest sign-in attempts", () => {
  it("lists an attempt with its room, network, plain-language reason and mirror age", async () => {
    wire(["site_admin"]);
    await renderPage();

    expect(await screen.findByText("412")).toBeTruthy();
    expect(screen.getByText("Guest WiFi")).toBeTruthy();
    expect(screen.getByText("Room found, submitted value did not match")).toBeTruthy();
    expect(screen.getByText("Refused")).toBeTruthy();
    expect(screen.getByText("10.77.1.25")).toBeTruthy();
  });

  // The comparison is unmasked: masked values beside each other compare nothing.
  it("shows an authorised operator the submitted and accepted values in full", async () => {
    wire(["front_office_operator"]);
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));
    expect(screen.queryByText("Nottheguest")).toBeNull();
    expect(post).not.toHaveBeenCalled();
    await revealWithPassword(user);
    expect(post).toHaveBeenCalledWith("/guest-signin-credentials/" + ATTEMPT.id, { password: "my-own-password" });

    expect(await screen.findByText("Nottheguest")).toBeTruthy();
    expect(screen.getByText("MARIA DEL CARMEN")).toBeTruthy();
    expect(screen.getByText("OKONKWO")).toBeTruthy();
    expect(screen.getByText("RES-4001")).toBeTruthy();
    expect(screen.queryByText(/•/)).toBeNull();
    expect(screen.queryByText(/\*{3,}/)).toBeNull();
  });

  it("tells an operator without the credential permission why the values are absent", async () => {
    wire(["site_viewer"]);
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(await screen.findByText(/do not have permission/i)).toBeTruthy();
    expect(screen.getByText(/allowed to see guest sign-in details/)).toBeTruthy();
    for (const secret of ["Nottheguest", "MARIA DEL CARMEN", "OKONKWO", "RES-4001"]) {
      expect(screen.queryByText(secret)).toBeNull();
    }
    // The page must never ASK for values it may not show: a 403 request still lands the attempt id in an access log.
    expect(post.mock.calls.some((c) => String(c[0]).startsWith("/guest-signin-credentials/"))).toBe(false);
    expect(screen.queryByLabelText("Your password")).toBeNull();
  });

  // No expected values are invented for a room the mirror does not hold.
  it("states the absence plainly when the mirror holds no stay for the room", async () => {
    wire(["site_admin"], {
      result: "ROOM_NOT_IN_MIRROR",
      result_label: "Room not in the local mirror",
      room_in_mirror: false,
      eligible_stay_candidates: 0,
    });
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    await revealWithPassword(user);
    expect(await screen.findByText(/No eligible stay for this room exists in the appliance's guest list/i)).toBeTruthy();
    expect(screen.getByText(/Changes made in the PMS after the displayed last-sync time/i)).toBeTruthy();
    expect(screen.getByText("Nottheguest")).toBeTruthy();
    expect(screen.queryByText("OKONKWO")).toBeNull();
  });

  it("says so, without blaming a permission, when no values were recorded at all", async () => {
    wire(["site_admin"], { credentials_available: false });
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(await screen.findByText(/No values were recorded for this attempt/i)).toBeTruthy();
    expect(screen.queryByText(/do not have permission/i)).toBeNull();
  });

  it("narrows the query server-side when a room is entered", async () => {
    wire(["site_admin"]);
    const user = await renderPage();
    await screen.findByText("412");

    await user.type(screen.getByLabelText("Filter by room number"), "905");
    await waitFor(() =>
      expect(get.mock.calls.some((c) => String(c[0]).includes("room=905"))).toBe(true));
  });

  it("reports a failed load instead of rendering an empty table", async () => {
    get.mockImplementation((raw?: unknown) => {
      const path = typeof raw === "string" ? raw : "";
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      // Only the page's own call fails; the teardown call must not reject.
      if (path === "") return Promise.resolve({});
      return Promise.reject(new Error("boom"));
    });
    await renderPage();
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});
