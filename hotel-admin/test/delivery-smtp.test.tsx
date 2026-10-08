import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// DELIVERY: the site's own mail server, and operating a sender like a product (contract §7).
//
// Pinned here: an SMTP sender posts kind "smtp" with the relay settings in `extra` and the password as the
// write-only `api_key`; the port may be left blank for the server to fill from the security mode; the test
// action calls the test route with the typed address and shows the outcome in the service's own words; and the
// Health column reads the last delivery and the last error off the row.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
const patch = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    patch: (...a: any[]) => patch(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {
    status: number; code: string;
    constructor(status: number, body: any) { super(body?.message ?? "err"); this.status = status; this.code = body?.error ?? "http_error"; }
  },
}));

// A site licensed for every code channel, so every service is offered.
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  const on = { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true };
  return { ...actual, useCapabilities: () => ({ surfaces: [], modules: { email_otp: on, sms_otp: on, whatsapp_otp: on } }) };
});

import NotificationsPage from "@/app/(app)/notifications/page";

const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });

const SMTP = {
  id: "p1", tenant_id: "t", channel: "email", kind: "smtp", enabled: true, display_name: "Site relay",
  extra: { host: "smtp.example.com", port: "587", security: "starttls", username: "codes@example.com", timeout_seconds: "10" },
  from_address: "codes@example.com", from_name: "Wi-Fi Access",
  last_success_at: new Date(Date.now() - 2 * 60_000).toISOString(),
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z",
};
const SENDGRID = {
  id: "p2", tenant_id: "t", channel: "email", kind: "sendgrid", enabled: false, display_name: "SendGrid",
  from_address: "noreply@example.com",
  last_error: "401 Unauthorized: invalid API key", last_error_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z",
};

function routes(providers: unknown[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/notification-providers") return Promise.resolve(list(providers));
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); patch.mockReset(); del.mockReset(); });

describe("the page", () => {
  it("is called Delivery and offers SMTP beside SendGrid, never SES, for a new email sender", async () => {
    routes([]);
    render(<NotificationsPage />);
    expect(await screen.findByRole("heading", { name: "Delivery" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Add sender/ }));
    const dialog = await screen.findByRole("dialog");
    const service = within(dialog).getByLabelText("Service") as HTMLSelectElement;
    expect(Array.from(service.options).map((o) => o.value)).toEqual(["stub", "sendgrid", "smtp"]);
    expect(Array.from(service.options).map((o) => o.textContent)).toContain("Your own mail server (SMTP)");
  });

  it("shows each sender's last delivery and last error", async () => {
    routes([SMTP, SENDGRID]);
    render(<NotificationsPage />);
    expect(await screen.findByText(/Last delivered 2m ago/)).toBeInTheDocument();
    expect(screen.getByText(/Last error: 401 Unauthorized: invalid API key at 3h ago/)).toBeInTheDocument();
    expect(screen.getByText("Sending")).toBeInTheDocument();
    expect(screen.getByText("Failing")).toBeInTheDocument();
    // The relay is named in the Service column.
    expect(screen.getByText("smtp.example.com:587")).toBeInTheDocument();
  });
});

