import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";

// "NOTHING PUBLISHED YET" IS NOT "NOTHING IS HAPPENING".
//
// A guest who checks out of a site with no published policy still receives a grace period -- the built-in
// emergency fallback -- and their departure raises a critical alert. These assert the question the page exists
// to answer: what does a guest checking out right now actually get, and did anyone choose it.

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: vi.fn(),
    put: (...a: any[]) => put(...a),
    del: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => {
  get.mockReset();
  put.mockReset();
});
afterEach(() => vi.resetModules());

// The real built-in constants, as data-plane/internal/grace reports them: 60 min, 5/2 Mbps, 500 MB,
// reject-new, device limit 0. If these ever change in Go, this fixture is deliberately the thing that looks wrong.
const emergency = {
  source: "EMERGENCY_FALLBACK",
  duration_seconds: 3600,
  down_kbps: 5000,
  up_kbps: 2000,
  data_quota_bytes: 500 * 1024 * 1024,
  device_limit: 0,
  device_limit_policy: "REJECT_NEW_DEVICE",
  policy_version: "EMERGENCY_GRACE_V1",
};

const publishedPolicy = {
  source: "PUBLISHED",
  duration_seconds: 1800,
  down_kbps: 10000,
  up_kbps: 4000,
  data_quota_bytes: 1024 * 1024 * 1024,
  device_limit: 2,
  device_limit_policy: "REJECT_NEW_DEVICE",
  eligibility_window_seconds: 7200,
  config_version: 4,
};

function mockGrace(state: Record<string, any>, history: any[] = [], historyAvailable = true) {
  get.mockImplementation((path: string) => {
    if (path === "/checkout-grace/history")
      return Promise.resolve({ data: history, meta: { has_more: false }, available: historyAvailable });
    if (path === "/checkout-grace") return Promise.resolve(state);
    return Promise.resolve({});
  });
}

async function renderScreen(canWrite = true) {
  const { CheckoutGraceScreen } = await import("@/components/checkout-grace/checkout-grace-screen");
  const r = render(<CheckoutGraceScreen canWrite={canWrite} />);
  await screen.findByText("What a departing guest receives");
  return r;
}

const v4 = {
  config_version: 4,
  published_at: "2026-09-17T09:00:00Z",
  actor: "Dana Whitfield",
  reason_code: "GUEST_FEEDBACK",
  policy: {
    grace_duration_seconds: 1800,
    grace_down_kbps: 10000,
    grace_up_kbps: 4000,
    grace_data_quota_bytes: 1024 * 1024 * 1024,
    grace_device_limit: 2,
    grace_device_limit_policy: "REJECT_NEW_DEVICE",
    eligibility_window_seconds: 7200,
  },
};

