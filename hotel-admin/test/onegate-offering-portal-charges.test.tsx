// ONEGATE REDESIGN — Internet offering, Guest portal and Charges.
//
// Two rules from the redesign handoff, asserted where they were broken:
//
//   1. "The UI must never show a button the role cannot use." Service plans hard-coded `writable = true`,
//      Internet packages offered Add / Edit / Disable to everyone, and Sign-in methods drew On/Off buttons for
//      the desk, which the server then refused. A read-only role now sees the state and no write control.
//   2. "Every page has a title equal to its menu label." The four Charges screens had none.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import ServicePlansPage from "@/app/(app)/service-plans/page";
import InternetPackagesPage from "@/app/(app)/internet-packages/page";
import SignInMethodsPage from "@/app/(app)/sign-in-methods/page";
import { FinancialHealthView } from "@/components/phase4/financial-health-view";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { SettlementsView } from "@/components/phase4/settlements-view";
import { FinancialRecoveryView } from "@/components/phase4/financial-recovery-view";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;
const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });

if (!("ResizeObserver" in globalThis)) {
  (globalThis as Record<string, unknown>).ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
}

const PLAN = {
  plan_id: "plan-1", code: "GOLD", name: "Gold", enabled: true, current_revision_id: "r1", revision_count: 1,
  down_kbps: 10000, up_kbps: 5000, max_concurrent_devices: 2, used_by_active_packages: 1,
};
const PKG = {
  package_id: "pk1", code: "FREE", name: "Free WiFi", active: true, current_revision_id: "r1", revision_count: 1,
  service_plan_id: "plan-1", service_plan_revision_id: "r1", service_plan_code: "GOLD", price_minor: 0,
};
const PROTECTION = {
  max_failed_attempts: 5, observation_window_seconds: 60, restriction_seconds: 60, is_default: true,
  limits: {
    min_failed_attempts: 3, max_failed_attempts: 20,
    min_observation_window_seconds: 30, max_observation_window_seconds: 3600,
    min_restriction_seconds: 30, max_restriction_seconds: 3600,
  },
  last_change: null,
};

function routes(roles: string[], extra: Record<string, unknown> = {}) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path in extra) return Promise.resolve(extra[path]);
    if (path === "/commercial-packages/plans") return Promise.resolve(list([PLAN]));
    if (path === "/commercial-packages") return Promise.resolve(list([PKG]));
    if (path === "/auth-methods") return Promise.resolve({ voucher: { enabled: true }, guest_account: { enabled: false } });
    if (path === "/guest-signin-protection") return Promise.resolve(PROTECTION);
    if (path === "/pms-routing") return Promise.resolve({ routes: [] });
    if (path.startsWith("/commercial-packages/activity")) {
      return Promise.resolve({
        data: [], meta: { total: 0, limit: 25, offset: 0, has_more: false },
        summary: { in_range: 0, started_in_range: 0, status_counts: { active: 0, ended: 0, other: 0 }, data_bytes: 0,
          active_now: 0, undated: 0, by_package: [], by_source: [], active_by_package: [] },
        range: { from: "2026-09-17T00:00:00Z", to: "2026-09-24T00:00:00Z" },
      });
    }
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("a read-only role is offered no write control", () => {
  it("Service plans: a viewer sees the plans and no Add, Edit or Delete", async () => {
    routes(["site_viewer"]);
    render(<ServicePlansPage />);
    expect(await screen.findByText("Gold", { selector: "button" })).toBeInTheDocument();
    expect(await screen.findByText(/can see the service plans but not change them/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add plan|add the first plan/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^edit$/i })).toBeNull();
    fireEvent.click(screen.getByText("Gold", { selector: "button" }));
    await screen.findByRole("dialog");
    expect(screen.queryByRole("button", { name: /delete…/i })).toBeNull();
  });

  it("Service plans: an admin is offered Add and Edit", async () => {
    routes(["site_admin"]);
    render(<ServicePlansPage />);
    expect(await screen.findByRole("button", { name: /add plan/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^edit$/i })).toBeInTheDocument();
  });

  it("Internet packages: a viewer sees the packages and no Add, Edit or Disable", async () => {
    routes(["site_viewer"]);
    render(<InternetPackagesPage />);
    expect(await screen.findByText("Free WiFi")).toBeInTheDocument();
    expect(await screen.findByText(/can see the internet packages but not change them/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add package/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^edit$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^disable$/i })).toBeNull();
  });

  it("Internet packages: an admin is offered Add package", async () => {
    routes(["site_admin"]);
    render(<InternetPackagesPage />);
    expect(await screen.findByRole("button", { name: /add package/i })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /^edit$/i })).toBeInTheDocument());
  });

  it("Sign-in methods: the desk sees each method's state in words and no switch", async () => {
    routes(["front_office_operator"]);
    render(<SignInMethodsPage />);
    expect(await screen.findByText(/can see which sign-in methods are offered but not change them/i)).toBeInTheDocument();
    expect(screen.getByText("Voucher code")).toBeInTheDocument();
    expect(screen.queryAllByRole("switch")).toHaveLength(0);
    // Guest sign-in protection is read-only for the desk too: the numbers show, no Save.
    expect(await screen.findByText(/can see these settings but not change them/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /save protection settings/i })).toBeNull();
    expect(screen.getByLabelText(/maximum failed attempts/i)).toBeDisabled();
  });

  it("Sign-in methods: an admin gets real switches that save immediately, one key at a time", async () => {
    routes(["site_admin"]);
    put.mockResolvedValue({ voucher: { enabled: false } });
    render(<SignInMethodsPage />);
    const voucher = await screen.findByRole("switch", { name: "Voucher code" });
    expect(voucher).toHaveAttribute("aria-checked", "true");
    fireEvent.click(voucher);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { voucher: { enabled: false } }));
    // Email and SMS cannot be switched on until a sender exists, and say so.
    expect(screen.getByRole("switch", { name: "Email code" })).toBeDisabled();
    expect(screen.getAllByText(/not available until/i).length).toBeGreaterThanOrEqual(2);
  });

  it("Guest sign-in protection: an out-of-range value blocks Save and says the range", async () => {
    routes(["site_admin"]);
    render(<SignInMethodsPage />);
    const attempts = await screen.findByLabelText(/maximum failed attempts/i);
    expect(screen.getByText(/default 5 attempts; allowed 3–20/i)).toBeInTheDocument();
    fireEvent.change(attempts, { target: { value: "1" } });
    expect(await screen.findByText(/enter a whole number between 3 and 20/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /save protection settings/i })).toBeDisabled();
  });
});

