import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

// SIGN-IN METHODS: a method whose sender is gone can be switched OFF (audit C-13), and an identity provider whose
// switch is on but whose application is gone is still listed so it can be switched off (C-14).

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  const on = { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true };
  return { ...actual, useCapabilities: () => ({ surfaces: [], modules: { sms_otp: on, social_login: on } }) };
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
    // No SMS sender any more, and no identity provider application.
    if (path === "/notification-providers") return Promise.resolve(list([]));
    if (path === "/social-providers") return Promise.resolve(list([]));
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Sign-in methods: a method that cannot deliver can still be switched off", () => {
  it("lets an SMS method that is ON be switched off although its sender is gone", async () => {
    routes({ sms: { enabled: true } });
    put.mockResolvedValue({ sms: { enabled: false } });
    render(<SignInMethodsPage />);
    const sw = await screen.findByRole("switch", { name: "SMS code" });
    expect(sw).not.toBeDisabled();
    await userEvent.click(sw);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { sms: { enabled: false } }));
  });

  it("does not let it be switched ON while nothing can send a code", async () => {
    routes({ sms: { enabled: false } });
    render(<SignInMethodsPage />);
    expect(await screen.findByRole("switch", { name: "SMS code" })).toBeDisabled();
  });

  it("lists a switched-on identity provider with no application, so its switch can be cleared", async () => {
    routes({ social: { google: { enabled: true } } });
    put.mockResolvedValue({ social: { google: { enabled: false } } });
    render(<SignInMethodsPage />);
    const box = await screen.findByRole("checkbox", { name: /google/i });
    expect(box).toBeChecked();
    expect(screen.getByText(/not set up/i)).toBeInTheDocument();
    await userEvent.click(box);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { social: { google: { enabled: false } } }));
  });
});
