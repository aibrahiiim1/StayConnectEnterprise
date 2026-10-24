import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

const get = vi.fn();
const put = vi.fn();
const post = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    put: (...a: any[]) => put(...a),
    post: (...a: any[]) => post(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => { get.mockReset(); put.mockReset(); post.mockReset(); del.mockReset(); });
afterEach(() => vi.resetModules());

const SETTINGS = {
  retention: [
    { key: "BACKUP_ENABLED", label: "Scheduled backups", unit: "0 = off, 1 = on", default: 0, min: 0, max: 1, value: 0, explains: "Off until you switch it on." },
    { key: "BACKUP_KEEP", label: "Backups kept", unit: "backups", default: 7, min: 1, max: 30, value: 7, explains: "How many to retain." },
    { key: "KEEP_BINARIES", label: "Service binaries", unit: "versions", default: 5, min: 2, max: 30, value: 5, explains: "Rollback targets." },
    { key: "DISK_WARN", label: "Disk warning threshold", unit: "% used", default: 80, min: 50, max: 95, value: 80, explains: "Warn here." },
    { key: "DISK_CRIT", label: "Disk critical threshold", unit: "% used", default: 90, min: 60, max: 99, value: 90, explains: "Alarm here." },
  ],
  schedule: "03:30",
  schedule_known: true,
  config_present: false,
};

function mockBackups() {
  get.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (p === "/backups/health") return Promise.resolve({
      retention_readable: true, retention: { disk_pct: 40, retained: 8, protected: 7, pinned: 0 },
      timer_active: true, timer_readable: true, database_backups: 1,
    });
    if (p === "/backups/artifacts") return Promise.resolve({ data: [], meta: { has_more: false } });
    if (p === "/backups/settings") return Promise.resolve(SETTINGS);
    return Promise.resolve({});
  });
}

// Storage & retention sits on its own tab of the Backups page.
async function openStorageTab() {
  const tab = await screen.findByRole("tab", { name: /Schedule & storage/ });
  fireEvent.mouseDown(tab, { button: 0 });
  fireEvent.click(tab);
}

describe("backup retention is editable, not just visible", () => {
  it("shows every knob with its unit, bounds and default", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await openStorageTab();

    const field = await screen.findByLabelText(/Service binaries/i);
    expect((field as HTMLInputElement).value).toBe("5");
    expect(screen.getByText(/Default 5 versions; allowed 2–30/)).toBeTruthy();
    // the backup schedule and counts are not in the sweep list: they live on the Scheduled backups card
    expect(screen.queryAllByLabelText(/Backups kept/i).length).toBeLessThanOrEqual(1);
    expect(await screen.findByLabelText(/Nightly sweep runs at/i)).toBeTruthy();
  });

  it("says the defaults are in force when nothing has been saved", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await openStorageTab();
    expect(await screen.findByText(/built-in defaults below are in force/i)).toBeTruthy();
  });

  it("sends the edited policy and the schedule under a password step-up", async () => {
    mockBackups();
    put.mockResolvedValue({ ...SETTINGS, config_present: true, schedule: "02:15" });
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await openStorageTab();

    fireEvent.change(await screen.findByLabelText(/Service binaries/i), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText(/Nightly sweep runs at/i), { target: { value: "02:15" } });
    fireEvent.change(await screen.findByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /Save retention policy/i }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/backups/settings");
    expect(body.retention.KEEP_BINARIES).toBe(9);
    // Untouched knobs travel too, or the appliance would rewrite them to defaults.
    expect(body.retention.DISK_WARN).toBe(80);
    expect(body.schedule).toBe("02:15");
    expect(body.password).toBe("pw");
  });

  it("offers nothing to save until something actually changed", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await openStorageTab();
    await screen.findByLabelText(/Service binaries/i);
    expect(screen.queryByRole("button", { name: /Save retention policy/i })).toBeNull();
  });
});
