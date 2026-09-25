// THE GUESTS AND PMS SCREENS, AFTER THE VELONET REDESIGN.
//
// The presentation of these screens changed completely; their behaviour was not allowed to. So what is asserted
// here is the behaviour the redesign had to carry over — and the three things it had to add:
//
//   * one-time secrets are the kit's OneTimeReveal ("Shown once"), never a card that can scroll away;
//   * the heaviest action keeps every guard it had (reason, password, typed REVOKE) and sends the same body;
//   * a role that cannot use a button never sees it (Active sessions, Guest accounts, Post-stay access);
//   * raw ids are not the main content (Cross-PMS transfer).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), patch: vi.fn(), del: vi.fn() },
  };
});

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});
afterEach(() => vi.resetModules());

const pathOf = (raw: unknown) => (typeof raw === "string" ? raw : "");

// ---------------------------------------------------------------------------------------------------- post-stay

const PROFILE = {
  id: "p-1",
  stay_id: "stay-uuid-1",
  origin_lifecycle_version: 2,
  external_reservation_id: "R-4001",
  normalized_room_number: "412",
  stay_status: "CHECKED_OUT",
  status: "ACTIVE",
  pin_generation: 1,
  issued_via: "CHECKOUT",
  issued_at: "2026-09-20T10:00:00Z",
  valid_until: "2026-09-27T10:00:00Z",
  revoked_at: null,
  revoke_reason: null,
  authenticable: true,
};

