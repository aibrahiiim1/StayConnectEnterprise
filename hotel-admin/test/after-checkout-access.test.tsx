import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import {
  DEFAULT_BOUNDS,
  changeLine,
  compareSnapshots,
  describePublishFailure,
  draftFromState,
  draftToTerms,
  fmtHours,
  isSwitchEvent,
  normaliseReasonCode,
  termsAt,
  validateDraft,
  type AccessHistoryItem,
  type AccessState,
} from "@/lib/api/after-checkout-access";

// jsdom implements no ResizeObserver; the switch measures itself on mount.
class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", () => ({
  api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: (...a: any[]) => put(...a), del: vi.fn() },
  ApiError: class ApiError extends Error {},
}));

const GB = 1024 ** 3;
const policy = {
  duration_hours: 12, duration_seconds: 43200, down_kbps: 5000, up_kbps: 2000,
  data_quota_bytes: GB, device_limit: 5, device_policy: "REJECT_NEW_DEVICE",
};
const bounds = {
  duration_hours: { min: 1, max: 72, default: 12, unit: "hours" },
  down_kbps: { min: 64, max: 1000000, default: 5000, unit: "kbps" },
  up_kbps: { min: 64, max: 1000000, default: 2000, unit: "kbps" },
  data_quota_bytes: { min: 10485760, max: 107374182400, default: GB, unit: "bytes" },
  device_limit: { min: 1, max: 20, default: 5, unit: "devices" },
};
const base: AccessState = {
  enabled: true, configured: true, config_version: 3, policy,
  effective: { source: "PUBLISHED", policy }, bounds,
  supported_device_policies: ["REJECT_NEW_DEVICE"], emergency_history: { count: 0 },
  eligibility: "Only devices authorised on the same stay before check-out.",
  sign_in: "Room number and reservation number only.",
};
const unconfigured: AccessState = {
  ...base, configured: false, config_version: 0, policy: null,
  effective: { source: "EMERGENCY_FALLBACK", policy: { ...policy, duration_hours: 1, duration_seconds: 3600, data_quota_bytes: 500 * 1024 * 1024 } },
  emergency_history: { count: 2, last_at: "2026-10-01T10:00:00Z" },
};
const disabled: AccessState = { ...base, enabled: false, effective: { source: "DISABLED" } };

const snap = (hours: number, quota = GB) => ({
  grace_duration_seconds: hours * 3600, grace_down_kbps: 5000, grace_up_kbps: 2000,
  grace_data_quota_bytes: quota, grace_device_limit: 5, grace_device_limit_policy: "REJECT_NEW_DEVICE",
});
const history: AccessHistoryItem[] = [
  { config_version: 4, published_at: "2026-10-03T10:00:00Z", actor: "Mona", reason_code: "SEASON_CHANGE",
    policy: { change: "AFTER_CHECKOUT_ENABLED", after_checkout_enabled: false } },
  { config_version: 3, published_at: "2026-10-02T10:00:00Z", actor: "Ali", reason_code: "POLICY_CHANGE", policy: snap(12) },
  { config_version: 2, published_at: "2026-10-01T10:00:00Z", actor: "Ali", reason_code: "INITIAL_SETUP", policy: snap(8) },
];

beforeEach(() => { get.mockReset(); put.mockReset(); });
afterEach(() => vi.resetModules());

function mockApi(states: AccessState[], hist: AccessHistoryItem[] = [], available = true) {
  let i = 0;
  get.mockImplementation((path: string) => {
    if (path === "/after-checkout-access/history") return Promise.resolve({ data: hist, available });
    if (path === "/after-checkout-access") return Promise.resolve(states[Math.min(i++, states.length - 1)]);
    if (path.startsWith("/post-stay-profiles")) return Promise.resolve({ profiles: [] });
    return Promise.resolve({});
  });
}

async function renderScreen(canWrite = true) {
  const { AfterCheckoutAccessScreen } = await import("@/components/after-checkout-access/after-checkout-access-screen");
  render(<AfterCheckoutAccessScreen canWrite={canWrite} canActOnReconnections={canWrite} />);
  await screen.findByText("What a departing guest receives");
}

