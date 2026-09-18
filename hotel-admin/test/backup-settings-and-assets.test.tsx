import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// RETENTION AND THE NIGHTLY SCHEDULE WERE SHOWN AND NOT EDITABLE.
//
// A screen that displays a policy an operator cannot change tells them something is under their control when
// it is not. These assert the knobs, their bounds, what is in force when nothing has been saved, and that
// untouched values travel with the ones that changed.

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

/** The schema the appliance serves, matching stayconnect-backup-cleanup's own defaults. */
const SETTINGS = {
  retention: [
    { key: "KEEP_DB", label: "Database backups", unit: "backups", default: 7, min: 1, max: 365, value: 7, explains: "How many to retain." },
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

describe("backup retention is editable, not just visible", () => {
  it("shows every knob with its unit, bounds and default", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);

    const field = await screen.findByLabelText(/Database backups/i);
    expect((field as HTMLInputElement).value).toBe("7");
    // An operator cannot set a value responsibly without knowing what it does and what it may be.
    expect(screen.getByText(/Allowed 1–365; default 7/)).toBeTruthy();
    expect(await screen.findByLabelText(/Nightly sweep runs at/i)).toBeTruthy();
  });

  it("says the defaults are in force when nothing has been saved", async () => {
    // A freshly onboarded appliance has no config file. "No policy saved" and "no policy in force" are
    // different claims, and only the first is true.
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    expect(await screen.findByText(/built-in defaults below are in force/i)).toBeTruthy();
  });

  it("sends the edited policy and the schedule under a password step-up", async () => {
    mockBackups();
    put.mockResolvedValue({ ...SETTINGS, config_present: true, schedule: "02:15" });
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);

    fireEvent.change(await screen.findByLabelText(/Database backups/i), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText(/Nightly sweep runs at/i), { target: { value: "02:15" } });
    fireEvent.change(await screen.findByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /Save retention policy/i }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/backups/settings");
    expect(body.retention.KEEP_DB).toBe(30);
    // Untouched knobs travel too: sending only what changed would let the appliance rewrite the rest to
    // defaults it never asked for.
    expect(body.retention.KEEP_BINARIES).toBe(5);
    expect(body.schedule).toBe("02:15");
    expect(body.password).toBe("pw");
  });

  it("offers nothing to save until something actually changed", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByLabelText(/Database backups/i);
    expect(screen.queryByRole("button", { name: /Save retention policy/i })).toBeNull();
  });
});

// The portal-branding half of this file moved to test/portal-settings.test.tsx when that screen was rebuilt
// as Portal settings. The assertions were not dropped -- upload, SVG refusal, the six shipped languages, one
// language at a time and the completeness count are all made there, against the page that now exists.
