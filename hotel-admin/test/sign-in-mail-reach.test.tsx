import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

// READING THE CODE: the email services a device may reach before sign-in, on Sign-in methods. Presets come
// from the appliance, the site adds host names, and the save carries the stored journey settings with it
// (the `portal` key is replaced whole by the server's merge).

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  const on = { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true };
  return { ...actual, useCapabilities: () => ({ surfaces: [], modules: { email_otp: on } }) };
});
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { api } from "@/lib/api";
import SignInMethodsPage from "@/app/(app)/sign-in-methods/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;
const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });
const presets = [
  { id: "gmail", name: "Gmail / Google Workspace", domains: ["mail.google.com", "accounts.google.com"] },
  { id: "outlook", name: "Outlook / Hotmail / Live / Microsoft 365", domains: ["outlook.live.com"] },
];

function routes(methods: Record<string, unknown>) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/auth-methods") return Promise.resolve(methods);
    if (path === "/auth-methods/mail-providers") return Promise.resolve(list(presets));
    if (path === "/notification-providers") return Promise.resolve(list([{ channel: "email", kind: "smtp", enabled: true }]));
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Email services reachable before sign-in", () => {
  it("offers the appliance's presets, takes host names, and saves them with the stored journey", async () => {
    routes({ email: { enabled: true }, portal: { primary_method: "email", remember_device_days: 7 } });
    put.mockResolvedValue({ email: { enabled: true }, portal: { primary_method: "email", remember_device_days: 7, mail_providers: ["gmail"], mail_domains: ["mail.example.com"] } });
    render(<SignInMethodsPage />);

    const gmail = await screen.findByLabelText("Gmail / Google Workspace") as HTMLInputElement;
    expect(gmail.checked).toBe(false);
    const save = screen.getByRole("button", { name: /Save email services/ }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.click(gmail);
    fireEvent.change(screen.getByLabelText("Other email services (host names)"), { target: { value: "Mail.Example.com\n*.bad" } });
    expect(screen.getByRole("alert").textContent).toMatch(/not a valid host name/);
    expect(save.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Other email services (host names)"), { target: { value: "Mail.Example.com" } });
    expect(save.disabled).toBe(false);
    expect(put).not.toHaveBeenCalled();
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", {
      portal: { primary_method: "email", remember_device_days: 7, mail_providers: ["gmail"], mail_domains: ["mail.example.com"] },
    }));
  });

  it("the journey save carries the chosen email services rather than dropping them", async () => {
    routes({ email: { enabled: true }, portal: { mail_providers: ["outlook"], mail_domains: ["mail.example.com"] } });
    put.mockResolvedValue({});
    render(<SignInMethodsPage />);
    const outlook = await screen.findByLabelText("Outlook / Hotmail / Live / Microsoft 365") as HTMLInputElement;
    expect(outlook.checked).toBe(true);
    const days = screen.getByLabelText("Remember devices") as HTMLInputElement;
    fireEvent.change(days, { target: { value: "14" } });
    fireEvent.click(screen.getByRole("button", { name: /Save journey/ }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", {
      portal: { mail_providers: ["outlook"], mail_domains: ["mail.example.com"], primary_method: "", remember_device_days: 14 },
    }));
  });
});
