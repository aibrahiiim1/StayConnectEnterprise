import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

// THE EMAIL CODE'S TEMPORARY INTERNET WINDOW on Sign-in methods: the Product Owner's defaults when nothing is
// stored, bounds refused before saving, and the save travelling with the email method (the server replaces the
// key whole, so the email switch must not be lost).

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

function routes(methods: Record<string, unknown>) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/auth-methods") return Promise.resolve(methods);
    if (path === "/notification-providers") return Promise.resolve(list([{ channel: "email", kind: "smtp", enabled: true }]));
    return Promise.resolve(list([]));
  });
}
beforeEach(() => { vi.clearAllMocks(); });

describe("Temporary internet while reading the code", () => {
  it("shows the Product Owner's defaults, refuses an out-of-bounds value, and saves with the email switch", async () => {
    routes({ email: { enabled: true } });
    put.mockResolvedValue({ email: { enabled: true } });
    render(<SignInMethodsPage />);
    const minutes = await screen.findByLabelText("How long") as HTMLInputElement;
    expect(minutes.value).toBe("10");
    expect((screen.getByLabelText("Download speed") as HTMLInputElement).value).toBe("2000");
    expect((screen.getByLabelText("Upload speed") as HTMLInputElement).value).toBe("1000");
    expect((screen.getByLabelText("Data allowance") as HTMLInputElement).value).toBe("25");
    const save = screen.getByRole("button", { name: /Save temporary internet/ }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(minutes, { target: { value: "45" } });
    expect(save.disabled).toBe(true);
    fireEvent.change(minutes, { target: { value: "15" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", {
      email: {
        enabled: true,
        temporary_access: { enabled: true, minutes: 15, down_kbps: 2000, up_kbps: 1000, data_mb: 25, per_device_daily: 3, per_destination_daily: 3, retry_after_minutes: 30, max_concurrent: 50 },
      },
    }));
  });
});