describe("an SMTP sender", () => {
  it("posts kind smtp with the relay in extra and the password as the write-only api_key", async () => {
    routes([]);
    post.mockResolvedValue({ ...SMTP });
    render(<NotificationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add sender/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Service"), { target: { value: "smtp" } });
    // The SMTP fields appear; the SendGrid-style "API user" does not.
    expect(within(dialog).queryByLabelText(/API user/)).toBeNull();
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "Site relay" } });
    fireEvent.change(within(dialog).getByLabelText(/Mail server/), { target: { value: " smtp.example.com " } });
    fireEvent.change(within(dialog).getByLabelText(/^Security/), { target: { value: "tls" } });
    // The port is left blank: the server fills 465 from the security mode, and the hint says so.
    expect(within(dialog).getByText(/Leave blank for the usual port for the security chosen \(465\)/)).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText(/^Username/), { target: { value: "codes@example.com" } });
    const pw = within(dialog).getByLabelText(/^Password/) as HTMLInputElement;
    expect(pw.type).toBe("password");
    fireEvent.change(pw, { target: { value: "s3cret" } });
    fireEvent.change(within(dialog).getByLabelText(/From address/), { target: { value: "codes@example.com" } });
    fireEvent.change(within(dialog).getByLabelText(/From name/), { target: { value: "Wi-Fi Access" } });
    fireEvent.change(within(dialog).getByLabelText(/^Timeout/), { target: { value: "20" } });

    fireEvent.click(within(dialog).getByRole("button", { name: /^Add sender$/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][0]).toBe("/notification-providers");
    expect(post.mock.calls[0][1]).toEqual({
      channel: "email", kind: "smtp", display_name: "Site relay",
      api_key: "s3cret", api_user: undefined,
      from_address: "codes@example.com", from_name: "Wi-Fi Access",
      extra: { host: "smtp.example.com", port: "", security: "tls", username: "codes@example.com", timeout_seconds: "20" },
      enabled: true,
    });
  });

  it("on Edit, a blank password keeps the stored one and the relay settings are sent back", async () => {
    routes([SMTP]);
    patch.mockResolvedValue({ ...SMTP });
    render(<NotificationsPage />);
    await screen.findByText("Site relay");
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog");
    expect((within(dialog).getByLabelText(/Mail server/) as HTMLInputElement).value).toBe("smtp.example.com");
    expect((within(dialog).getByLabelText(/^Port/) as HTMLInputElement).value).toBe("587");
    fireEvent.change(within(dialog).getByLabelText(/^Port/), { target: { value: "2525" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
    expect(patch.mock.calls[0][0]).toBe("/notification-providers/p1");
    expect(patch.mock.calls[0][1]).not.toHaveProperty("api_key");
    expect(patch.mock.calls[0][1].extra).toEqual({ host: "smtp.example.com", port: "2525", security: "starttls", username: "codes@example.com", timeout_seconds: "10" });
  });

  it("shows the server's validation message in the dialog", async () => {
    routes([]);
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new (ApiError as any)(400, { error: "bad_request", message: "a username requires a password" }));
    render(<NotificationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add sender/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Service"), { target: { value: "smtp" } });
    fireEvent.change(within(dialog).getByLabelText(/Mail server/), { target: { value: "smtp.example.com" } });
    fireEvent.change(within(dialog).getByLabelText(/^Username/), { target: { value: "codes" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^Add sender$/ }));
    expect(await within(dialog).findByText("a username requires a password")).toBeInTheDocument();
  });
});

describe("Send a test message", () => {
  it("calls the test route with the typed address and shows the outcome, including the service's own error", async () => {
    routes([SMTP]);
    post.mockResolvedValueOnce({ ok: true, kind: "smtp" });
    render(<NotificationsPage />);
    await screen.findByText("Site relay");
    fireEvent.click(screen.getByRole("button", { name: /Send a test message/ }));
    const dialog = await screen.findByRole("dialog");
    const to = within(dialog).getByLabelText(/Send to \(email address\)/) as HTMLInputElement;
    expect(to.type).toBe("email");
    // Nothing to send to, nothing to click.
    expect((within(dialog).getByRole("button", { name: /Send test/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(to, { target: { value: "me@example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Send test/ }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/notification-providers/p1/test", { to: "me@example.com" }));
    expect(await within(dialog).findByText("Delivered")).toBeInTheDocument();
    expect(within(dialog).getByText(/sent through Your own mail server \(SMTP\)/)).toBeInTheDocument();
    // The row is re-read so the Health column reflects the recorded outcome.
    expect(get.mock.calls.filter((c) => c[0] === "/notification-providers").length).toBeGreaterThanOrEqual(2);

    post.mockResolvedValueOnce({ ok: false, error: "smtp_auth", message: "535 5.7.8 Authentication credentials invalid" });
    fireEvent.click(within(dialog).getByRole("button", { name: /Send again/ }));
    expect(await within(dialog).findByText("Not delivered")).toBeInTheDocument();
    expect(within(dialog).getByText("535 5.7.8 Authentication credentials invalid")).toBeInTheDocument();
    expect(within(dialog).getByText("smtp_auth")).toBeInTheDocument();
  });

  it("reports an appliance that could not run the test at all", async () => {
    routes([SMTP]);
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new (ApiError as any)(502, { error: "scd_unavailable", message: "the session controller is not reachable" }));
    render(<NotificationsPage />);
    await screen.findByText("Site relay");
    fireEvent.click(screen.getByRole("button", { name: /Send a test message/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/Send to/), { target: { value: "me@example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Send test/ }));
    expect(await within(dialog).findByText("the session controller is not reachable")).toBeInTheDocument();
  });
});
