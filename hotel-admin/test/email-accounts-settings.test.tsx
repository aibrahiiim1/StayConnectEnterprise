import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

// EMAIL ACCOUNTS: THE TERMS (self-service contract §6). Every range, unit and default comes from the appliance; a
// save needs a valid draft, a reason and the operator's password; a read-only role sees the values and no Save;
// the change history is listed in words.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a), post: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { EmailAccountsSettingsCard, changeSummary, draftProblem, type Draft } from "@/components/email-accounts-settings";

const SETTINGS = {
  verification_required: true, verification_deadline_hours: 24, suspension_policy: "SUSPEND", reminder_hours_before: 4,
  resend_cooldown_seconds: 60, resend_max_per_day: 5, grace_minutes: 60, max_grace_extensions: 1,
  registrations_per_device_per_day: 3,
};
const BOUNDS = [
  { key: "verification_deadline_hours", min: 1, max: 168, unit: "hours", default: 24 },
  { key: "reminder_hours_before", min: 0, max: 72, unit: "hours", default: 4 },
  { key: "resend_cooldown_seconds", min: 30, max: 3600, unit: "seconds", default: 60 },
  { key: "resend_max_per_day", min: 1, max: 20, unit: "emails per day", default: 5 },
  { key: "grace_minutes", min: 15, max: 240, unit: "minutes", default: 60 },
  { key: "max_grace_extensions", min: 0, max: 3, unit: "times", default: 1 },
  { key: "registrations_per_device_per_day", min: 1, max: 10, unit: "accounts per device per day", default: 3 },
];
const RESP = {
  settings: SETTINGS, defaults: SETTINGS, config_version: 2, updated_at: "2026-10-14T10:00:00Z", updated_by: "it@hotel.test",
  limits: { bounds: BOUNDS, suspension_policies: ["SUSPEND", "WARN_ONLY"] },
  changes: [{
    changed_at: "2026-10-14T10:00:00Z", changed_by: "it@hotel.test", change_reason: "conference week",
    old_grace_minutes: 30, new_grace_minutes: 60, old_suspension_policy: "WARN_ONLY", new_suspension_policy: "SUSPEND",
  }],
};

beforeEach(() => {
  get.mockReset(); put.mockReset();
  get.mockImplementation((p: string) => (p === "/self-service-settings/" ? Promise.resolve(RESP) : Promise.resolve({})));
});
afterEach(cleanup);

describe("Email accounts settings", () => {
  it("shows every setting with the server's unit, range and default, and the history in words", async () => {
    render(<EmailAccountsSettingsCard canEdit />);
    const deadline = (await screen.findByLabelText("Time to verify")) as HTMLInputElement;
    expect(deadline.value).toBe("24");
    expect(screen.getByText(/Default 24 hours; allowed 1–168/)).toBeTruthy();
    expect(screen.getByText(/Default 60 minutes; allowed 15–240/)).toBeTruthy();
    expect(screen.getByText(/Default 3 accounts per device per day; allowed 1–10/)).toBeTruthy();
    expect(screen.getByText(/keep the deadline they were given/)).toBeTruthy();
    expect(screen.getByText(/When not verified: remind only → suspend; Extra time when suspended: 30 → 60/)).toBeTruthy();
    expect(screen.getByText(/conference week/)).toBeTruthy();
  });

  it("saves only a changed, valid draft with a reason and the password", async () => {
    put.mockResolvedValue({ notice: "Saved." });
    render(<EmailAccountsSettingsCard canEdit />);
    const grace = (await screen.findByLabelText("Extra time when suspended")) as HTMLInputElement;
    const save = screen.getByRole("button", { name: /save email accounts settings/i }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    fireEvent.change(grace, { target: { value: "300" } });
    expect(screen.getByRole("alert").textContent).toMatch(/from 15 to 240 minutes/);
    fireEvent.change(grace, { target: { value: "90" } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(save.disabled).toBe(true); // no reason, no password yet

    fireEvent.change(screen.getByLabelText("Reason for the change"), { target: { value: "longer conference sessions" } });
    expect(save.disabled).toBe(true); // still no password
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/self-service-settings/", {
      ...SETTINGS, grace_minutes: 90, reason: "longer conference sessions", password: "pw",
    }));
  });

  it("refuses a reminder that is not before the deadline", () => {
    const d: Draft = { required: true, policy: "SUSPEND", nums: {
      verification_deadline_hours: "4", reminder_hours_before: "4", resend_cooldown_seconds: "60", resend_max_per_day: "5",
      grace_minutes: "60", max_grace_extensions: "1", registrations_per_device_per_day: "3",
    } };
    expect(draftProblem(d, BOUNDS as any)).toMatch(/reminder must come before the deadline/i);
    expect(draftProblem({ ...d, nums: { ...d.nums, reminder_hours_before: "0" } }, BOUNDS as any)).toBeNull();
    expect(draftProblem({ ...d, nums: { ...d.nums, max_grace_extensions: "4" } }, BOUNDS as any)).toMatch(/from 0 to 3/);
  });

  it("a read-only role sees the values and no Save, reason or password", async () => {
    render(<EmailAccountsSettingsCard canEdit={false} />);
    expect(((await screen.findByLabelText("Time to verify")) as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: /save email accounts settings/i })).toBeNull();
    expect(screen.queryByLabelText("Confirm your password")).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.getByText(/can see the Email accounts settings but not change them/)).toBeTruthy();
  });

  it("says so when the appliance does not report the settings", async () => {
    get.mockResolvedValue({});
    render(<EmailAccountsSettingsCard canEdit />);
    expect(await screen.findByText(/does not report Email accounts settings/)).toBeTruthy();
  });

  it("words a first save", () => {
    expect(changeSummary({ changed_at: "x", new_grace_minutes: 60, old_grace_minutes: null })).toBe("First saved settings");
  });
});
