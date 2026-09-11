import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// GUEST SIGN-IN ATTEMPTS — the screen, not the API.
//
// What these assert is the part a server test cannot: that an operator WITHOUT the credential permission is
// never shown a value and is told why, that an operator WITH it sees the comparison unmasked, and that a room
// the mirror does not hold produces a stated absence rather than empty fields which read as withheld data.

// jsdom implements no ResizeObserver, and Radix measures its dialog content on mount. Without this the whole
// tree throws during the first render and EVERY assertion fails with a message about the text it could not
// find — which reads as a broken page rather than a missing browser API. Scoped to this file deliberately:
// changing the shared setup would alter the environment twenty other suites already pass in.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
// ApiError is part of the module's RUNTIME surface, not just its types: ErrorBanner does
// `err instanceof ApiError` to decide whether to show a trace id. A mock that omits it makes that check
// throw, React unmounts the tree, and the page renders as an empty div.
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a) } };
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

/** wire mocks the three endpoints the page uses, with the operator's roles as the only variable. */
function wire(roles: string[], overrides: Record<string, unknown> = {}) {
  get.mockImplementation((raw?: unknown) => {
    // The harness invokes the spy once with no arguments while tearing the test down. Reading `.startsWith`
    // on that call throws INSIDE the cleanup hook, which vitest reports as a failure of the test that had
    // already passed — a teardown artefact wearing the costume of a product defect. Coercing first costs
    // nothing and keeps the failure output about the page.
    const path = typeof raw === "string" ? raw : "";
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path.startsWith("/guest-signin-attempts?")) {
      return Promise.resolve({ data: [ATTEMPT], meta: { has_more: false } });
    }
    if (path.startsWith("/guest-signin-attempts/")) {
      return Promise.resolve({ ...DETAIL, ...overrides });
    }
    if (path.startsWith("/guest-signin-credentials/")) return Promise.resolve(CREDENTIALS);
    return Promise.resolve({ data: [], meta: { has_more: false } });
  });
}

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-signin-attempts/page");
  render(<Page />);
  return userEvent.setup();
}

beforeEach(() => get.mockReset());
afterEach(() => vi.resetModules());

describe("guest sign-in attempts", () => {
  it("lists an attempt with its room, network, plain-language reason and mirror age", async () => {
    wire(["site_admin"]);
    await renderPage();

    expect(await screen.findByText("412")).toBeTruthy();
    expect(screen.getByText("Guest WiFi")).toBeTruthy();
    expect(screen.getByText("Room found, submitted value did not match")).toBeTruthy();
    expect(screen.getByText("Refused")).toBeTruthy();
    // The device is present as SECONDARY information, which is what the Product Owner asked for.
    expect(screen.getByText("10.77.1.25")).toBeTruthy();
  });

  // THE COMPARISON, UNMASKED. Anything less answers nothing: "OK••••••" beside "OK•••••••" is not a
  // comparison, and the operators who reach this screen already hold the guest's room and stay.
  it("shows an authorised operator the submitted and accepted values in full", async () => {
    wire(["front_office_operator"]);
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(await screen.findByText("Nottheguest")).toBeTruthy();
    expect(screen.getByText("MARIA DEL CARMEN")).toBeTruthy();
    expect(screen.getByText("OKONKWO")).toBeTruthy();
    expect(screen.getByText("RES-4001")).toBeTruthy();
    // Nothing is redacted. If a masking character ever appears here, the panel has stopped answering.
    expect(screen.queryByText(/•/)).toBeNull();
    expect(screen.queryByText(/\*{3,}/)).toBeNull();
  });

  // ...and an operator WITHOUT the second permission sees no value and is told which permission is missing,
  // rather than an empty box that reads as a bug.
  it("tells an operator without the credential permission why the values are absent", async () => {
    wire(["site_viewer"]);
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(await screen.findByText(/do not have permission/i)).toBeTruthy();
    expect(screen.getByText(/View_Guest_SignIn_Credentials/)).toBeTruthy();
    for (const secret of ["Nottheguest", "MARIA DEL CARMEN", "OKONKWO", "RES-4001"]) {
      expect(screen.queryByText(secret)).toBeNull();
    }
    // The page must never even ASK for values it may not show — a request that 403s is still a request that
    // put the attempt id in an access log for no reason.
    expect(get.mock.calls.some((c) => String(c[0]).startsWith("/guest-signin-credentials/"))).toBe(false);
  });

  // NO EXPECTED VALUES ARE INVENTED. A room the mirror does not hold has nothing that would have been
  // accepted, and the screen says so in the Product Owner's words rather than showing blanks.
  it("states the absence plainly when the mirror holds no stay for the room", async () => {
    wire(["site_admin"], {
      result: "ROOM_NOT_IN_MIRROR",
      result_label: "Room not in the local mirror",
      room_in_mirror: false,
      eligible_stay_candidates: 0,
    });
    const user = await renderPage();

    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(await screen.findByText(/No eligible stay for this room exists in the local mirror/i)).toBeTruthy();
    expect(screen.getByText(/Changes made in the PMS after the displayed last-sync time/i)).toBeTruthy();
    // What the guest typed IS still shown — it is the one thing that is genuinely known here.
    expect(screen.getByText("Nottheguest")).toBeTruthy();
    // ...and nothing is presented as having been expected.
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
      // Only the page's own call is made to fail. The teardown call described above must not reject, or an
      // unhandled rejection from the cleanup hook is reported instead of the assertion.
      if (path === "") return Promise.resolve({});
      return Promise.reject(new Error("boom"));
    });
    await renderPage();
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});
