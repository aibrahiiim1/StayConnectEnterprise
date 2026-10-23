import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

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

function routes(methods: Record<string, unknown>, httpsActive?: boolean) {
  g.mockImplementation((path: string) => {
    if (path === "/portal-https" && httpsActive !== undefined) return Promise.resolve({ state: { https_active: httpsActive } });
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/auth-methods") return Promise.resolve(methods);
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

describe("Sign-in methods: plain-HTTP warning (GDPR audit A3)", () => {
  it("warns while the portal has no HTTPS and a typed sign-in method is on", async () => {
    routes({ sms: { enabled: true } }, false);
    render(<SignInMethodsPage />);
    expect(await screen.findByText("Sign-ins are not encrypted yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Portal HTTPS" })).toHaveAttribute("href", "/portal-https");
  });

  it("says nothing once HTTPS is active, or when no typed method is on", async () => {
    routes({ sms: { enabled: true } }, true);
    const { unmount } = render(<SignInMethodsPage />);
    await screen.findByRole("switch", { name: "SMS code" });
    expect(screen.queryByText("Sign-ins are not encrypted yet")).not.toBeInTheDocument();
    unmount();
    routes({ open: { enabled: true } }, false);
    render(<SignInMethodsPage />);
    await screen.findByRole("switch", { name: "SMS code" });
    expect(screen.queryByText("Sign-ins are not encrypted yet")).not.toBeInTheDocument();
  });
});
