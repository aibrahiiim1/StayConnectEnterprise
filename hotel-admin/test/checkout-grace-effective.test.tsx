import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";

// "NOTHING PUBLISHED YET" IS NOT "NOTHING IS HAPPENING", and the screen used to imply it was.
//
// A guest who checks out of a site with no published policy still receives a grace period -- the built-in
// emergency fallback -- and their departure raises a critical alert. The page said only "(nothing published
// yet)", so an operator could read it, see no numbers, and conclude the feature was dormant. On the live
// PRE-LIVE appliance it had already run six times by then.
//
// These assert the question the page exists to answer: what does a guest checking out right now actually
// get, and did anyone choose it.

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    put: (...a: any[]) => put(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  del.mockReset();
});
afterEach(() => vi.resetModules());

// The real built-in constants, as data-plane/internal/grace reports them: 60 min, 5/2 Mbps, 500 MB,
// reject-new. If these ever change in Go, this fixture is deliberately the thing that looks wrong.
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
  config_version: 4,
};

function mockGrace(state: Record<string, any>, packages: any[] = [], history: any[] = []) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/checkout-grace/packages") return Promise.resolve({ data: packages, meta: { has_more: false } });
    if (path === "/checkout-grace/history") return Promise.resolve({ data: history, meta: { has_more: false } });
    if (path === "/checkout-grace") return Promise.resolve(state);
    return Promise.resolve({});
  });
}

async function renderForm() {
  const { CheckoutGraceForm } = await import("@/components/phase3/checkout-grace-form");
  render(<CheckoutGraceForm canWrite />);
}

describe("checkout grace states what is actually in force", () => {
  it("names the emergency fallback as something the hotel did not choose, and shows its real terms", async () => {
    mockGrace({
      published: false,
      config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: emergency,
      emergency_history: { count: 6, last_at: "2026-09-14T07:47:27Z" },
    });
    await renderForm();

    await screen.findByText("In force right now");
    // The badge, specifically: the phrase also appears in the explanatory prose, so an unqualified match
    // would be ambiguous and would pass for the wrong reason.
    // NOT A POLICY ANYONE CHOSE. The distinction is the whole point: an operator must not read a safe default
    // as a considered decision.
    expect(screen.getByText(/Emergency fallback · not a policy this hotel chose/i)).toBeTruthy();

    // The real terms, not a shrug. An operator cannot decide whether 60 minutes is right for their property
    // without being told it is 60 minutes.
    const dl = screen.getByLabelText("Effective checkout grace");
    expect(within(dl).getByText("1 h")).toBeTruthy();
    expect(within(dl).getByText("5 Mbps")).toBeTruthy();
    expect(within(dl).getByText("2 Mbps")).toBeTruthy();
    expect(within(dl).getByText("500 MB")).toBeTruthy();

    // And that it has ALREADY happened -- the fact that turns an abstract warning into a thing to act on.
    // Asserted on the sentence rather than on a bare "6", which also matches the timestamp beside it.
    const history = screen.getByText(/already been used/i);
    expect(history.textContent).toMatch(/already been used\s*6\s*times/i);
  });

  it("does not call a published policy an emergency, and shows its version", async () => {
    mockGrace({
      published: true,
      config_version: 4,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: publishedPolicy,
      emergency_history: { count: 0 },
    });
    await renderForm();

    await screen.findByText("In force right now");
    expect(screen.getByText(/Hotel policy · version 4/i)).toBeTruthy();
    expect(screen.queryByText(/Emergency fallback · not a policy this hotel chose/i)).toBeNull();
    expect(screen.queryByText(/built-in emergency terms/i)).toBeNull();

    const dl = screen.getByLabelText("Effective checkout grace");
    expect(within(dl).getByText("30 min")).toBeTruthy();
    expect(within(dl).getByText("1.0 GB")).toBeTruthy();
  });

  // THE QUESTION AN OPERATOR ASKS NEXT, and the expensive wrong answer.
  //
  // Someone publishing a shorter policy while a guest is mid-grace must not believe they have just cut that
  // guest off, and someone publishing a longer one must not believe they have extended them. The page says so
  // in both states rather than leaving it to be assumed.
  it("says plainly that publishing only affects future checkouts", async () => {
    mockGrace({
      published: false,
      config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: emergency,
      emergency_history: { count: 0 },
    });
    await renderForm();
    expect(await screen.findByText(/applies to future checkouts only/i)).toBeTruthy();
    expect(screen.getByText(/keeps the exact terms they were given at checkout/i)).toBeTruthy();
  });

  // A site that has never had a conversion should not be told it has. Silence is the correct report, and the
  // mirror-image mistake -- inventing a count -- would undermine the panel that matters.
  it("does not claim the fallback has been used when it has not", async () => {
    mockGrace({
      published: false,
      config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: emergency,
      emergency_history: { count: 0 },
    });
    await renderForm();
    await screen.findByText("In force right now");
    expect(screen.queryByText(/already been used/i)).toBeNull();
  });
});
