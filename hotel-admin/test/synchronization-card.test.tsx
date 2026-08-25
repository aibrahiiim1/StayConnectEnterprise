// THE SYNCHRONIZATION SECTION MUST NOT INVENT A NUMBER.
//
// Everything else in this file is ordinary UI coverage. This is the part that matters: Protel's FIAS provides
// no record total before the end of a sync, so a percentage or an "X of Y" on this screen would be fabricated
// by the client. The assertions below fail if one ever appears, including by way of a well-meaning helper
// someone adds later.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SynchronizationCard } from "@/app/(app)/pms-interfaces/synchronization-card";
import type { PmsInterfaceHealth } from "@/lib/api";

const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api, post: (...a: unknown[]) => post(...a) } };
});

const health = (over: Partial<PmsInterfaceHealth> = {}): PmsInterfaceHealth =>
  ({
    pms_interface_id: "i1",
    transport_status: "CONNECTED",
    continuity_status: "CONTINUOUS",
    sync_status: "IN_SYNC",
    in_house_stays: 461,
    pending_events: 0,
    review_events: 0,
    ...over,
  }) as PmsInterfaceHealth;

beforeEach(() => {
  post.mockReset();
  post.mockResolvedValue({ note: "The request is recorded." });
});
afterEach(() => vi.useRealTimers());

describe("synchronization", () => {
  it("shows the real received count and never a percentage or a total", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "RECEIVING", sync_records_received: 1847, sync_status: "RESYNC_IN_PROGRESS" })}
        onRefreshed={() => {}}
      />,
    );
    // The count appears twice on purpose: once in the field list, once inside the honest sentence.
    expect(screen.getAllByText(/1,847/).length).toBeGreaterThan(0);
    expect(screen.getByText(/does not provide a total/i)).toBeInTheDocument();

    // No percentage, no "of N", no progress bar. FIAS cannot support any of them before the end of a sync.
    const body = document.body.textContent ?? "";
    expect(body).not.toMatch(/\d+\s*%/);
    expect(body).not.toMatch(/\bof\s+\d/i);
    expect(document.querySelector("progress")).toBeNull();
    expect(document.querySelector('[role="progressbar"]')).toBeNull();
  });

  it("explains that the first connection syncs automatically and this asks for another", () => {
    render(<SynchronizationCard id="i1" health={health()} onRefreshed={() => {}} />);
    expect(screen.getByText(/first successful connection/i)).toBeInTheDocument();
    expect(screen.getByText(/another complete, fresh copy/i)).toBeInTheDocument();
  });

  it("refuses to offer the action while the PMS is disconnected", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ transport_status: "DISCONNECTED" })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /full resync now/i })).toBeDisabled();
    expect(screen.getByText(/available when the PMS is connected/i)).toBeInTheDocument();
  });

  it("refuses while a sync is already running, so a second click cannot start one", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "RECEIVING", sync_status: "RESYNC_IN_PROGRESS" })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /full resync now/i })).toBeDisabled();
  });

  it("sends the reason and the password, and never an actor or an interface secret", async () => {
    const user = userEvent.setup();
    render(<SynchronizationCard id="i1" health={health()} onRefreshed={() => {}} />);

    await user.type(screen.getByLabelText(/password to confirm this resync/i), "hunter2");
    await user.click(screen.getByRole("button", { name: /full resync now/i }));

    await waitFor(() => expect(post).toHaveBeenCalledOnce());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/full-resync");
    expect(body).toEqual({ reason_code: "SUSPECTED_STALE_GUEST_LIST", password: "hunter2" });
    expect(Object.keys(body as object)).not.toContain("actor");
  });

  it("polls while a sync is active and stops once it completes", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();
    const { rerender } = render(
      <SynchronizationCard id="i1" health={health({ sync_stage: "RECEIVING" })} onRefreshed={onRefreshed} />,
    );
    await vi.advanceTimersByTimeAsync(7000);
    expect(onRefreshed.mock.calls.length).toBeGreaterThan(0);

    const during = onRefreshed.mock.calls.length;
    rerender(
      <SynchronizationCard id="i1" health={health({ sync_stage: "COMPLETE" })} onRefreshed={onRefreshed} />,
    );
    await vi.advanceTimersByTimeAsync(10000);
    expect(onRefreshed.mock.calls.length).toBe(during);
  });

  it("says plainly that an interrupted sync left the previous guest list in place", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "INTERRUPTED", sync_failure_code: "TRANSPORT_LOST" })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByText(/previous guest list is still in use/i)).toBeInTheDocument();
    expect(screen.getByText(/nothing was lost or partly replaced/i)).toBeInTheDocument();
  });
});
