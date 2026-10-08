import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

// THE CLIENT JOURNEY on Sign-in methods (contract §2.3, §6.2, §9): the primary method and the remembered-device
// window save together as the one `portal` key, nothing is sent until Save, only a method that is switched on
// can lead, and the days field is an operational setting with its default, unit and bounds stated.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  const on = { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true };
  // No hospitality: the Room sign-in card and the protection card are not part of this.
  return { ...actual, useCapabilities: () => ({ surfaces: [], modules: { email_otp: on, sms_otp: on } }) };
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

function routes(methods: Record<string, unknown>, roles = ["site_admin"]) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/auth-methods") return Promise.resolve(methods);
    if (path === "/notification-providers") return Promise.resolve(list([{ channel: "email", kind: "smtp", enabled: true }]));
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Client journey", () => {
  it("saves the primary method and the remembered-device window as the portal key, on Save only", async () => {
    routes({ voucher: { enabled: true }, email: { enabled: true }, sms: { enabled: false } });
    put.mockResolvedValue({ voucher: { enabled: true }, email: { enabled: true }, portal: { primary_method: "email", remember_device_days: 14 } });
    render(<SignInMethodsPage />);

    const primary = await screen.findByLabelText("Primary method") as HTMLSelectElement;
    // Automatic, then only the methods that are switched on: SMS is off and is not offered to lead.
    expect(Array.from(primary.options).map((o) => o.value)).toEqual(["", "email", "voucher"]);
    expect(primary.value).toBe("");
    const days = screen.getByLabelText("Remember devices") as HTMLInputElement;
    expect(days.value).toBe("30");
    expect(screen.getByText(/Default 30 days; allowed 0–365/)).toBeInTheDocument();
    expect(screen.getByText(/0 = always ask/)).toBeInTheDocument();

    const save = screen.getByRole("button", { name: /Save journey/ }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(primary, { target: { value: "email" } });
    fireEvent.change(days, { target: { value: "14" } });
    expect(put).not.toHaveBeenCalled();
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { portal: { primary_method: "email", remember_device_days: 14 } }));
    // One key, so the switches on this page are untouched by the save.
    expect(Object.keys(put.mock.calls[0][1])).toEqual(["portal"]);
  });

  it("reads the stored journey back, and refuses a window outside 0–365 days", async () => {
    routes({ voucher: { enabled: true }, email: { enabled: true }, portal: { primary_method: "voucher", remember_device_days: 7 } });
    render(<SignInMethodsPage />);
    const primary = await screen.findByLabelText("Primary method") as HTMLSelectElement;
    expect(primary.value).toBe("voucher");
    const days = screen.getByLabelText("Remember devices") as HTMLInputElement;
    expect(days.value).toBe("7");
    fireEvent.change(days, { target: { value: "400" } });
    expect(screen.getByText(/Must be between 0 and 365 days/)).toBeInTheDocument();
    expect((screen.getByRole("button", { name: /Save journey/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(days, { target: { value: "0" } });
    expect((screen.getByRole("button", { name: /Save journey/ }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("names a stored primary method that has since been switched off", async () => {
    routes({ voucher: { enabled: true }, email: { enabled: false }, portal: { primary_method: "email" } });
    render(<SignInMethodsPage />);
    const primary = await screen.findByLabelText("Primary method") as HTMLSelectElement;
    expect(primary.value).toBe("email");
    expect(primary.options[primary.selectedIndex].textContent).toBe("Email code (switched off)");
    expect(screen.getByText(/portal uses the automatic order/)).toBeInTheDocument();
  });

  it("a read-only role sees the journey and cannot save it", async () => {
    routes({ voucher: { enabled: true }, portal: { primary_method: "voucher", remember_device_days: 30 } }, ["front_office_operator"]);
    render(<SignInMethodsPage />);
    const primary = await screen.findByLabelText("Primary method") as HTMLSelectElement;
    expect(primary.disabled).toBe(true);
    expect((screen.getByLabelText("Remember devices") as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: /Save journey/ })).toBeNull();
  });
});
