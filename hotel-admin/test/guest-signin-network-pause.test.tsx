import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import { NetworkPauseSettingsCard, NetworkPausesNotice } from "@/components/guest-signin-network-pause";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;

const pause = {
  id: "p1", guest_network: "Guest Wi-Fi", started_at: "2026-10-19T20:00:00Z", expires_at: "2026-10-19T20:15:00Z",
  remaining_seconds: 600, failure_count: 30, confirmed_devices: 0,
};
const devices = [
  { device_mac: "02:00:00:00:00:01", last_submitted_room_unverified: "103", last_attempt_at: "2026-10-19T20:01:00Z", attempts: 2, confirmed: false },
  { device_mac: "02:00:00:00:00:02", last_submitted_room_unverified: "999", last_attempt_at: "2026-10-19T20:02:00Z", attempts: 9, confirmed: false },
];

beforeEach(() => {
  vi.clearAllMocks();
  get.mockImplementation((path: string) => {
    if (path === "/guest-signin-restrictions/network-pauses") return Promise.resolve({ pauses: [pause] });
    if (path === "/guest-signin-restrictions/network-pauses/p1/devices") return Promise.resolve({ devices });
    if (path === "/guest-signin-protection/network") return Promise.resolve({
      network_max_failures: 30, network_window_seconds: 600, network_pause_seconds: 900, is_default: true,
      limits: { min_network_max_failures: 10, max_network_max_failures: 1000, min_network_window_seconds: 60,
        max_network_window_seconds: 3600, min_network_pause_seconds: 60, max_network_pause_seconds: 3600 },
    });
    return Promise.resolve({});
  });
  post.mockResolvedValue({});
});

describe("Network pause notice", () => {
  it("shows the pause and lets staff confirm the client's device with a reason", async () => {
    render(<NetworkPausesNotice canAct />);
    expect(await screen.findByText(/Room sign-in is paused on Guest Wi-Fi/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Let a client try again" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("radio", { name: /Room typed: 103/ }));
    await userEvent.type(within(dialog).getByLabelText(/^Reason/), "client at reception, room confirmed");
    await userEvent.click(within(dialog).getByRole("button", { name: "Let this device try again" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-signin-restrictions/network-pauses/p1/confirm",
      { device_mac: "02:00:00:00:00:01", reason: "client at reception, room confirmed" }));
  });

  it("ends the pause with a reason", async () => {
    render(<NetworkPausesNotice canAct />);
    await userEvent.click(await screen.findByRole("button", { name: "End the pause" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/^Reason/), "tour group mistyping");
    await userEvent.click(within(dialog).getByRole("button", { name: "End the pause" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-signin-restrictions/network-pauses/p1/end",
      { reason: "tour group mistyping" }));
  });

  it("offers no actions to a role that may only look", async () => {
    render(<NetworkPausesNotice canAct={false} />);
    expect(await screen.findByText(/Room sign-in is paused/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "End the pause" })).not.toBeInTheDocument();
  });
});

describe("Network pause settings", () => {
  it("shows minutes, refuses a value out of range, and saves seconds with a reason and the password", async () => {
    put.mockResolvedValue({
      network_max_failures: 50, network_window_seconds: 600, network_pause_seconds: 1200, is_default: false,
      limits: { min_network_max_failures: 10, max_network_max_failures: 1000, min_network_window_seconds: 60,
        max_network_window_seconds: 3600, min_network_pause_seconds: 60, max_network_pause_seconds: 3600 },
    });
    render(<NetworkPauseSettingsCard canWrite />);
    const max = await screen.findByLabelText(/Wrong room sign-ins/);
    expect(screen.getByLabelText(/Pause for/)).toHaveValue("15");
    await userEvent.clear(max);
    await userEvent.type(max, "5");
    expect(screen.getByRole("button", { name: "Save network pause settings" })).toBeDisabled();
    await userEvent.clear(max);
    await userEvent.type(max, "50");
    const pauseField = screen.getByLabelText(/Pause for/);
    await userEvent.clear(pauseField);
    await userEvent.type(pauseField, "20");
    await userEvent.click(screen.getByRole("button", { name: "Save network pause settings" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/^Reason/), "big conference");
    await userEvent.type(within(dialog).getByLabelText(/password/i), "hunter22");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save network pause settings" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/guest-signin-protection/network", {
      network_max_failures: 50, network_window_seconds: 600, network_pause_seconds: 1200,
      reason: "big conference", password: "hunter22" }));
  });
});