const setField = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe("After Check-out Access: pure rules", () => {
  it("validates against the bounds the API sent, not hard-coded ones", () => {
    const d = draftFromState(base, ["REJECT_NEW_DEVICE"]);
    expect(validateDraft(d, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"])).toEqual({});
    expect(validateDraft({ ...d, hours: "73" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).hours).toMatch(/1 and 72/);
    expect(validateDraft({ ...d, hours: "1.5" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).hours).toBeTruthy();
    expect(validateDraft({ ...d, downMbps: "0.05" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).downMbps).toBeTruthy();
    expect(validateDraft({ ...d, quotaMb: "9" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).quotaMb).toBeTruthy();
    expect(validateDraft({ ...d, deviceLimit: "21" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).deviceLimit).toBeTruthy();
    const tight = { ...DEFAULT_BOUNDS, duration_hours: { ...DEFAULT_BOUNDS.duration_hours, max: 24 } };
    expect(validateDraft({ ...d, hours: "48" }, tight, ["REJECT_NEW_DEVICE"]).hours).toBeTruthy();
    expect(validateDraft({ ...d, devicePolicy: "OTHER" }, DEFAULT_BOUNDS, ["REJECT_NEW_DEVICE"]).devicePolicy).toBeTruthy();
  });

  it("converts the draft to the API's units once", () => {
    const t = draftToTerms({ ...draftFromState(base, ["REJECT_NEW_DEVICE"]), downMbps: "7.5", quotaMb: "2048" });
    expect(t).toMatchObject({ duration_hours: 12, down_kbps: 7500, up_kbps: 2000, data_quota_bytes: 2 * GB, device_limit: 5 });
  });

  it("seeds an unconfigured site from the API defaults", () => {
    const d = draftFromState(unconfigured, ["REJECT_NEW_DEVICE"]);
    expect(d).toMatchObject({ hours: "12", downMbps: "5", upMbps: "2", quotaMb: "1024", deviceLimit: "5" });
  });

  it("words hours and reasons", () => {
    expect(fmtHours(1)).toBe("1 hour");
    expect(fmtHours(12)).toBe("12 hours");
    expect(fmtHours(0.5)).toBe("30 minutes");
    expect(normaliseReasonCode("late checkout")).toBe("LATE_CHECKOUT");
    expect(normaliseReasonCode("123")).toBe("");
  });

  it("reads switch events and term changes from the history", () => {
    expect(isSwitchEvent(history[0])).toBe(true);
    expect(changeLine(history, 0)).toMatch(/turned off/);
    // A switch event has no terms: the terms in force are the next older publication.
    expect(termsAt(history, 0)?.grace_duration_seconds).toBe(12 * 3600);
    expect(changeLine(history, 1)).toMatch(/Duration after check-out 8 hours → 12 hours/);
    expect(compareSnapshots(history[2].policy, history[1].policy).filter((c) => c.changed).map((c) => c.key)).toEqual(["hours"]);
  });

  it("maps the contract's failure codes to sentences and fields", () => {
    expect(describePublishFailure({ status: 409, code: "version_conflict" }).conflict).toBe(true);
    expect(describePublishFailure({ status: 401, code: "reauth" }).field).toBe("password");
    expect(describePublishFailure({ status: 400, code: "reason_invalid" }).field).toBe("reason");
    expect(describePublishFailure({ status: 400, code: "validation", message: "bounds" }).message).toMatch(/bounds/);
  });
});

describe("After Check-out Access page", () => {
  it("shows the effective source and the plain-language contract", async () => {
    mockApi([base], history);
    await renderScreen();
    expect(screen.getByTestId("aca-source").getAttribute("data-source")).toBe("PUBLISHED");
    const sentence = screen.getByTestId("aca-sentence").textContent!;
    expect(sentence).toMatch(/12 hours after the verified check-out/);
    expect(sentence).toMatch(/share 1 GB of data in total/);
    expect(sentence).toMatch(/only the room number and the reservation number/);
    expect(sentence).toMatch(/New devices are refused/);
    expect(screen.getByText("Future check-outs only")).toBeInTheDocument();
    expect(screen.queryByText(/Grace Period/)).toBeNull();
  });

  it("shows the emergency fallback and the disabled state", async () => {
    mockApi([unconfigured]);
    await renderScreen();
    expect(screen.getByTestId("aca-source").getAttribute("data-source")).toBe("EMERGENCY_FALLBACK");
    expect(screen.getByText("Departing guests are on the emergency fallback")).toBeInTheDocument();
    document.body.innerHTML = "";
    vi.resetModules();
    mockApi([disabled]);
    await renderScreen();
    expect(screen.getByTestId("aca-source").getAttribute("data-source")).toBe("DISABLED");
    expect(screen.getByTestId("aca-sentence").textContent).toMatch(/turned off/);
  });

  it("lists switch events in the history table", async () => {
    mockApi([base], history);
    await renderScreen();
    const list = screen.getByRole("region", { name: "After Check-out Access history" });
    expect(within(list).getByText(/Turned off/)).toBeInTheDocument();
    expect(within(list).getAllByText(/Ali/).length).toBeGreaterThan(0);
  });

  it("hides the edit button for a read-only role", async () => {
    mockApi([base], history);
    await renderScreen(false);
    expect(screen.queryByRole("button", { name: /Edit settings/ })).toBeNull();
    expect(screen.getByText(/can view these settings but not change them/)).toBeInTheDocument();
  });

  it("shows bound hints, refuses out-of-range entries, then saves whole with the viewed version, reason and password", async () => {
    mockApi([base, { ...base, config_version: 4 }], history);
    put.mockResolvedValue({ config_version: 4 });
    await renderScreen();
    fireEvent.click(screen.getByRole("button", { name: /Edit settings/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Allowed: 1 to 72 hours/)).toBeInTheDocument();

    setField(/Duration after verified check-out/, "100");
    fireEvent.click(within(dialog).getByRole("button", { name: /Review changes/ }));
    expect(await within(dialog).findByText(/between 1 and 72/)).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();

    setField(/Duration after verified check-out/, "24");
    fireEvent.click(within(dialog).getByRole("button", { name: /Review changes/ }));
    await within(dialog).findByText("What changes");
    const save = within(dialog).getByRole("button", { name: "Save settings" });
    expect(save).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Confirm your password"), { target: { value: "pw" } });
    expect(save).not.toBeDisabled();
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/after-checkout-access");
    expect(body).toMatchObject({
      duration_hours: 24, down_kbps: 5000, up_kbps: 2000, data_quota_bytes: GB, device_limit: 5,
      device_policy: "REJECT_NEW_DEVICE", expected_config_version: 3, reason_code: "POLICY_CHANGE", password: "pw",
    });
    expect(body).not.toHaveProperty("enabled");
  });

  it("sends only the switch when only the switch changed", async () => {
    mockApi([base, disabled], history);
    put.mockResolvedValue({ config_version: 4 });
    await renderScreen();
    fireEvent.click(screen.getByRole("button", { name: /Edit settings/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("switch"));
    fireEvent.click(within(dialog).getByRole("button", { name: /Review changes/ }));
    await within(dialog).findByText("What changes");
    fireEvent.change(within(dialog).getByLabelText("Confirm your password"), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0][1];
    expect(body.enabled).toBe(false);
    expect(body).not.toHaveProperty("duration_hours");
    expect(body.expected_config_version).toBe(3);
  });

  it("keeps the draft and reloads on a version conflict", async () => {
    mockApi([base, { ...base, config_version: 4 }], history);
    put.mockRejectedValue({ status: 409, code: "version_conflict", message: "stale" });
    await renderScreen();
    fireEvent.click(screen.getByRole("button", { name: /Edit settings/ }));
    const dialog = await screen.findByRole("dialog");
    setField(/Duration after verified check-out/, "30");
    fireEvent.click(within(dialog).getByRole("button", { name: /Review changes/ }));
    await within(dialog).findByText("What changes");
    fireEvent.change(within(dialog).getByLabelText("Confirm your password"), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
    expect(await within(dialog).findByText(/Someone else changed this setting/)).toBeInTheDocument();
    expect((screen.getByLabelText(/Duration after verified check-out/) as HTMLInputElement).value).toBe("30");
  });
});
