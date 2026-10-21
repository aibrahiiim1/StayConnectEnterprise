import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// CLIENT ACCOUNTS: MANAGE AND REMOVE. Manage shows the verification state, packages and devices of one account and
// offers the verification adjustments; Remove says what it did (deleted, or removed with history kept); a removed
// account is badged and offers only Manage.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: (...a: any[]) => del(...a) } };
});

import { isRemovedAccount } from "@/lib/account-verification";

const HOUR = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();
const now = Date.now();
const LATE = { id: "s1", username: "late@example.com", enabled: true, login_count: 3, active_devices: 1,
  self_registered: true, email_verified: false, verification_deadline: iso(now - HOUR), suspended: true,
  grace_extensions_used: 1 };
const GONE = { id: "r1", username: "old@example.com.removed-ab12cd34.invalid", enabled: false, login_count: 2,
  active_devices: 0, self_registered: true, email_verified: false };
const MANAGE = {
  account: LATE, removed: false, removal: "remove", entitlements_total: 1,
  entitlements: [{ id: "e1", status: "ACTIVE", package_name: "Day pass", activated_at: iso(now - 2 * HOUR),
    window_ends_at: iso(now + 20 * HOUR), consumed_data_bytes: 2_000_000, data_quota_bytes: 5_000_000_000,
    consumed_online_seconds: 60, max_devices: 1,
    devices: [{ mac: "02:aa:bb:cc:dd:01", status: "AUTHORIZED", online: false, first_authorized: iso(now - 2 * HOUR) }] }],
};

function wire(roles: string[] = ["site_admin"]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/guest-accounts/portal") return Promise.resolve({ enabled: true });
    if (path === "/guest-accounts/s1/manage") return Promise.resolve(MANAGE);
    if (typeof path === "string" && path.startsWith("/guest-accounts?")) {
      return Promise.resolve({ data: [LATE, GONE], meta: { has_more: false }, page: 1, page_size: 50,
        summary: { total: 2, enabled: 1, disabled: 1, devices_online: 1 } });
    }
    return Promise.resolve({});
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); del.mockReset(); wire(); });
afterEach(() => { cleanup(); vi.resetModules(); });

// The confirmation opened on top of the page (or of the Manage dialog): the last dialog, once it says what it asks.
const topDialog = (text: RegExp) => waitFor(() => {
  const last = screen.getAllByRole("dialog").at(-1)!;
  if (!text.test(last.textContent ?? "")) throw new Error("the confirmation is not open yet");
  return last;
});

const row = (name: string) => screen.getByText(name).closest("tr") as HTMLElement;

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-accounts/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText("late@example.com");
}

