import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

// CODE REQUESTS (contract §6.7): staff see recent code requests and the device's temporary internet; "Let them
// try again" and "Show code" each take a reason and the password; a role without code-reveal never sees the
// reveal button; the code is shown only after the reveal succeeds.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import { CodeRequestsCard, windowSummary, type CodeRequest, type CodeRequestsResp } from "@/components/code-requests";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const limits = { per_device_daily: 3, per_destination_daily: 3, retry_after_minutes: 30, max_concurrent: 50 };
const pending: CodeRequest = {
  challenge_id: "0b6d7c1e-2f3a-4b5c-8d9e-0f1a2b3c4d5e", channel: "email", destination: "guest@example.com",
  issued_at: "2026-10-12T08:00:00Z", expires_at: "2026-10-12T08:10:00Z", attempts: 0, max_attempts: 5, state: "PENDING",
  device_mac: "02:00:00:00:00:01", code_available: true, device_windows_24h: 1, retry_wait_seconds: 1500,
  window: { started_at: "2026-10-12T07:40:00Z", expires_at: "2026-10-12T07:50:00Z", ended_at: "2026-10-12T07:50:00Z", end_reason: "EXPIRED", used_bytes: 1048576, data_cap_bytes: 26214400, live: false },
};
const resp: CodeRequestsResp = { requests: [pending], limits, code_copy_available: true };

beforeEach(() => { vi.clearAllMocks(); g.mockResolvedValue(resp); });

describe("Code requests", () => {
  it("summarises the window, the wait and the limit", () => {
    expect(windowSummary(pending, limits)).toBe("Window ended (time ran out) · 1.05 MB used · next window in 25 min");
    expect(windowSummary({ ...pending, retry_wait_seconds: 0, device_windows_24h: 3 }, limits)).toContain("daily limit reached (3/3)");
  });

  it("lets the desk clear one device with a reason and the password", async () => {
    post.mockResolvedValue({ cleared_windows: 1 });
    render(<CodeRequestsCard roles={["front_office_operator"]} />);
    fireEvent.click(await screen.findByRole("button", { name: /let them try again/i }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "mail never came" } });
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw" } });
    const confirm = screen.getAllByRole("button", { name: /let them try again/i }).pop()!;
    fireEvent.click(confirm);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/code-requests/clear", { device_mac: "02:00:00:00:00:01", reason: "mail never came", password: "pw" }));
  });

  it("shows a live code only after the reveal succeeds", async () => {
    post.mockResolvedValue({ code: "482913", expires_at: "2026-10-12T08:10:00Z" });
    render(<CodeRequestsCard roles={["hotel_it_manager"]} />);
    fireEvent.click(await screen.findByRole("button", { name: /show code/i }));
    expect(screen.queryByText("482913")).toBeNull();
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "guest at the desk" } });
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw" } });
    fireEvent.click(screen.getAllByRole("button", { name: /show code/i }).pop()!);
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/code-reveal/${pending.challenge_id}`, { reason: "guest at the desk", password: "pw" }));
    expect(await screen.findByText("482913")).toBeTruthy();
  });

  it("never offers the reveal to a role without code-reveal, nor any action to a viewer", async () => {
    const { unmount } = render(<CodeRequestsCard roles={["guest_relations_operator"]} />);
    await screen.findByText("guest@example.com");
    expect(screen.queryByRole("button", { name: /show code/i })).toBeNull();
    expect(screen.getByRole("button", { name: /let them try again/i })).toBeTruthy();
    unmount();
    render(<CodeRequestsCard roles={["site_viewer"]} />);
    await screen.findByText("guest@example.com");
    expect(screen.queryByRole("button", { name: /show code/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /let them try again/i })).toBeNull();
  });
});
