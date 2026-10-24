import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { chooseInRow, rowMenuHas } from "./row-actions-helper";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { ApiError } from "@/lib/api";
import { verificationStatus } from "@/lib/account-verification";

const HOUR = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();
const now = Date.now();
const base = { enabled: true, login_count: 0, active_devices: 0, self_registered: true, email_verified: false };
const ACCOUNTS = [
  { ...base, id: "s1", username: "late@example.com", verification_deadline: iso(now - HOUR), suspended: true },
  { ...base, id: "s2", username: "new@example.com", verification_deadline: iso(now + 20 * HOUR), suspended: false },
  { ...base, id: "s3", username: "done@example.com", email_verified: true, email_verified_at: iso(now - HOUR), suspended: false },
  { id: "p1", username: "room-desk", enabled: true, login_count: 2, active_devices: 0, self_registered: false, suspended: false },
];

function wire() {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["front_office_operator"] });
    if (path === "/guest-accounts/portal") return Promise.resolve({ enabled: true });
    if (typeof path === "string" && path.startsWith("/guest-accounts?")) {
      return Promise.resolve({ data: ACCOUNTS, meta: { has_more: false }, page: 1, page_size: 50,
        summary: { total: 4, enabled: 4, disabled: 0, devices_online: 0 } });
    }
    return Promise.resolve({});
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); wire(); });
afterEach(() => { cleanup(); vi.resetModules(); });

const row = (name: string) => screen.getByText(name).closest("tr") as HTMLElement;

describe("Client accounts — self-service email accounts", () => {
  it("words every verification state", () => {
    expect(verificationStatus(ACCOUNTS[0] as any, now)?.label).toBe("Suspended: email not verified");
    expect(verificationStatus(ACCOUNTS[1] as any, now)?.label).toMatch(/^Verify by /);
    expect(verificationStatus(ACCOUNTS[2] as any, now)).toEqual({ label: "Email verified", tone: "ok" });
    expect(verificationStatus(ACCOUNTS[3] as any, now)).toBeNull();
    expect(verificationStatus({ ...ACCOUNTS[0], suspended: false, grace_until: iso(now + HOUR) } as any, now)?.label)
      .toMatch(/^Extra time until /);
  });

  it("badges self-service accounts, shows their state, and offers resend instead of a password", async () => {
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    await screen.findByText("late@example.com");

    const late = row("late@example.com");
    expect(within(late).getByText("Self-service")).toBeTruthy();
    expect(within(late).getByText("Suspended: email not verified")).toBeTruthy();
    expect(await rowMenuHas(late, /password/i)).toBe(false);
    expect(await rowMenuHas(late, /resend verification email/i)).toBe(true);

    expect(within(row("new@example.com")).getByText(/^Verify by /)).toBeTruthy();

    const done = row("done@example.com");
    expect(within(done).getByText("Email verified")).toBeTruthy();
    expect(await rowMenuHas(done, /resend verification email/i)).toBe(false);
    expect(await rowMenuHas(done, /password/i)).toBe(false);

    const staff = row("room-desk");
    expect(within(staff).queryByText("Self-service")).toBeNull();
    expect(await rowMenuHas(staff, /password/i)).toBe(true);
    expect(await rowMenuHas(staff, /resend verification email/i)).toBe(false);
  });

  it("resends through edged, and says so when the appliance cannot", async () => {
    post.mockResolvedValueOnce({ status: "queued" });
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    // Imported with the page: after vi.resetModules a statically imported provider is a different context.
    const { ToastProvider } = await import("@/components/ui/toast");
    render(<ToastProvider><Page /></ToastProvider>);
    await screen.findByText("late@example.com");
    await chooseInRow(row("late@example.com"), /resend verification email/i);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-accounts/s1/resend-verification"));
    expect(await screen.findByText(/verification email queued for late@example.com/i)).toBeTruthy();

    post.mockRejectedValueOnce(new ApiError(501, { error: "resend_unavailable",
      message: "This appliance's sign-in service cannot resend verification emails yet." }));
    await chooseInRow(row("new@example.com"), /resend verification email/i);
    expect(await screen.findByText(/cannot be resent from here yet/i)).toBeTruthy();

    post.mockRejectedValueOnce(new ApiError(429, { error: "resend_wait", retry_after_seconds: 42 }));
    await chooseInRow(row("new@example.com"), /resend verification email/i);
    expect(await screen.findByText(/try again in 42 seconds/i)).toBeTruthy();
  });
});