describe("every Charges screen has its menu label as its title", () => {
  const HEALTH = {
    outbox_queued: 0, outbox_in_flight: 0, outbox_held_recovery: 0, outbox_oldest_age_seconds: 0,
    postings_unknown: 0, review_queue_open: 0, review_oldest_age_seconds: 0,
    payments_created: 0, payments_pending: 0, payments_unknown: 0, payments_oldest_age_seconds: 0,
    settlements_required: 0, settlements_in_progress: 0, settlements_manual_review: 0, settlements_failed: 0,
    recovery_active: false, recovery_epoch: 1, recovery_holds_open: 0,
    payment_account_configured: true, provider_egress_enabled: false, status: "OK", reasons: [],
  };

  it.each([
    ["Charge health", () => <FinancialHealthView />],
    ["Manual review", () => <ManualReviewView />],
    ["Settlements", () => <SettlementsView />],
    ["Recovery", () => <FinancialRecoveryView />],
  ])("%s", async (title, el) => {
    routes([], {
      "/financial-ops/health": { health: HEALTH },
      "/financial-review/queue": { queue: [] },
      "/financial-review/actions": { actions: [] },
      "/financial-ops/settlements": { settlements: [] },
      "/financial-ops/recovery": { recovery: { Epoch: 1, Reason: "INITIAL", Active: false, HeldTotal: 0, HeldOpen: 0, EnteredAt: "", ReleasedAt: "" } },
      "/financial-ops/recovery/holds": { holds: [] },
      "/financial-ops/recovery/zero-attempt": { queue: [], limit: 200, note: "", eligibility: "" },
    });
    render(el());
    expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
    expect(screen.getByText("Charges")).toBeInTheDocument();
  });

  it("Settlements offers no refund button, even with a settled charge on screen", async () => {
    routes([], {
      "/financial-ops/settlements": { settlements: [{ settlement_id: "s1", purchase_id: "p1", method: "ONLINE_PAYMENT",
        status: "SETTLED", purchase_state: "GRANTED", amount_minor: 1000, currency: "USD", currency_exponent: 2 }] },
    });
    render(<SettlementsView />);
    expect(await screen.findByText("10.00 USD")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /refund/i })).toBeNull();
  });
});