describe("Client accounts — manage and remove", () => {
  it("recognises a removed account by its marker", () => {
    expect(isRemovedAccount(GONE as any)).toBe(true);
    expect(isRemovedAccount({ ...GONE, enabled: true } as any)).toBe(false);
    expect(isRemovedAccount(LATE as any)).toBe(false);
  });

  it("badges a removed account and offers only Manage", async () => {
    await renderPage();
    const gone = row(GONE.username);
    expect(within(gone).getByText("Removed")).toBeTruthy();
    expect(within(gone).getByRole("button", { name: /manage/i })).toBeTruthy();
    expect(within(gone).queryByRole("button", { name: /^remove$/i })).toBeNull();
    expect(within(gone).queryByRole("button", { name: /enable/i })).toBeNull();
  });

  it("shows packages and devices, and adjusts verification through edged", async () => {
    post.mockResolvedValue({ status: "ok" });
    await renderPage();
    await userEvent.click(within(row("late@example.com")).getByRole("button", { name: /manage/i }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Day pass")).toBeTruthy();
    expect(within(dialog).getByText("02:aa:bb:cc:dd:01")).toBeTruthy();
    expect(within(dialog).getByText("Holds a slot")).toBeTruthy();

    // MARK VERIFIED VOUCHES FOR AN ADDRESS: a reason and the operator's password, then edged (audit C-4).
    await userEvent.click(within(dialog).getByRole("button", { name: /mark email verified/i }));
    const confirm = await topDialog(/password|disconnected/i);
    const confirmBtn = within(confirm).getByRole("button", { name: /^mark verified$/i });
    expect(confirmBtn).toBeDisabled();
    await userEvent.type(within(confirm).getByLabelText(/reason/i), "shown at the desk");
    await userEvent.type(within(confirm).getByLabelText(/password/i), "pw");
    await userEvent.click(confirmBtn);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-accounts/s1/verify-email",
      { reason: "shown at the desk", password: "pw" }));
    await userEvent.click(within(dialog).getByRole("button", { name: /new verification period/i }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-accounts/s1/restart-verification"));
  });

  it("removes an account with history and says the address is free again", async () => {
    del.mockResolvedValueOnce({ status: "removed", history_kept: true, disconnected_sessions: 1 });
    await renderPage();
    await userEvent.click(within(row("late@example.com")).getByRole("button", { name: /manage/i }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("Day pass");
    await userEvent.click(within(dialog).getByRole("button", { name: /remove account/i }));
    expect(await screen.findByText(/its history is kept|history are kept/i)).toBeTruthy();
    // REMOVE CANNOT BE UNDONE, so it asks for the password (audit C-19).
    const confirm = screen.getAllByRole("dialog").at(-1)!;
    await userEvent.type(within(confirm).getByLabelText(/password/i), "pw");
    await userEvent.click(within(confirm).getByRole("button", { name: /^remove account$/i }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/guest-accounts/s1", { password: "pw" }));
    expect(await screen.findByText(/has been removed/i)).toBeTruthy();
  });
});

describe("Client accounts — what each role is offered (C-3, C-4, C-18)", () => {
  it("the voucher station cannot vouch for an address, act on a device, or flip the portal switch", async () => {
    wire(["voucher_operator"]);
    await renderPage();
    expect(screen.getByRole("switch", { name: /offer username-and-password sign-in/i })).toBeDisabled();
    await userEvent.click(within(row("late@example.com")).getByRole("button", { name: /manage/i }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("Day pass");
    expect(within(dialog).queryByRole("button", { name: /mark email verified/i })).toBeNull();
    // It still reads Active sessions, so the Device view is offered -- read-only.
    expect(within(dialog).getByRole("button", { name: /^device$/i })).toBeTruthy();
  });

  it("the desk may vouch for an address but not flip a sign-in method", async () => {
    wire(["front_office_operator"]);
    await renderPage();
    expect(screen.getByRole("switch", { name: /offer username-and-password sign-in/i })).toBeDisabled();
    expect(screen.getByText(/your role cannot change which sign-in methods are offered/i)).toBeTruthy();
    await userEvent.click(within(row("late@example.com")).getByRole("button", { name: /manage/i }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("Day pass");
    expect(within(dialog).getByRole("button", { name: /mark email verified/i })).toBeTruthy();
  });
});

describe("Client accounts — Disable (decision D-A)", () => {
  it("confirms, says what happens to the devices and the package, and reports what edged did", async () => {
    const patch = (await import("@/lib/api")).api.patch as unknown as ReturnType<typeof vi.fn>;
    patch.mockResolvedValue({ ...LATE, enabled: false, packages_suspended: 1, disconnected_sessions: 1 });
    await renderPage();
    await userEvent.click(within(row("late@example.com")).getByRole("button", { name: /^disable\b/i }));
    const confirm = await topDialog(/password|disconnected/i);
    expect(within(confirm).getByText(/will be disconnected/i)).toBeTruthy();
    expect(within(confirm).getByText(/package is paused/i)).toBeTruthy();
    expect(patch).not.toHaveBeenCalled();
    await userEvent.click(within(confirm).getByRole("button", { name: /^disable account$/i }));
    await waitFor(() => expect(patch).toHaveBeenCalledWith("/guest-accounts/s1", { enabled: false }));
    expect(await screen.findByText(/1 device disconnected; its package is paused/i)).toBeTruthy();
  });
});
