// THE STANDING "APPLY CURRENT SETTINGS TO PACKAGES" ACTION.
//
// The state this exists for is real and was on the PRE-LIVE appliance: service plan "Free Internet" is on
// revision 4 (10/5 Mbps) while the "Freee" package still pins revision 3 (2/2 Mbps). Package Edit keeps the
// pin when the plan selection is unchanged — deliberately, so a rename never carries a technical change — so
// the only way to move a package forward was to publish ANOTHER plan revision just to be offered the prompt.
//
// What is guarded here is mostly what the action must NOT do: it must not publish a service plan revision, it
// must not touch a package that is already current, and it must not drop any part of the package it rewrites.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, body?: unknown) {
      super(typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : `HTTP ${status}`);
      this.status = status;
    }
  }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import ServicePlansPage from "@/app/(app)/service-plans/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const p = api.post as unknown as ReturnType<typeof vi.fn>;
function list<T>(data: T[]) { return { data, meta: { has_more: false } }; }

// The live shape, names and all: one plan on revision 4, one package left on revision 3, one already current.
const PLANS = [{
  plan_id: "plan-free", code: "FREE", name: "Free Internet", enabled: true,
  current_revision_id: "rev4", revision_count: 4,
  down_kbps: 10000, up_kbps: 5000, max_concurrent_devices: 1, used_by_active_packages: 2,
}];
const PACKAGES = [
  {
    package_id: "pkg-freee", code: "FREEE", name: "Freee", active: true,
    service_plan_id: "plan-free", service_plan_revision_id: "rev3",
  },
  {
    package_id: "pkg-current", code: "CURRENT", name: "Already current", active: true,
    service_plan_id: "plan-free", service_plan_revision_id: "rev4",
  },
];

const FREEE_CURRENT = {
  code: "FREEE",
  display: { name: "Freee" },
  duration_policy: { end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 },
  eligibility_rules: [{ type: "AUTH_METHOD", value: { methods: ["pms"] } }],
  grant_tiers: [{ order: 10, value: { down_kbps: 2000 } }],
  visible_from: "2026-01-01T00:00:00Z",
  visible_until: null,
};

function mockLoad(packages = PACKAGES) {
  g.mockImplementation((path: string) => {
    if (path === "/commercial-packages/plans") return Promise.resolve(list(PLANS));
    if (path === "/commercial-packages") return Promise.resolve(list(packages));
    if (path === "/commercial-packages/pkg-freee/current") return Promise.resolve(FREEE_CURRENT);
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("ServicePlansPage — stale package pin", () => {
  it("flags the plan whose packages are behind, and offers the action", async () => {
    mockLoad();
    render(<ServicePlansPage />);
    expect(await screen.findByText("Free Internet")).toBeInTheDocument();
    expect(screen.getByTestId("stale-count-FREE").textContent).toContain("1 still on older settings");
    expect(screen.getByTestId("apply-current-FREE")).toBeInTheDocument();
  });

  it("offers ONLY the stale package — one already on the current revision is not listed", async () => {
    mockLoad();
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));

    expect(screen.getByTestId("repin-title").textContent).toBe("Apply current settings to packages");
    expect(screen.getByLabelText("repin-FREEE")).toBeInTheDocument();
    expect(screen.queryByLabelText("repin-CURRENT")).toBeNull();
  });

  it("shows no action at all when every package is already on the current settings", async () => {
    mockLoad([PACKAGES[1]]);
    render(<ServicePlansPage />);
    expect(await screen.findByText("Free Internet")).toBeInTheDocument();
    expect(screen.queryByTestId("apply-current-FREE")).toBeNull();
    expect(screen.queryByTestId("stale-count-FREE")).toBeNull();
  });

  it("changes NOTHING until the operator picks a package and applies", async () => {
    // Opening the prompt is not an action. The real Freee package must stay exactly as it is until chosen.
    mockLoad();
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    expect(p).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /not now/i }));
    expect(p).not.toHaveBeenCalled();
    expect(screen.getByRole("status").textContent).toContain("Nothing was changed");
  });

  it("publishes a PACKAGE revision only, pinned to the plan's EXISTING current revision", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    const [path, body] = p.mock.calls[0];
    // ONE post, to the package endpoint. Nothing was posted to /commercial-packages/plans, so no service
    // plan revision was created — which is the whole point of the action existing separately.
    expect(path).toBe("/commercial-packages");
    expect(p.mock.calls.some((c: unknown[]) => String(c[0]).includes("/plans"))).toBe(false);
    // ...and it pins the revision the plan ALREADY had.
    expect(body.service_plan_revision_id).toBe("rev4");
  });

  it("preserves the package's rules, tiers, duration, sale window and display exactly", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    const body = p.mock.calls[0][1];
    expect(body.code).toBe("FREEE");
    expect(body.display).toEqual({ name: "Freee" });
    expect(body.duration_policy).toEqual({ end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 });
    expect(body.eligibility_rules).toEqual([{ type: "AUTH_METHOD", value: { methods: ["pms"] } }]);
    expect(body.grant_tiers).toEqual([{ order: 10, grant: { down_kbps: 2000 } }]);
    expect(body.visible_from).toBe("2026-01-01T00:00:00Z");
    // The configuration came from the package's own current revision, not from the list row.
    expect(g).toHaveBeenCalledWith("/commercial-packages/pkg-freee/current");
  });

  it("tells the operator that guests already connected are unaffected", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("1 package updated"));
    const msg = screen.getByRole("status").textContent ?? "";
    expect(msg).toContain("Existing guest access is unchanged");
    expect(msg.toLowerCase()).not.toContain("revision");
  });

  it("never asks the operator for a revision id", async () => {
    mockLoad();
    const { container } = render(<ServicePlansPage />);
    await screen.findByText("Free Internet");
    fireEvent.click(screen.getByTestId("apply-current-FREE"));
    expect(container.innerHTML).not.toContain("rev3");
    expect(container.innerHTML).not.toContain("rev4");
    expect(screen.queryByLabelText(/revision id/i)).toBeNull();
  });
});
