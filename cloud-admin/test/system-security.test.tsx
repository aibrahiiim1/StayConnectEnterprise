import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, SUPPORT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import TeamPage from "@/app/(app)/system/team/page";
import BackupHealthPage from "@/app/(app)/system/backup-health/page";

beforeEach(() => nav.reset("/system/team"));

const settings = {
  operator_session_max_hours: 24, default: 24, min: 1, max: 168, unit: "hours",
  operator_idle_minutes: 30, idle_bounds: { default: 30, min: 5, max: 240, unit: "minutes" },
  signin_lock_failures: 5, signin_lock_failures_bounds: { default: 5, min: 3, max: 20, unit: "wrong attempts" },
  signin_lock_minutes: 15, signin_lock_minutes_bounds: { default: 15, min: 1, max: 1440, unit: "minutes" },
  updated_at: "2026-10-01T08:00:00Z", updated_by: "owner@example.test",
};
const team = { match: "/api/cloud/v1/team", method: "GET", body: { items: [] } };

describe("System > Team: the sign-in lifetime setting", () => {
  it("shows the current value with its bounds, and saves a new one with a reason after step-up", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      team,
      { match: "/api/cloud/v1/settings/security", method: "GET", body: settings },
      {
        match: "/api/cloud/v1/settings/security", method: "PATCH",
        body: (n: number) => (n === 0 ? { status: 403, body: { error: "reauth_required" } }
          : { body: { ...settings, operator_session_max_hours: 8 } }),
      },
      { method: "POST", match: "/api/v1/auth/reauth", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const hours = await screen.findByLabelText("Sign-in lifetime in hours");
    expect(hours).toHaveValue("24");
    expect(screen.getByText(/From 1 to 168 hours\. Default 24\./)).toBeInTheDocument();
    expect(screen.getByText(/ends their sign-ins at once/)).toBeInTheDocument();

    await user.clear(hours);
    await user.type(hours, "200");
    await user.type(screen.getByPlaceholderText("e.g. shared workstation"), "front desk PC");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/from 1 to 168/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);

    await user.clear(hours);
    await user.type(hours, "8");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm your password" });
    await user.type(within(dialog).getByLabelText(/^Password/), "hunter22");
    await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].body).toEqual({ operator_session_max_hours: 8, operator_idle_minutes: 30, signin_lock_failures: 5, signin_lock_minutes: 15, reason: "front desk PC" });
  });

  it("the idle limit is saved with its bounds (GDPR plan C10)", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      team,
      { match: "/api/cloud/v1/settings/security", method: "GET", body: settings },
      { match: "/api/cloud/v1/settings/security", method: "PATCH", body: { ...settings, operator_idle_minutes: 15 } },
    ]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const idle = await screen.findByLabelText("Sign out after this many minutes without activity");
    expect(idle).toHaveValue("30");
    await user.clear(idle);
    await user.type(idle, "2");
    await user.type(screen.getByPlaceholderText("e.g. shared workstation"), "night shift");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/from 5 to 240/)).toBeInTheDocument();
    await user.clear(idle);
    await user.type(idle, "15");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ operator_session_max_hours: 24, operator_idle_minutes: 15, signin_lock_failures: 5, signin_lock_minutes: 15, reason: "night shift" });
  });

  it("the sign-in lockout is two settings with their bounds (Central migration 0053)", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      team,
      { match: "/api/cloud/v1/settings/security", method: "GET", body: settings },
      { match: "/api/cloud/v1/settings/security", method: "PATCH", body: { ...settings, signin_lock_failures: 3, signin_lock_minutes: 60 } },
    ]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const n = await screen.findByLabelText("Wrong passwords or codes before the account is locked");
    const mins = screen.getByLabelText("Minutes an account stays locked");
    expect(n).toHaveValue("5");
    expect(mins).toHaveValue("15");
    await user.clear(n);
    await user.type(n, "2");
    await user.type(screen.getByPlaceholderText("e.g. shared workstation"), "audit finding");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/wrong attempts from 3 to 20/)).toBeInTheDocument();
    await user.clear(n);
    await user.type(n, "3");
    await user.clear(mins);
    await user.type(mins, "60");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ operator_session_max_hours: 24, operator_idle_minutes: 30, signin_lock_failures: 3, signin_lock_minutes: 60, reason: "audit finding" });
  });

  it("support staff see the value but cannot change it", async () => {
    mockFetch([team, { match: "/api/cloud/v1/settings/security", method: "GET", body: settings }]);
    renderAs(SUPPORT_ME, <TeamPage />);
    expect(await screen.findByLabelText("Sign-in lifetime in hours")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });
});

const retention = {
  audit_log_months: 24, audit_log_months_bounds: { default: 24, min: 6, max: 120, unit: "months" },
  history_personal_months: 24, history_personal_months_bounds: { default: 24, min: 6, max: 120, unit: "months" },
  history_years: 6, history_years_bounds: { default: 6, min: 1, max: 10, unit: "years" },
  disabled_account_months: 24, disabled_account_months_bounds: { default: 24, min: 1, max: 120, unit: "months" },
  updated_at: "2026-10-19T08:00:00Z", updated_by: "owner@example.test",
};

