import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// Recovery bounds belong to one interface; unreadable provenance must differ from an empty history.

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

const iface = {
  id: "i1",
  connector_kind: "protel-fias",
  display_label: "Main PMS",
  lifecycle_state: "ACTIVE",
  current_revision_id: "r1",
  current_revision_no: 1,
  revision_count: 1,
  published: true,
  secret_generation: 1,
  secret_rotated_at: new Date().toISOString(),
};

const health = {
  pms_interface_id: "i1",
  room_auth_ready: true,
  transport_status: "CONNECTED",
  continuity_status: "CONTINUOUS",
  sync_status: "IN_SYNC",
  materialization_ready: true,
  in_house_stays: 12,
  pending_events: 0,
  review_events: 0,
};

const recovery = {
  backoff_min_ms: 750,
  backoff_max_ms: 45000,
  stable_reset_seconds: 90,
  link_down_alert_seconds: 1200,
  blocked_after_refusals: 5,
  config_version: 4,
  is_default: false,
};

const revisions = [
  { id: "r1", revision_no: 1, source_timezone: "Europe/Berlin", posting_target_model: "UNSET",
    normalization_version: 1, config: { endpoint: "10.0.0.1:5003" }, published: true },
];

function mockPage(over: Record<string, any> = {}) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/pms-interfaces") return Promise.resolve({ interfaces: [iface] });
    if (path.endsWith("/health")) return Promise.resolve({ health });
    if (path.endsWith("/connection-settings")) {
      if (over.recoveryFails) return Promise.reject(new Error("nope"));
      return Promise.resolve(over.recovery ?? recovery);
    }
    if (path.endsWith("/revisions")) {
      return Promise.resolve({
        revisions: over.revisions ?? revisions,
        provenance_status: over.provenanceStatus ?? "AVAILABLE",
      });
    }
    return Promise.resolve({ interface: iface, guest_networks: [] });
  });
}

async function openManage() {
  const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
  render(<Page />);
  await screen.findByText("Main PMS");
  await userEvent.click(screen.getByRole("button", { name: "Manage" }));
  await userEvent.click(await screen.findByRole("tab", { name: "Configuration" }));
}

describe("connection recovery is scoped to one interface", () => {
  it("reads this interface's bounds from its own endpoint, not a site-wide one", async () => {
    mockPage();
    await openManage();

    await screen.findByRole("heading", { name: "Connection recovery" });

    // Read interface-level settings so connections do not share one site's recovery bounds.
    await waitFor(() =>
      expect(get.mock.calls.some(([p]) => p === "/pms-interfaces/i1/connection-settings")).toBe(true));
    expect(get.mock.calls.some(([p]) => String(p).includes("/pms-roster-reconciliation"))).toBe(false);

    expect(await screen.findByDisplayValue("750")).toBeTruthy();
    expect(screen.getByDisplayValue("45000")).toBeTruthy();
    expect(screen.getByDisplayValue("1200")).toBeTruthy();
  });

  it("no longer claims the values govern the whole property", async () => {
    mockPage();
    await openManage();
    await screen.findByRole("heading", { name: "Connection recovery" });

    expect(screen.queryByText(/apply to the whole site/i)).toBeNull();
    expect(screen.queryByText(/changing a value here changes it for all of them/i)).toBeNull();
    expect(screen.getByText(/These apply to this connection only\./i)).toBeTruthy();
    expect(screen.getByText(/keeps its own values and is unaffected/i)).toBeTruthy();
  });

  it("saves to this interface, carrying only the field that changed", async () => {
    mockPage();
    put.mockResolvedValue({ config_version: 5 });
    await openManage();
    await screen.findByRole("heading", { name: "Connection recovery" });

    const field = await screen.findByDisplayValue("1200");
    await userEvent.clear(field);
    await userEvent.type(field, "600");
    await userEvent.type(screen.getByPlaceholderText(/Reason/i), "alerting sooner during the upgrade");
    await userEvent.click(screen.getByRole("button", { name: /Save recovery settings/i }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/connection-settings");
    expect(body.link_down_alert_seconds).toBe(600);
    expect(body.reason).toBe("alerting sooner during the upgrade");
    // Send only changed fields to avoid overwriting another operator's edits.
    expect(body.backoff_min_ms).toBeUndefined();
    expect(body.blocked_after_refusals).toBeUndefined();
  });

  it("says when nobody has configured this connection, without pretending it is unset", async () => {
    mockPage({ recovery: { ...recovery, is_default: true, config_version: 0 } });
    await openManage();
    await screen.findByRole("heading", { name: "Connection recovery" });
    await waitFor(() =>
      expect(screen.getAllByText(/nobody has changed this one/i).length).toBe(5));
  });
});

describe("a provenance read failure is visible, and is not mistaken for absence", () => {
  it("says the record could not be read, and still shows the configuration", async () => {
    mockPage({
      provenanceStatus: "UNAVAILABLE",
      // provenance_status distinguishes read failure from an honestly empty history.
      revisions,
    });
    await openManage();

    // Keep configuration visible even when the provenance read fails.
    await screen.findByRole("heading", { name: "Live configuration" });
    const sheet = screen.getByRole("dialog");
    expect(await within(sheet).findByText("Version 1")).toBeTruthy();
    expect(within(sheet).getByText("In use")).toBeTruthy();

    expect(await screen.findByText(/could not be read just now/i)).toBeTruthy();
    expect(screen.getByText(/fault to report, not a sign that nothing was recorded/i)).toBeTruthy();
    expect(screen.queryByText(/was not recorded\./i)).toBeNull();
  });

  it("still says 'not recorded' when the read succeeded and there genuinely is nothing", async () => {
    mockPage({ provenanceStatus: "AVAILABLE", revisions });
    await openManage();

    expect(await screen.findByText(/How this version came to be saved was not recorded\./i)).toBeTruthy();
    expect(screen.queryByText(/could not be read just now/i)).toBeNull();
  });
});
