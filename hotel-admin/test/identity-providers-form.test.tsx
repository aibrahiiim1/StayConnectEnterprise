import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("next/link", () => ({ default: ({ href, children, ...p }: any) => <a href={href} {...p}>{children}</a> }));
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import SocialProvidersPage from "@/app/(app)/social-providers/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;

const HOST = "wifi.nilehotel.com";
const KEY = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQg\n-----END PRIVATE KEY-----";

function routes(rows: unknown[] | Error, roles = ["site_admin"]) {
  g.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles });
    if (p === "/social-providers") {
      if (rows instanceof Error) return Promise.reject(rows);
      return Promise.resolve({
        data: rows, meta: { has_more: false }, https_active: true,
        callback_urls: [{ hostname: HOST, kind: "CUSTOM", url: `https://${HOST}/auth/social/callback`, canonical: true }],
      });
    }
    return Promise.resolve({});
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Identity providers form", () => {
  it("keeps the line breaks of an Apple .p8 key and checks the Team and Key IDs", async () => {
    routes([]);
    post.mockResolvedValueOnce({});
    render(<SocialProvidersPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /Add (a )?provider/ }))[0]);
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/^Provider/), { target: { value: "apple" } });
    fireEvent.change(within(dlg).getByLabelText(/^Services ID/), { target: { value: "com.hotel.signin" } });
    fireEvent.change(within(dlg).getByLabelText(/^Private key/), { target: { value: KEY } });
    fireEvent.change(within(dlg).getByLabelText(/^Team ID/), { target: { value: "abc" } });
    expect(within(dlg).getByText(/Team ID is 10 uppercase letters and digits/)).toBeInTheDocument();
    fireEvent.change(within(dlg).getByLabelText(/^Team ID/), { target: { value: "a1b2c3d4e5" } });
    fireEvent.change(within(dlg).getByLabelText(/^Key ID/), { target: { value: "K1EY2ID3AB" } });
    fireEvent.click(within(dlg).getByRole("checkbox"));
    expect(within(dlg).getByText(/Enter your password to save/)).toBeInTheDocument();
    fireEvent.change(within(dlg).getByLabelText(/Your password/), { target: { value: "op-pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [, body] = post.mock.calls[0];
    expect(body.client_secret).toBe(KEY);
    expect(body.team_id).toBe("A1B2C3D4E5");
    expect(body.key_id).toBe("K1EY2ID3AB");
  });

  it("says when the providers cannot be read, with Try again", async () => {
    routes(new Error("query failed"));
    render(<SocialProvidersPage />);
    expect(await screen.findByText("Identity providers could not be loaded")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Try again/ })).toBeInTheDocument();
  });

  it("gives a read-only role the full setup in Details", async () => {
    routes([{
      id: "p1", provider: "facebook", enabled: false, client_id: "app-1", callback_hosts: [HOST], callback_urls: [],
      secret_set: true, offered: false, offered_reason: "disabled", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z",
    }], ["front_office_operator"]);
    render(<SocialProvidersPage />);
    expect(await screen.findByText("Switched off")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Details\b/ }));
    expect(await screen.findByText(`https://${HOST}/auth/social/callback`)).toBeInTheDocument();
    expect(screen.getByText("Stored (never shown)")).toBeInTheDocument();
  });
});
