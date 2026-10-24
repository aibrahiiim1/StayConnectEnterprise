import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fakeAudit } from "./fake-paged";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      put: (...a: any[]) => put(...a),
      del: (...a: any[]) => del(...a),
      postRaw: vi.fn(),
    },
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
}));

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset(); });

// A row's secondary actions live in its "⋯" menu (RowActions); Radix opens it from the keyboard.
async function openRowMenu(subject: string) {
  fireEvent.keyDown(await screen.findByRole("button", { name: `Actions for ${subject}` }), { key: "Enter" });
  return screen.findByRole("menu");
}
afterEach(() => { cleanup(); vi.resetModules(); });

function routes(map: Record<string, unknown>, roles: string[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles, operator_id: "me" });
    for (const [prefix, body] of Object.entries(map)) {
      if (path === prefix || path.startsWith(prefix + "?")) {
        return Promise.resolve(typeof body === "function" ? body(path) : body);
      }
    }
    return Promise.resolve({});
  });
}

const SERVICE = {
  service: "scd", state: "healthy", process_state: "running", health_ok: true, health_detail: "ok",
  consecutive_failures: 0, restart_count: 0, restarts_in_window: 0, restart_window_secs: 300,
  backoff_level: 0, backoff_ms: 0, next_retry_at: null, first_failure_at: null, last_failure_at: null,
  last_failure_reason: "", last_exit_code: null, last_exit_signal: "", last_healthy_at: new Date().toISOString(),
  last_recovery_at: null, time_since_healthy_s: 0, degraded_dependency: "", critical: true,
  updated_at: new Date().toISOString(),
};
const SUMMARY = {
  overall: "healthy", counts: { healthy: 1 }, services: [SERVICE],
  boot: { converged: false, alert_open: false, pending: ["kea"] }, generated_at: new Date().toISOString(),
};

describe("Diagnostics", () => {
  it("is titled by its menu label and says when the appliance is still starting", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_admin"]);
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { level: 1, name: "Diagnostics" })).toBeTruthy();
    expect(await screen.findByText(/still starting after boot/i)).toBeTruthy();
  });

  it("restart asks for the impact, a reason and a password, and sends exactly those", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    await openRowMenu("scd");
    fireEvent.click(await screen.findByRole("menuitem", { name: "Restart…" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Every client currently online is disconnected/)).toBeTruthy();
    const confirm = within(dialog).getByRole("button", { name: "Restart now" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);

    await userEvent.type(within(dialog).getByLabelText(/Why are you restarting it/), "stuck");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw-123");
    await userEvent.click(confirm);

    await waitFor(() => expect(post).toHaveBeenCalledWith("/diagnostics/services/scd/restart", { reason: "stuck", password: "pw-123" }));
  });

  it("offers a read-only role neither the logs nor Recheck or Restart, which the server refuses (N-22)", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_viewer"]);
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    await screen.findByText(/can view diagnostics, but not read service logs/i);
    expect(screen.queryByRole("button", { name: "Actions for scd" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Recheck/ })).toBeNull();
  });

  it("offers the IT manager the logs", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["hotel_it_manager"]);
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    const menu = await openRowMenu("scd");
    expect(within(menu).getByRole("menuitem", { name: "Logs" })).toBeTruthy();
  });
});

describe("Alerts", () => {
  it("is titled Alerts and says why an empty queue is good news", async () => {
    get.mockResolvedValue({ data: [], meta: { has_more: false } });
    const { OperationalAlertsView } = await import("@/components/phase3/operational-alerts-view");
    render(<OperationalAlertsView />);
    expect(screen.getByRole("heading", { level: 1, name: "Alerts" })).toBeTruthy();
    expect(await screen.findByText("No open alerts")).toBeTruthy();
    expect(screen.getByText("Every checkout was handled with the configured policy.")).toBeTruthy();
  });
});

