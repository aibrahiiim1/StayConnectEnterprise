// The Devices page reorganisation (2026-11-02): the sign-in and capacity settings are shown where an operator looks for
// them, the Devices page keeps only device settings behind one button, and the daily device-change limit is confirmed by
// a warning (Cancel / Confirm) with no reason and no password. The other groups keep their reason and password.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup, fireEvent } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as Record<string, unknown>).ResizeObserver = (globalThis as Record<string, unknown>).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: unknown[]) => get(...a), post: vi.fn(), put: (...a: unknown[]) => put(...a), patch: vi.fn(), del: vi.fn() } };
});

const BOUNDS = {
  device_retention_days: { min: 7, max: 3650, default: 90, unit: "days", help: "Devices not seen for this long are removed." },
  otp_code_lifetime_minutes: { min: 2, max: 30, default: 10, unit: "minutes", help: "How long a one-time code works." },
};

function wire() {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/device-settings/") return Promise.resolve({ settings: { client_device_changes_per_day: 3 } });
    if (path === "/signin-capacity-settings/") return Promise.resolve({ settings: { device_retention_days: 90, otp_code_lifetime_minutes: 10 }, bounds: BOUNDS });
    if (path.startsWith("/client-devices?")) return Promise.resolve({ data: [], meta: { has_more: false } });
    return Promise.resolve({});
  });
}
beforeEach(() => { get.mockReset(); put.mockReset(); wire(); });
afterEach(() => { cleanup(); vi.resetModules(); });

async function openDeviceSettings() {
  const { default: Page } = await import("@/app/(app)/devices/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  // No settings sit on the page itself any more.
  await screen.findByText(/no devices yet/i);
  expect(screen.queryByText(/sign-in limits and capacity/i)).toBeNull();
  expect(screen.queryByTestId("device-changes-setting")).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: /device settings/i }));
  return screen.findByRole("dialog", { name: "Device settings" });
}

describe("daily device changes", () => {
  it("asks for no reason and no password, warns, and saves only on Confirm", async () => {
    put.mockResolvedValue({ settings: { client_device_changes_per_day: 5 } });
    const dlg = await openDeviceSettings();
    const setting = await within(dlg).findByTestId("device-changes-setting");
    await within(setting).findByText("3 per day");
    fireEvent.click(within(setting).getByRole("button", { name: "Change" }));
    expect(within(setting).queryByLabelText(/reason/i)).toBeNull();
    expect(within(setting).queryByLabelText(/password/i)).toBeNull();
    fireEvent.change(within(setting).getByLabelText(/changes per day/i), { target: { value: "5" } });
    fireEvent.click(within(setting).getByRole("button", { name: "Save" }));

    const warn = await screen.findByRole("alertdialog", { name: /change the daily device limit/i }).catch(() =>
      screen.findByRole("dialog", { name: /change the daily device limit/i }));
    expect(warn.textContent).toMatch(/5 devices a day/);
    expect(warn.textContent).toMatch(/now 3/);
    expect(within(warn).queryByLabelText(/reason/i)).toBeNull();
    expect(within(warn).queryByLabelText(/password/i)).toBeNull();

    fireEvent.click(within(warn).getByRole("button", { name: "Cancel" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(put).not.toHaveBeenCalled();

    fireEvent.click(within(setting).getByRole("button", { name: "Save" }));
    const again = await screen.findByRole("alertdialog", { name: /change the daily device limit/i }).catch(() =>
      screen.findByRole("dialog", { name: /change the daily device limit/i }));
    fireEvent.click(within(again).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/device-settings/", { client_device_changes_per_day: 5 }));
  });
});

describe("the sign-in and capacity groups", () => {
  it("cover every setting the appliance reports, each exactly once", async () => {
    const { SIGNIN_SETTING_GROUPS, SIGNIN_SETTING_LABELS } = await import("@/components/devices/signin-limits-card");
    const all = Object.values(SIGNIN_SETTING_GROUPS).flat();
    expect(new Set(all).size).toBe(all.length);
    const src = readFileSync(join(__dirname, "..", "..", "data-plane", "cmd", "edged", "signin_capacity_admin.go"), "utf-8");
    const fields = Array.from(src.matchAll(/^\s*\{"([a-z_]+)", signinCapacityBound/gm)).map((m) => m[1]);
    expect(fields.length).toBeGreaterThan(10);
    expect([...all].sort()).toEqual([...fields].sort());
    for (const k of fields) expect(SIGNIN_SETTING_LABELS[k]).toBeTruthy();
  });

  it("keep their reason and password when moved (device retention)", async () => {
    const dlg = await openDeviceSettings();
    const group = await within(dlg).findByTestId("settings-group-Device retention");
    await within(group).findByText("90 days");
    expect(within(group).queryByText(/code lifetime/i)).toBeNull();
    fireEvent.click(within(group).getByRole("button", { name: "Change" }));
    fireEvent.change(within(group).getByRole("spinbutton"), { target: { value: "120" } });
    fireEvent.click(within(group).getByRole("button", { name: "Save" }));
    const confirm = await screen.findByRole("dialog", { name: /save device retention/i });
    expect(within(confirm).getByLabelText(/reason/i)).toBeInTheDocument();
    expect(within(confirm).getByLabelText(/password/i)).toBeInTheDocument();
    // The appliance's 4-character minimum holds in the dialog, as it did in the form before the move.
    fireEvent.change(within(confirm).getByLabelText(/reason/i), { target: { value: "abc" } });
    fireEvent.change(within(confirm).getByLabelText(/password/i), { target: { value: "pw" } });
    expect(within(confirm).getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.change(within(confirm).getByLabelText(/reason/i), { target: { value: "abcd" } });
    expect(within(confirm).getByRole("button", { name: "Save" })).toBeEnabled();
    expect(put).not.toHaveBeenCalled();
  });
});
