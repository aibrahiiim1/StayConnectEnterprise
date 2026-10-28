import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import { CodeRequestsCard, type CodeRequest, type CodeRequestsResp } from "@/components/code-requests";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const pending: CodeRequest = {
  challenge_id: "0b6d7c1e-2f3a-4b5c-8d9e-0f1a2b3c4d5e", channel: "sms", destination: "+201001234567",
  issued_at: "2026-10-12T08:00:00Z", expires_at: "2026-10-12T08:10:00Z", attempts: 0, max_attempts: 5, state: "PENDING",
  device_mac: "02:00:00:00:00:01", code_available: true,
};
const resp: CodeRequestsResp = { requests: [pending], code_copy_available: true };

beforeEach(() => { vi.clearAllMocks(); g.mockResolvedValue(resp); });

describe("Code requests", () => {
  it("lists the request with its channel and offers no temporary-internet action", async () => {
    render(<CodeRequestsCard roles={["front_office_operator"]} />);
    await screen.findByText("+201001234567");
    expect(screen.getByText(/SMS · 02:00:00:00:00:01/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /let them try again/i })).toBeNull();
    expect(screen.queryByText(/temporary internet/i)).toBeNull();
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

  it("asks for a reason of at least four characters before revealing, like every other reveal", async () => {
    render(<CodeRequestsCard roles={["hotel_it_manager"]} />);
    fireEvent.click(await screen.findByRole("button", { name: /show code/i }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "ok" } });
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw" } });
    fireEvent.click(screen.getAllByRole("button", { name: /show code/i }).pop()!);
    expect(await screen.findByText(/Give a reason: 4 to 500 characters/)).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
  });

  it("never offers to show an existing client's code, and says why", async () => {
    g.mockResolvedValue({ ...resp, requests: [{ ...pending, code_available: false, existing_client: true }] });
    render(<CodeRequestsCard roles={["front_office_operator"]} />);
    await screen.findByText("+201001234567");
    expect(screen.queryByRole("button", { name: /show code/i })).toBeNull();
    expect(screen.getByText(/existing client/i)).toBeTruthy();
  });

  it("never offers the reveal to a role without code-reveal", async () => {
    const { unmount } = render(<CodeRequestsCard roles={["guest_relations_operator"]} />);
    await screen.findByText("+201001234567");
    expect(screen.queryByRole("button", { name: /show code/i })).toBeNull();
    unmount();
    render(<CodeRequestsCard roles={["site_viewer"]} />);
    await screen.findByText("+201001234567");
    expect(screen.queryByRole("button", { name: /show code/i })).toBeNull();
  });
});