describe("Activity", () => {
  const ROWS = fakeAudit([
    { ts: "2026-09-20T10:00:00Z", actor_type: "operator", actor_id: "op-1", action: "backup.downloaded", payload: { file: "db-1.sql.gz" } },
    { ts: "2026-09-20T09:00:00Z", actor_type: "system", action: "health.recheck" },
  ]);

  it("filters to security events with a count, and keeps the raw payload behind a disclosure", async () => {
    routes({ "/audit": ROWS, "/operators": { data: [{ id: "op-1", display_name: "Mona" }] } }, ["site_admin"]);
    const Page = (await import("@/app/(app)/audit/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { level: 1, name: "Activity" })).toBeTruthy();
    await screen.findByText("2 entries");

    const chips = screen.getByRole("radiogroup", { name: "Show" });
    const security = within(chips).getByRole("radio", { name: /Security/ });
    await userEvent.click(security);
    expect(security.getAttribute("aria-checked")).toBe("true");
    expect(await screen.findByText("1 entry")).toBeTruthy();

    // The row opens the exact record in a side drawer.
    const table = screen.getByRole("table");
    await userEvent.click(within(table).getAllByRole("row")[1]);
    const drawer = await screen.findByRole("dialog");
    expect(within(drawer).getByText("backup.downloaded")).toBeTruthy();
    const disclosure = within(drawer).getByText("Raw payload");
    expect(disclosure.closest("details")).toBeTruthy();
  });
});

describe("Backups", () => {
  const ART = {
    data: [
      { name: "db-20260921T095526Z.sql.gz", size_bytes: 1024, modified_at: "2026-09-21T09:55:26Z", verified_at: "2026-09-21T10:00:00Z", verified_tables: 90 },
      { name: "db-20260920T095526Z.sql.gz", size_bytes: 1024, modified_at: "2026-09-20T09:55:26Z", verified_at: null },
    ],
  };
  const base = {
    "/backups/artifacts": ART,
    "/backups/maintenance": { active: false },
    "/backups/last-restore": { present: false },
    "/backups/health": { retention_readable: false, timer_active: true, timer_readable: true, database_backups: 2 },
    "/backups/settings": { retention: [], schedule_known: false, config_present: false },
  };

  it("offers Restore only for a verified backup", async () => {
    routes(base, ["site_admin"]);
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    expect(await screen.findByRole("button", { name: "Restore db-20260921T095526Z.sql.gz" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Restore db-20260920T095526Z.sql.gz" })).toBeNull();
    expect(screen.getByText("This site can be recovered from its latest backup")).toBeTruthy();
  });

  it("restore requires the backup's name typed out and a password, and sends both", async () => {
    routes(base, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    const name = "db-20260921T095526Z.sql.gz";
    await userEvent.click(await screen.findByRole("button", { name: `Restore ${name}` }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/current data will be replaced/)).toBeTruthy();
    expect(within(dialog).getByText(/Take a fresh safety copy/)).toBeTruthy();

    const go = within(dialog).getByRole("button", { name: "Restore this backup" }) as HTMLButtonElement;
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    expect(go.disabled, "a password alone must not be enough").toBe(true);
    await userEvent.type(within(dialog).getByLabelText(/to confirm/), name);
    expect(go.disabled).toBe(false);
    await userEvent.click(go);

    await waitFor(() => expect(post).toHaveBeenCalledWith(
      `/backups/artifacts/${encodeURIComponent(name)}/restore`, { password: "pw", confirm: name }));
    expect(await within(dialog).findByText(/this window stays open until it finishes/i)).toBeTruthy();
  });

  it("Back up now collects the password in a dialog and sends it", async () => {
    routes(base, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByText("Available backups");
    await userEvent.click(await screen.findByRole("button", { name: /Back up now/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Back up now" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/run", { password: "pw" }));
  });

  it("shows a read-only role no write action and no download (decision D-B)", async () => {
    routes(base, ["site_viewer"]);
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByText(/can see the backups, but not download/);
    await screen.findByText("db-20260921T095526Z.sql.gz");
    expect(screen.queryByRole("button", { name: /Back up now/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Restore / })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Download / })).toBeNull();
    expect(screen.queryByRole("link", { name: /^Download / })).toBeNull();
  });

  it("the IT manager downloads with a password: a ticket first, then the file by form post", async () => {
    routes(base, ["hotel_it_manager"]);
    post.mockResolvedValue({ ticket: "tkt-1" });
    const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(function (this: HTMLFormElement) {
      expect(this.method.toLowerCase()).toBe("post");
      expect(this.action).toContain("/api/edge/v1/backup-download/db-20260921T095526Z.sql.gz");
      expect((this.elements.namedItem("ticket") as HTMLInputElement).value).toBe("tkt-1");
    });
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByText(/can see and download backups/);
    await userEvent.click(await screen.findByRole("button", { name: "Download db-20260921T095526Z.sql.gz" }));
    const dialog = await screen.findByRole("dialog");
    const go = within(dialog).getByRole("button", { name: "Download" }) as HTMLButtonElement;
    expect(go.disabled, "the password is required").toBe(true);
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith(
      "/backup-download/db-20260921T095526Z.sql.gz/ticket", { password: "pw" }));
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    submit.mockRestore();
  });
});

describe("Operators", () => {
  const OPS = {
    data: [
      { id: "me", email: "me@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["site_admin"] },
      { id: "o2", email: "desk@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["front_office_operator"] },
    ],
  };

  it("marks the signed-in operator and does not promise a re-enable the API does not offer", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    expect(await screen.findByText("You")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Disable desk@hotel" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText(/can be re-enabled/)).toBeNull();
    expect(within(dialog).getByText(/no way to re-enable/i)).toBeTruthy();
  });

  it("adding a role is confirmed before the request is sent, and sends the same body", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    const select = await screen.findByLabelText("Give desk@hotel another role");
    await userEvent.selectOptions(select, "site_viewer");
    expect(post).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Give this role\?/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Add role" })).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText(/Your password/), "admin-pass-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add role" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/operators/o2/roles",
      { role: "site_viewer", your_password: "admin-pass-1" }));
  });

  it("new operators default to Site viewer", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: /Add operator/ }));
    const dialog = await screen.findByRole("dialog");
    expect((within(dialog).getByLabelText("Role") as HTMLSelectElement).value).toBe("site_viewer");
  });
});

