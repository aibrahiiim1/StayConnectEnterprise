import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { chooseInRow, rowMenuHas } from "./row-actions-helper";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
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
    api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() },
  };
});

const FORMAT = {
  password_style: "mixed", password_length: 14, config_version: 0,
  limits: {
    entropy_floor_bits: 40, max_length: 32, default_style: "mixed", default_length: 14,
    styles: [{
      key: "mixed", label: "Upper and lower case letters and digits",
      alphabet: "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789",
      min_length: 7, max_length: 32, bits_per_char: 5.781,
    }],
  },
};

const ACCOUNT = {
  id: "ga-1", username: "room101", display_name: "Room 101 guest", enabled: true,
  login_count: 3, active_devices: 2, created_at: "2026-09-30T10:00:00Z",
};

function wire() {
  get.mockImplementation((raw: unknown) => {
    const path = typeof raw === "string" ? raw : String(raw);
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/guest-accounts/portal") return Promise.resolve({ enabled: true });
    if (path === "/account-password-settings/") return Promise.resolve(FORMAT);
    if (path === "/account-password-settings/changes") return Promise.resolve({ changes: [] });
    if (path.startsWith("/guest-accounts")) {
      return Promise.resolve({
        data: [ACCOUNT], meta: { has_more: false }, authority: "iam_v2", page: 1, page_size: 50,
        summary: { total: 1, enabled: 1, disabled: 0, devices_online: 2 },
      });
    }
    return Promise.resolve({});
  });
}

const passwordBox = (dialog: HTMLElement) =>
  dialog.querySelector('input[name="password"]') as HTMLInputElement;

async function openPage() {
  // Both imports must come from the same module graph: after vi.resetModules() a static ToastProvider would carry a
  // different React context than the page's useToast.
  const { ToastProvider } = await import("@/components/ui/toast");
  const { default: Page } = await import("@/app/(app)/guest-accounts/page");
  render(<ToastProvider><Page /></ToastProvider>);
  expect(await screen.findByText("room101")).toBeTruthy();
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});
afterEach(() => {
  cleanup();
  vi.resetModules();
});

describe("Client accounts — disconnecting an account's devices", () => {
  it("sends the disconnect flag with the new password and reports the confirmed count", async () => {
    wire();
    post.mockResolvedValue({ status: "password_set", disconnected_sessions: 2 });
    const user = userEvent.setup();
    await openPage();

    await chooseInRow(document.body, /password/i);
    const dialog = await screen.findByRole("dialog");
    await user.type(passwordBox(dialog), "fresh-one");
    await user.click(within(dialog).getByLabelText(/disconnect this account’s devices now/i));
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/guest-accounts/ga-1/set-password", {
        password: "fresh-one", generate: false, disconnect_sessions: true,
      }),
    );
    expect(await screen.findByText("2 devices disconnected.")).toBeTruthy();
  });

  it("does not ask for a disconnect, or claim one, when the box is left unticked", async () => {
    wire();
    post.mockResolvedValue({ status: "password_set" });
    const user = userEvent.setup();
    await openPage();

    await chooseInRow(document.body, /password/i);
    const dialog = await screen.findByRole("dialog");
    await user.type(passwordBox(dialog), "fresh-two");
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/guest-accounts/ga-1/set-password", {
        password: "fresh-two", generate: false, disconnect_sessions: false,
      }),
    );
    expect(await screen.findByText("Password updated for room101.")).toBeTruthy();
    expect(screen.queryByText(/disconnected\./)).toBeNull();
  });

  it("says so when a device could not be disconnected, instead of reporting a clean success", async () => {
    wire();
    post.mockResolvedValue({ status: "password_set", disconnected_sessions: 1, disconnect_failures: 1 });
    const user = userEvent.setup();
    await openPage();

    await chooseInRow(document.body, /password/i);
    const dialog = await screen.findByRole("dialog");
    await user.type(passwordBox(dialog), "fresh-three");
    await user.click(within(dialog).getByLabelText(/disconnect this account’s devices now/i));
    await user.click(within(dialog).getByRole("button", { name: "Set password" }));

    expect(await screen.findByText("1 device disconnected.")).toBeTruthy();
    expect(await screen.findByText("1 device could not be disconnected.")).toBeTruthy();
    expect(screen.getByText(/End those sessions from Active sessions/)).toBeTruthy();
  });

  it("reports the row action's confirmed count and its failures", async () => {
    wire();
    post.mockResolvedValue({ disconnected_sessions: 1, disconnect_failures: 2 });
    const user = userEvent.setup();
    await openPage();

    await chooseInRow(document.body, /disconnect/i);
    const confirm = await screen.findByRole("dialog");
    expect(within(confirm).getByText(/The account still works, so they can sign in again/)).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: /disconnect devices/i }));

    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-accounts/ga-1/disconnect"));
    expect(await screen.findByText("1 device disconnected.")).toBeTruthy();
    expect(await screen.findByText("2 devices could not be disconnected.")).toBeTruthy();
  });
});

describe("Client accounts — no account-lockout fiction", () => {
  it("offers no Locked out counter and no Locked out filter, and points at the real protection", async () => {
    wire();
    await openPage();

    expect(screen.queryByText("Locked out")).toBeNull();
    const filter = screen.getByRole("combobox", { name: "Filter by status" });
    expect(Array.from(filter.querySelectorAll("option")).map((o) => o.textContent))
      .toEqual(["All accounts", "Can sign in", "Disabled", "Removed"]); // Removed: accounts retired with history kept
    expect(screen.getByText("Devices online")).toBeTruthy();
    expect(screen.getByText("Able to sign in")).toBeTruthy();
  });
});