describe("checkout grace states what is actually in force", () => {
  it("names the emergency fallback as something the hotel did not choose, with its real terms", async () => {
    mockGrace({
      published: false,
      config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: emergency,
      emergency_history: { count: 6, last_at: "2026-09-14T07:47:27Z" },
    });
    await renderScreen();

    // The emergency warning, as a distinct status tone and in words.
    const attention = screen.getByLabelText("Needs attention");
    expect(within(attention).getByText(/Departing guests are on the emergency fallback/i)).toBeTruthy();
    expect(within(attention).getByText(/not a decision made for this hotel/i)).toBeTruthy();
    expect(screen.getAllByText("Emergency fallback").length).toBeGreaterThan(0);

    // The real terms, not a shrug.
    const strip = screen.getByLabelText("Grace allowance");
    expect(within(strip).getByText("1 h")).toBeTruthy();
    expect(within(strip).getByText("5 Mbps")).toBeTruthy();
    expect(within(strip).getByText("2 Mbps")).toBeTruthy();
    expect(within(strip).getByText("500 MB")).toBeTruthy();

    // And that it has ALREADY happened -- six times.
    const usedCard = screen.getByText("Emergency fallback used").closest(".rounded-lg")!;
    expect(usedCard.textContent).toMatch(/6/);
    expect(usedCard.textContent).toMatch(/critical alert/i);
  });

  it("builds the guest sentence from the fields, including the device rule", async () => {
    mockGrace({
      published: false,
      config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: emergency,
      emergency_history: { count: 0 },
    });
    await renderScreen();
    const sentence = screen.getByTestId("grace-sentence").textContent!;
    expect(sentence).toMatch(/keeps it for 1 hour after checkout/);
    expect(sentence).toMatch(/up to 5 Mbps down and 2 Mbps up/);
    expect(sentence).toMatch(/with 500 MB of data/);
    expect(sentence).toMatch(/Devices already connected at checkout stay online; new devices are refused/);
  });

  it("calls the fallback's zero device limit 'no extra devices', not '0'", async () => {
    mockGrace({ published: false, config_version: 0, effective: emergency, emergency_history: { count: 0 } });
    await renderScreen();
    const details = screen.getByLabelText("Policy details");
    expect(within(details).getByText(/No extra devices/)).toBeTruthy();
  });

  it("does not call a published policy an emergency, and shows its version and who changed it", async () => {
    mockGrace(
      {
        published: true,
        config_version: 4,
        supported_device_policies: ["REJECT_NEW_DEVICE"],
        effective: publishedPolicy,
        emergency_history: { count: 0 },
      },
      [v4],
    );
    await renderScreen();

    expect(screen.getByText("Hotel policy · v4")).toBeTruthy();
    expect(screen.queryByText(/Departing guests are on the emergency fallback/i)).toBeNull();
    expect(screen.getByText("by Dana Whitfield")).toBeTruthy();

    const strip = screen.getByLabelText("Grace allowance");
    expect(within(strip).getByText("30 min")).toBeTruthy();
    expect(within(strip).getByText("1 GB")).toBeTruthy();
    expect(screen.getByTestId("grace-sentence").textContent).toMatch(/30 minutes after checkout/);
    const details = screen.getByLabelText("Policy details");
    expect(within(details).getByText("2 h")).toBeTruthy();
    expect(within(details).getByText(/Up to 2 devices/)).toBeTruthy();
    expect(within(details).getByText(/built automatically from this policy/)).toBeTruthy();
  });

  it("flags the fallback being used AFTER the policy in force was published", async () => {
    mockGrace(
      {
        published: true,
        config_version: 4,
        effective: publishedPolicy,
        emergency_history: { count: 2, last_at: "2026-09-18T10:00:00Z" },
      },
      [v4],
    );
    await renderScreen();
    const attention = screen.getByLabelText("Needs attention");
    expect(within(attention).getByText(/emergency fallback was used after this policy was published/i)).toBeTruthy();
    expect(within(attention).getByRole("link", { name: /operational alerts/i }).getAttribute("href")).toBe(
      "/operational-alerts",
    );
  });

  it("does not flag fallback uses that happened BEFORE the policy was published", async () => {
    mockGrace(
      {
        published: true,
        config_version: 4,
        effective: publishedPolicy,
        emergency_history: { count: 2, last_at: "2026-09-10T10:00:00Z" },
      },
      [v4],
    );
    await renderScreen();
    expect(screen.queryByText(/used after this policy was published/i)).toBeNull();
  });

  it("says plainly that publishing only affects future checkouts", async () => {
    mockGrace({ published: false, config_version: 0, effective: emergency, emergency_history: { count: 0 } });
    await renderScreen();
    expect(screen.getByText("Future checkouts only")).toBeTruthy();
    expect(screen.getByText(/keeps the exact terms they were given at checkout/i)).toBeTruthy();
  });

  it("does not claim the fallback has been used when it has not", async () => {
    mockGrace({ published: false, config_version: 0, effective: emergency, emergency_history: { count: 0 } });
    await renderScreen();
    expect(screen.getByText("Never used on this appliance")).toBeTruthy();
    expect(screen.queryByText(/Most recently/i)).toBeNull();
  });

  it("says it CANNOT SEE the history rather than claiming there is none", async () => {
    mockGrace(
      { published: true, config_version: 2, effective: { ...publishedPolicy, config_version: 2 }, emergency_history: { count: 0 } },
      [],
      false,
    );
    await renderScreen();
    expect(screen.getAllByText(/cannot be read on this appliance/i).length).toBeGreaterThan(0);
    expect(screen.getByText(/does not mean no policy has been published/i)).toBeTruthy();
    expect(screen.queryByLabelText("Checkout grace policy history")).toBeNull();
    expect(screen.queryByText("No version published yet")).toBeNull();
  });

  it("uses operator wording only — no internal identifiers or codes on screen", async () => {
    mockGrace(
      {
        published: true,
        config_version: 4,
        effective: publishedPolicy,
        emergency_history: { count: 1, last_at: "2026-09-18T10:00:00Z" },
      },
      [v4],
    );
    const { container } = await renderScreen();
    const text = container.textContent ?? "";
    for (const bad of [
      /iam_v2/i,
      /phase[\s-]?\d/i,
      /\bT0\d{3}\b/,
      /\bD\d{2}\b/,
      /REJECT_NEW_DEVICE/,
      /EMERGENCY_GRACE/,
      /CHECKOUT_GRACE_V1/,
      /package revision/i,
      /config_version/,
      /entitlement/i,
    ]) {
      expect(text, `unexpected wording ${bad}`).not.toMatch(bad);
    }
  });

  it("a read-only role sees the terms but cannot open the editor", async () => {
    mockGrace({ published: false, config_version: 0, effective: emergency, emergency_history: { count: 0 } });
    await renderScreen(false);
    expect((screen.getByRole("button", { name: /Create hotel policy/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/can view this policy but not change it/i)).toBeTruthy();
  });

  it("shows a retryable error when the policy cannot be loaded", async () => {
    get.mockImplementation(() => Promise.reject(Object.assign(new Error("boom"), { status: 500 })));
    const { CheckoutGraceScreen } = await import("@/components/checkout-grace/checkout-grace-screen");
    render(<CheckoutGraceScreen canWrite />);
    expect((await screen.findByRole("alert")).textContent).toMatch(/could not be loaded/i);
    expect(screen.getByRole("button", { name: /Try again/i })).toBeTruthy();
  });
});
