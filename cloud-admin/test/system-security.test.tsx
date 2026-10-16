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
    expect(calls.filter((c) => c.method === "PATCH")[1].body).toEqual({ operator_session_max_hours: 8, reason: "front desk PC" });
  });

  it("support staff see the value but cannot change it", async () => {
    mockFetch([team, { match: "/api/cloud/v1/settings/security", method: "GET", body: settings }]);
    renderAs(SUPPORT_ME, <TeamPage />);
    expect(await screen.findByLabelText("Sign-in lifetime in hours")).toBeDisabled();
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
