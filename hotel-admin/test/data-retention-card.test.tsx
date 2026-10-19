import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react";

// GDPR C1: how long guest data is kept -- six periods with the server's bounds and defaults, changed only with a
// reason and the password, and nothing offered to a role that cannot change it.

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a), post: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

const bounds = {
  pms_stay_days: { min: 7, max: 3650, default: 30, unit: "days after checkout" },
  client_activity_days: { min: 30, max: 3650, default: 365, unit: "days" },
  inactive_identity_days: { min: 30, max: 3650, default: 365, unit: "days" },
  expired_record_days: { min: 1, max: 365, default: 7, unit: "days" },
  audit_log_days: { min: 90, max: 3650, default: 730, unit: "days" },
  sign_in_attempt_days: { min: 7, max: 365, default: 30, unit: "days" },
  log_days: { min: 1, max: 730, default: 14, unit: "days" },
  staff_note_days: { min: 30, max: 3650, default: 730, unit: "days" },
};
let settings: Record<string, number>;
beforeEach(() => {
  settings = { pms_stay_days: 30, client_activity_days: 365, inactive_identity_days: 365, expired_record_days: 7, audit_log_days: 730, sign_in_attempt_days: 30, log_days: 14, staff_note_days: 730 };
  get.mockReset(); put.mockReset();
  get.mockImplementation((path: string) => path === "/data-retention/"
    ? Promise.resolve({ settings: { ...settings }, bounds, runs: [] })
    : Promise.resolve({}));
  put.mockImplementation((_p: string, body: any) => { settings = body.settings; return Promise.resolve({}); });
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderCard(writable: boolean) {
  const { DataRetentionCard } = await import("@/app/(app)/backups/data-retention-card");
  const { ToastProvider } = await import("@/components/ui/toast");
  const r = render(<ToastProvider><DataRetentionCard writable={writable} /></ToastProvider>);
  await screen.findByText("Hotel stay data");
  return r;
}

describe("Guest data retention", () => {
  it("shows every period and offers no change to a read-only role", async () => {
    await renderCard(false);
    expect(screen.getAllByText("730 days")).toHaveLength(2); // the audit log details and the staff notes
    expect(screen.getByText("Staff notes")).toBeTruthy();
    expect(screen.getByText(/cannot be brought back/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change/i })).toBeNull();
  });

  it("saves every period within the server's bounds, with a reason and the password", async () => {
    await renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: /change/i }));
    const save = screen.getByRole("button", { name: /^save$/i }) as HTMLButtonElement;
    fireEvent.change(screen.getByLabelText(/hotel stay data/i), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: "hotel policy" } });
    fireEvent.change(screen.getByLabelText(/your password/i), { target: { value: "pw" } });
    expect(save.disabled).toBe(true); // 3 days is below the server's minimum of 7
    fireEvent.change(screen.getByLabelText(/hotel stay data/i), { target: { value: "60" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/data-retention/", {
      settings: { pms_stay_days: 60, client_activity_days: 365, inactive_identity_days: 365, expired_record_days: 7, audit_log_days: 730, sign_in_attempt_days: 30, log_days: 14, staff_note_days: 730 },
      reason: "hotel policy", password: "pw",
    }));
    await screen.findByText("60 days");
  });

  it("shows nothing on an appliance without the setting", async () => {
    get.mockImplementation(() => Promise.reject(new Error("404")));
    const { DataRetentionCard } = await import("@/app/(app)/backups/data-retention-card");
    const { ToastProvider } = await import("@/components/ui/toast");
    const { container } = render(<ToastProvider><DataRetentionCard writable /></ToastProvider>);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).not.toContain("Guest data kept for");
  });
});
