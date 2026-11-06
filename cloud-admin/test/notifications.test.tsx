import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, SUPPORT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import NotificationsPage from "@/app/(app)/system/notifications/page";

beforeEach(() => nav.reset("/system/notifications"));

const kinds = [
  { kind: "licence_expiring", label: "Licence ending soon", description: "A licence reaches each warning point.", default_delivery: "immediate" },
  { kind: "appliance_waiting", label: "Appliance waiting", description: "An appliance waits for activation.", default_delivery: "digest" },
];
const b = (d: number, min: number, max: number, unit: string) => ({ default: d, min, max, unit });
const settings = {
  enabled: false, smtp_host: "", smtp_port: 587, smtp_security: "starttls", smtp_username: "", from_address: "",
  from_name: "OneGate Central", console_url: "", smtp_password_set: false,
  licence_first_warning_days: 30, licence_second_warning_days: 7, licence_final_warning_days: 1,
  offline_after_minutes: 1440, digest_hour_utc: 7, log_retention_days: 90, max_attempts: 8,
  last_success_at: null, last_error: null, last_error_at: null, updated_at: "2026-11-01T08:00:00Z", updated_by: "owner@example.test",
  pending: 0, failed_last_24h: 0, kinds,
  bounds: {
    licence_first_warning_days: b(30, 2, 180, "days"), licence_second_warning_days: b(7, 1, 90, "days"),
    licence_final_warning_days: b(1, 0, 30, "days"), offline_after_minutes: b(1440, 15, 10080, "minutes"),
    digest_hour_utc: b(7, 0, 23, "hour (UTC)"), log_retention_days: b(90, 7, 730, "days"), max_attempts: b(8, 1, 20, "attempts"),
  },
};
const subs = { email: "ops@example.test", items: [], kinds };
const base = [
  { match: "/api/cloud/v1/notifications/settings", method: "GET", body: settings },
  { match: "/api/cloud/v1/notifications/subscriptions/me", method: "GET", body: subs },
  { match: /\/api\/cloud\/v1\/notifications\/log/, method: "GET", body: { items: [] } },
];

describe("System > Notifications", () => {
  it("says notifications are off and shows every event with Off selected", async () => {
    mockFetch(base);
    renderAs(PLATFORM_ME, <NotificationsPage />);
    expect(await screen.findByText("Notifications are off")).toBeInTheDocument();
    expect(await screen.findByLabelText("Licence ending soon: how you are told")).toHaveValue("");
    expect(screen.getByLabelText("Appliance waiting: how you are told")).toHaveValue("");
    expect(screen.getByText("No messages yet.")).toBeInTheDocument();
  });

  it("saves the recommended choices for the signed-in member", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      ...base,
      {
        match: "/api/cloud/v1/notifications/subscriptions/me", method: "PUT",
        body: { ...subs, items: [{ event_kind: "licence_expiring", delivery: "immediate" }, { event_kind: "appliance_waiting", delivery: "digest" }] },
      },
    ]);
    renderAs(PLATFORM_ME, <NotificationsPage />);
    await user.click(await screen.findByRole("button", { name: "Use recommended" }));
    expect(screen.getByLabelText("Appliance waiting: how you are told")).toHaveValue("digest");
    await user.click(screen.getByRole("button", { name: "Save my choices" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ items: [
      { event_kind: "licence_expiring", delivery: "immediate" },
      { event_kind: "appliance_waiting", delivery: "digest" },
    ] });
  });

  it("support staff choose their own events but cannot change the mail server", async () => {
    mockFetch(base);
    renderAs(SUPPORT_ME, <NotificationsPage />);
    expect(await screen.findByLabelText("Licence ending soon: how you are told")).toBeEnabled();
    expect(screen.queryByRole("button", { name: /Send test to me/ })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Send notifications")).toBeDisabled();
    expect(screen.getByText("A platform admin can switch them on.")).toBeInTheDocument();
  });

  it("refuses licence warnings out of order before sending anything", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch(base);
    renderAs(PLATFORM_ME, <NotificationsPage />);
    const second = await screen.findByLabelText(/Second licence warning/);
    await user.clear(second);
    await user.type(second, "40");
    const saves = screen.getAllByRole("button", { name: "Save" });
    await user.click(saves[saves.length - 1]);
    expect(await screen.findByText(/must come in order/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("shows the last sending error", async () => {
    mockFetch([
      { ...base[0], body: { ...settings, enabled: true, last_error: "the mail server refused the sign-in", last_error_at: "2026-11-05T10:00:00Z" } },
      base[1], base[2],
    ]);
    renderAs(PLATFORM_ME, <NotificationsPage />);
    expect(await screen.findByText("Central could not send its last email")).toBeInTheDocument();
    expect(screen.getByText("the mail server refused the sign-in")).toBeInTheDocument();
  });
});