describe("Appliance & licence", () => {
  const STATUS = {
    activation: "activated", serial: "VN-0001", appliance_id: "ap-1", customer_name: "Acme Hotels", site_name: "Nile",
    license: { state: "expired", valid_until: "2026-09-01T00:00:00Z", grace_ends_at: "2026-09-15T00:00:00Z", days_left: 0,
      max_concurrent_online_guests: 100, current_online_guests: 85 },
    central: { state: "connected", last_contact_at: new Date().toISOString(), last_error: null },
    details: { identity_key_fingerprint: "ab", cert_fingerprint: "cd", cert_not_after: null, assignment_version: 3,
      license_version: 2, wan_mac: "aa:bb:cc:dd:ee:ff", lan_mac: "", central_endpoint: "https://central.example" },
  };

  it("is titled by its menu label, says existing guest sessions survive and shows the capacity in words", async () => {
    routes({ "/central/status": STATUS }, ["site_admin"]);
    const Page = (await import("@/app/(app)/appliance/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { level: 1, name: "Appliance & licence" })).toBeTruthy();
    expect(await screen.findByText(/clients already online are not disconnected/)).toBeTruthy();
    expect(screen.getByText(/85% of the licensed capacity · nearly full/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Upload licence file/ })).toBeTruthy();
    const text = document.body.textContent ?? "";
    for (const retired of ["Subscription", "Trial", "Plan limits", "Fleet telemetry"]) {
      expect(text, `"${retired}" is a retired word`).not.toContain(retired);
    }
  });
});