describe("Post-stay access", () => {
  beforeEach(() => {
    get.mockImplementation((raw: unknown) =>
      pathOf(raw) === "/post-stay-profiles/" ? Promise.resolve({ profiles: [PROFILE] }) : Promise.resolve({}),
    );
  });

  it("shows the reset PIN once, in the one-time reveal, after a reason and password", async () => {
    post.mockResolvedValue({ pin: "482913", valid_until: "2026-09-27T10:00:00Z" });
    const { PostStayView } = await import("@/components/phase5/post-stay-view");
    const user = userEvent.setup();
    render(<PostStayView canAct />);

    await user.click(await screen.findByRole("button", { name: "Reset PIN" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Reset PIN" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Reason/), "Guest lost the printout");
    await user.type(within(dialog).getByLabelText(/Confirm your password/), "hunter22");
    await user.click(submit);

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/post-stay-profiles/p-1/reset", {
        password: "hunter22",
        reason: "Guest lost the printout",
      }),
    );
    expect(await screen.findByText("New PIN — shown once")).toBeTruthy();
    expect(screen.getByTestId("one-time-value").textContent).toBe("482913");
    expect(screen.getByText(/Shown once\./)).toBeTruthy();
    // Acknowledging closes it for good: there is no way to show it again.
    await user.click(screen.getByRole("button", { name: "I have given it to the guest" }));
    await waitFor(() => expect(screen.queryByTestId("one-time-value")).toBeNull());
    expect(screen.queryByRole("button", { name: /show pin/i })).toBeNull();
  });

  it("ends access only with a reason of 4+ characters, the password AND the typed word REVOKE", async () => {
    post.mockResolvedValue({});
    const { PostStayView } = await import("@/components/phase5/post-stay-view");
    const user = userEvent.setup();
    render(<PostStayView canAct />);

    await user.click(await screen.findByRole("button", { name: "End access" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "End access permanently" });
    expect(within(dialog).getByText(/cannot be undone/i)).toBeTruthy();

    await user.type(within(dialog).getByLabelText(/Reason/), "abc");
    await user.type(within(dialog).getByLabelText(/Confirm your password/), "hunter22");
    await user.type(within(dialog).getByLabelText(/to confirm/), "REVOKE");
    expect(submit).toBeDisabled(); // reason too short

    await user.type(within(dialog).getByLabelText(/Reason/), "d");
    expect(submit).toBeEnabled();
    await user.clear(within(dialog).getByLabelText(/to confirm/));
    await user.type(within(dialog).getByLabelText(/to confirm/), "REVOK");
    expect(submit).toBeDisabled(); // the word is incomplete

    await user.type(within(dialog).getByLabelText(/to confirm/), "E");
    await user.click(submit);
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/post-stay-profiles/p-1/revoke", { password: "hunter22", reason: "abcd" }),
    );
  });

  it("offers a read-only role no action at all, and says so", async () => {
    const { PostStayView } = await import("@/components/phase5/post-stay-view");
    render(<PostStayView canAct={false} />);
    expect(await screen.findByText("Room 412")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Reset PIN" })).toBeNull();
    expect(screen.queryByRole("button", { name: "End access" })).toBeNull();
    expect(screen.getByText(/can view this but not change it/i)).toBeTruthy();
    // Raw ids stay off the table.
    expect(screen.queryByText("stay-uuid-1")).toBeNull();
    expect(screen.queryByText("2026-09-27T10:00:00Z")).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------- sessions

const SESSION = {
  id: "sess-1", tenant_id: "t", site_id: "s", appliance_id: "a", guest_id: "g",
  ip: "10.0.0.5", mac: "02:00:00:00:00:05", state: "active",
  started_at: new Date().toISOString(), bytes_down: 1000, bytes_up: 100,
  subject_kind: "room", room: "318", active_devices: 3, max_devices: 4,
};

function wireSessions(roles: string[]) {
  get.mockImplementation((raw: unknown) => {
    const path = pathOf(raw);
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path.startsWith("/sessions")) return Promise.resolve({ data: [SESSION], meta: { has_more: false } });
    return Promise.resolve({});
  });
}

describe("Active sessions", () => {
  it("never shows Disconnect to a role that may only read sessions", async () => {
    wireSessions(["site_viewer"]);
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);
    expect(await screen.findByText("Room 318")).toBeTruthy();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/auth/whoami"));
    expect(screen.queryByRole("button", { name: /disconnect/i })).toBeNull();
    expect(await screen.findByText(/not disconnect a device/i)).toBeTruthy();
  });

  it("names the guest and their other devices when a desk role disconnects one", async () => {
    wireSessions(["front_office_operator"]);
    post.mockResolvedValue({});
    const { default: Page } = await import("@/app/(app)/sessions/page");
    const user = userEvent.setup();
    render(<Page />);
    // The title is the menu label, whichever view is selected.
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Active sessions");
    await user.click(await screen.findByRole("button", { name: /disconnect/i }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Disconnect Room 318's device?")).toBeTruthy();
    expect(within(dialog).getByText(/Their 2 other devices stay online/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/sessions/sess-1/disconnect", { reason: "admin" }));
  });
});

// ---------------------------------------------------------------------------------------------------- guest accounts

const ACCOUNT = {
  id: "ga-1", username: "room101", display_name: "Room 101 guest", enabled: true,
  login_count: 3, active_devices: 1, max_devices: 2, created_at: "", updated_at: "",
};

function wireAccounts(roles: string[]) {
  get.mockImplementation((raw: unknown) => {
    const path = pathOf(raw);
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/guest-accounts") return Promise.resolve({ data: [ACCOUNT], meta: { has_more: false } });
    if (path === "/guest-accounts/portal") return Promise.resolve({ enabled: true });
    return Promise.resolve({});
  });
}

describe("Guest accounts", () => {
  it("shows a read-only role the list and the switch state, but no action", async () => {
    wireAccounts(["site_viewer"]);
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    expect(await screen.findByText("room101")).toBeTruthy();
    await screen.findByText(/can view this but not change it/i);
    for (const name of [/add account/i, /^edit$/i, /password/i, /disable/i, /^delete$/i, /disconnect/i]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    const sw = screen.getByRole("switch", { name: /offer username-and-password sign-in/i });
    expect(sw).toBeDisabled();
    expect(screen.getByText("Shown on the portal")).toBeTruthy();
  });

  it("reveals a generated password once, in the one-time reveal", async () => {
    wireAccounts(["front_office_operator"]);
    post.mockResolvedValue({ status: "ok", generated_password: "Kx7-pq2-Zr9" });
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    const user = userEvent.setup();
    render(<Page />);

    await user.click(await screen.findByRole("button", { name: /password/i }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByLabelText(/generate a strong password/i));
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/guest-accounts/ga-1/set-password", {
        password: "", generate: true, disconnect_sessions: false,
      }),
    );
    expect(await screen.findByText("Password for room101")).toBeTruthy();
    expect(screen.getByTestId("one-time-value").textContent).toBe("Kx7-pq2-Zr9");
    expect(screen.getAllByText(/cannot be looked up again/i).length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------------------------------- transfers

describe("Cross-PMS transfer", () => {
  it("keeps raw network ids and timestamps out of the main content", async () => {
    get.mockImplementation((raw: unknown) => {
      const path = pathOf(raw);
      if (path === "/stay-transfers/review-signals") {
        return Promise.resolve({
          signals: [{ resolved_at: "2026-09-20T08:00:00Z", outcome_code: "AMBIGUOUS_ROOM", guest_network_id: "0b5a6c1e-1111-4222-8333-444455556666", occurrences: 3 }],
        });
      }
      if (path === "/stay-transfers/") {
        return Promise.resolve({
          transfers: [{ id: "t-1", from_external_reservation_id: "R1", from_room: "101", to_external_reservation_id: "R2", to_room: "201", created_at: "2026-09-21T09:30:00Z" }],
        });
      }
      return Promise.resolve({});
    });
    const { StayTransferView } = await import("@/components/phase5/stay-transfer-view");
    render(<StayTransferView canAct={false} />);
    expect(await screen.findByText("Room matched twice")).toBeTruthy();
    expect(await screen.findByText("Room 201")).toBeTruthy();
    const text = document.body.textContent ?? "";
    expect(text).not.toContain("0b5a6c1e-1111-4222-8333-444455556666");
    expect(text).not.toContain("2026-09-21T09:30:00Z");
    expect(text).not.toContain("AMBIGUOUS_ROOM");
    // A read-only role gets no transfer form at all.
    expect(screen.queryByRole("button", { name: "Preview" })).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------- restrictions

describe("Active restrictions", () => {
  it("requires a reason to release, and says releasing does not sign the guest in", async () => {
    get.mockResolvedValue({
      restrictions: [{
        id: "r-1", device_mac: "02:00:00:aa:bb:cc", guest_network: "Guest WiFi", last_submitted_room: "412",
        failure_count: 5, reason: "RATE_LIMITED", restricted_at: new Date().toISOString(),
        expires_at: new Date(Date.now() + 60_000).toISOString(), remaining_seconds: 60,
      }],
    });
    post.mockResolvedValue({});
    const { ActiveRestrictions } = await import("@/components/guest-signin-restrictions");
    const user = userEvent.setup();
    render(<ActiveRestrictions canRelease onShowAttempts={() => {}} />);

    await user.click(await screen.findByRole("button", { name: "Release" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Releasing does not sign the guest in")).toBeTruthy();
    const submit = within(dialog).getByRole("button", { name: "Release" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Reason/), "identity confirmed at the desk");
    await user.click(submit);
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/guest-signin-restrictions/r-1/release", { reason: "identity confirmed at the desk" }),
    );
  });

  it("offers no Release to a role that may only look", async () => {
    get.mockResolvedValue({ restrictions: [{
      id: "r-1", device_mac: "02:00:00:aa:bb:cc", failure_count: 5, reason: "RATE_LIMITED",
      restricted_at: new Date().toISOString(), expires_at: new Date(Date.now() + 60_000).toISOString(), remaining_seconds: 60,
    }] });
    const { ActiveRestrictions } = await import("@/components/guest-signin-restrictions");
    render(<ActiveRestrictions canRelease={false} onShowAttempts={() => {}} />);
    expect(await screen.findByText("02:00:00:aa:bb:cc")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Release" })).toBeNull();
  });
});