describe("System > Team: data retention (Central migration 0054)", () => {
  it("shows the four periods with their bounds, refuses one out of range, and sends only what changed", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      team,
      { match: "/api/cloud/v1/settings/security", method: "GET", body: settings },
      { match: "/api/cloud/v1/settings/retention", method: "GET", body: retention },
      { match: "/api/cloud/v1/settings/retention", method: "PATCH", body: { ...retention, audit_log_months: 36 } },
    ]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const audit = await screen.findByLabelText("Months the audit log is kept");
    expect(audit).toHaveValue("24");
    expect(screen.getByLabelText("Years the history and licences are kept")).toHaveValue("6");
    expect(screen.getByLabelText("Months before a disabled account is anonymised")).toHaveValue("24");
    expect(screen.getByText(/From 1 to 10 years\. Default 6\./)).toBeInTheDocument();

    await user.clear(audit);
    await user.type(audit, "3");
    await user.type(screen.getByPlaceholderText("e.g. auditor's request"), "auditor asked");
    const saves = screen.getAllByRole("button", { name: "Save" });
    await user.click(saves[saves.length - 1]);
    expect(await screen.findByText(/Audit log \(months\): enter a whole number from 6 to 120/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);

    await user.clear(audit);
    await user.type(audit, "36");
    await user.click(saves[saves.length - 1]);
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ audit_log_months: 36, reason: "auditor asked" });
  });

  it("support staff see the periods but cannot change them", async () => {
    mockFetch([
      team,
      { match: "/api/cloud/v1/settings/security", method: "GET", body: settings },
      { match: "/api/cloud/v1/settings/retention", method: "GET", body: retention },
    ]);
    renderAs(SUPPORT_ME, <TeamPage />);
    expect(await screen.findByLabelText("Months the audit log is kept")).toBeDisabled();
    expect(screen.queryByPlaceholderText("e.g. auditor's request")).not.toBeInTheDocument();
  });
});

const registration = {
  max_waiting_appliances: 200, max_waiting_appliances_bounds: { default: 200, min: 10, max: 5000, unit: "appliances" },
  waiting_appliances: 7, updated_at: "2026-10-29T08:00:00Z", updated_by: "owner@example.test",
};

describe("System > Team: appliance registration limit (Central migration 0059)", () => {
  it("shows the waiting count next to the cap, refuses a value out of range, and saves with a reason", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      team,
      { match: "/api/cloud/v1/settings/registration", method: "GET", body: registration },
      { match: "/api/cloud/v1/settings/registration", method: "PATCH", body: { ...registration, max_waiting_appliances: 300 } },
    ]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const max = await screen.findByLabelText("Most appliances waiting for activation");
    expect(max).toHaveValue("200");
    expect(screen.getByTestId("waiting-count")).toHaveTextContent("7 of 200 appliances are waiting for activation now.");
    expect(screen.getByText(/From 10 to 5000 appliances\. Default 200\./)).toBeInTheDocument();
    expect(screen.getByText(/Last changed/)).toHaveTextContent("owner@example.test");

    await user.clear(max);
    await user.type(max, "5");
    await user.type(screen.getByPlaceholderText("e.g. opening week, many new appliances"), "opening week");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/appliances from 10 to 5000/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);

    await user.clear(max);
    await user.type(max, "300");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ max_waiting_appliances: 300, reason: "opening week" });
  });

  it("refuses a save without a reason", async () => {
    const user = userEvent.setup();
    mockFetch([team, { match: "/api/cloud/v1/settings/registration", method: "GET", body: registration }]);
    renderAs(PLATFORM_ME, <TeamPage />);
    const max = await screen.findByLabelText("Most appliances waiting for activation");
    await user.clear(max);
    await user.type(max, "250");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Enter a reason/)).toBeInTheDocument();
  });

  it("support staff see the limit but cannot change it", async () => {
    mockFetch([team, { match: "/api/cloud/v1/settings/registration", method: "GET", body: registration }]);
    renderAs(SUPPORT_ME, <TeamPage />);
    expect(await screen.findByLabelText("Most appliances waiting for activation")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });
});

describe("System > Backup health: the database backup", () => {
  it("a failed last run is shown as a problem with its error, next to the newest good dump", async () => {
    nav.reset("/system/backup-health");
    mockFetch([{
      match: "/api/cloud/v1/backup-health", method: "GET", body: {
        available: false, message: "backup cleanup has not run yet on this host",
        database_backup: {
          newest_file: "/opt/stayconnect/backups/db/central-20261015T020000Z-scheduled.dump",
          newest_at: new Date(Date.now() - 30 * 3600_000).toISOString(),
          last_attempt_at: new Date(Date.now() - 3600_000).toISOString(),
          last_success_at: new Date(Date.now() - 30 * 3600_000).toISOString(),
          last_failure_at: new Date(Date.now() - 3600_000).toISOString(),
          last_error: "pg_dump failed",
          healthy: false,
          problem: "The last database backup FAILED: pg_dump failed. The newest good backup is older than that run.",
        },
      },
    }]);
    renderAs(PLATFORM_ME, <BackupHealthPage />);
    expect(await screen.findByText("Database backup problem")).toBeInTheDocument();
    expect(screen.getByText(/The last database backup FAILED/)).toBeInTheDocument();
    expect(screen.getByText("Newest database backup")).toBeInTheDocument();
    expect(screen.getByText("central-20261015T020000Z-scheduled.dump")).toBeInTheDocument();
    expect(screen.getByText("Failed: pg_dump failed")).toBeInTheDocument();
  });

  it("a healthy backup shows no problem", async () => {
    nav.reset("/system/backup-health");
    mockFetch([{
      match: "/api/cloud/v1/backup-health", method: "GET", body: {
        available: false, message: "backup cleanup has not run yet on this host",
        database_backup: { newest_file: "/x/central-1.dump", newest_at: new Date().toISOString(),
          last_attempt_at: new Date().toISOString(), last_success_at: new Date().toISOString(), healthy: true, problem: null },
      },
    }]);
    renderAs(PLATFORM_ME, <BackupHealthPage />);
    expect(await screen.findByText("Newest database backup")).toBeInTheDocument();
    expect(screen.queryByText("Database backup problem")).not.toBeInTheDocument();
    expect(screen.getByText("Succeeded")).toBeInTheDocument();
  });
});
