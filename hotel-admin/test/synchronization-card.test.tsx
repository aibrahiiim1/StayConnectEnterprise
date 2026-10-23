// FIAS provides no record total before sync ends, so the UI cannot show a truthful percentage.

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
    expect(screen.getAllByText(/1,847/).length).toBeGreaterThan(0);
    expect(screen.getByText(/does not say how many records it will send/i)).toBeInTheDocument();

    const body = document.body.textContent ?? "";
    expect(body).not.toMatch(/\d+\s*%/);
    expect(body).not.toMatch(/\bof\s+\d/i);
    expect(document.querySelector("progress")).toBeNull();
    expect(document.querySelector('[role="progressbar"]')).toBeNull();
  });

  it("explains that the first connection syncs automatically and this asks for another", async () => {
    render(<SynchronizationCard id="i1" health={health()} onRefreshed={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Tips: Guest list refresh" }));
    expect(screen.getByText(/first successful connection/i)).toBeInTheDocument();
    expect(screen.getByText(/looks out of date/i)).toBeInTheDocument();
  });

  it("refuses to offer the action while the PMS is disconnected", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ transport_status: "DISCONNECTED" })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /refresh the guest list now/i })).toBeDisabled();
    expect(screen.getByText(/available once the PMS is connected/i)).toBeInTheDocument();
  });

  it("refuses while a sync is already running, so a second click cannot start one", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "RECEIVING", sync_status: "RESYNC_IN_PROGRESS" })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /refresh the guest list now/i })).toBeDisabled();
  });

  it("sends the reason and the password, and never an actor or an interface secret", async () => {
    const user = userEvent.setup();
    render(<SynchronizationCard id="i1" health={health()} onRefreshed={() => {}} />);

    await user.click(screen.getByRole("button", { name: /refresh the guest list now/i }));
    await user.type(await screen.findByLabelText(/confirm your password/i), "hunter2");
    await user.click(screen.getByRole("button", { name: /^request refresh$/i }));

    await waitFor(() => expect(post).toHaveBeenCalledOnce());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/full-resync");
    expect(body).toEqual({ reason_code: "SUSPECTED_STALE_GUEST_LIST", password: "hunter2" });
    expect(Object.keys(body as object)).not.toContain("actor");
  });

  it("polls fast while a sync is active and drops to the slow rate once it completes", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();
    const { rerender } = render(
      <SynchronizationCard id="i1" health={health({ sync_stage: "RECEIVING" })} onRefreshed={onRefreshed} />,
    );
    await vi.advanceTimersByTimeAsync(7000);
    const fast = onRefreshed.mock.calls.length;
    expect(fast).toBeGreaterThanOrEqual(2); // 3s cadence

    // COMPLETE slows polling but does not stop watching the open page.
    rerender(
      <SynchronizationCard id="i1" health={health({ sync_stage: "COMPLETE" })} onRefreshed={onRefreshed} />,
    );
    onRefreshed.mockClear();
    await vi.advanceTimersByTimeAsync(7000);
    expect(onRefreshed.mock.calls.length).toBeLessThan(fast);
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

describe("observing an external reconnect", () => {
  it("polls while DISCONNECTED, so the reconnect can be discovered at all", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();
    render(
      <SynchronizationCard
        id="i1"
        health={health({ transport_status: "DISCONNECTED", sync_status: "RESYNC_REQUIRED" })}
        onRefreshed={onRefreshed}
      />,
    );

    // Poll even while idle so a later reconnect is observed.
    await vi.advanceTimersByTimeAsync(11000);
    expect(onRefreshed.mock.calls.length).toBeGreaterThan(0);
  });

  it("follows the whole automatic sequence to COMPLETE and then stops polling", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();

    const steps: Partial<PmsInterfaceHealth>[] = [
      { transport_status: "DISCONNECTED", sync_status: "RESYNC_REQUIRED" },
      { transport_status: "CONNECTED", sync_status: "RESYNC_REQUIRED", sync_stage: "REQUESTING_FULL_SYNC" },
      { transport_status: "CONNECTED", sync_status: "RESYNC_REQUIRED", sync_stage: "WAITING_FOR_PMS" },
      { transport_status: "CONNECTED", sync_status: "RESYNC_IN_PROGRESS", sync_stage: "RECEIVING", sync_records_received: 120 },
      { transport_status: "CONNECTED", sync_status: "RESYNC_IN_PROGRESS", sync_stage: "RECEIVING", sync_records_received: 1847 },
      { transport_status: "CONNECTED", sync_status: "RESYNC_IN_PROGRESS", sync_stage: "PUBLISHING", sync_records_received: 1847 },
      { transport_status: "CONNECTED", sync_status: "IN_SYNC", sync_stage: "COMPLETE", sync_records_received: 1847, last_sync_in_house_count: 461 },
    ];

    const { rerender } = render(
      <SynchronizationCard id="i1" health={health(steps[0])} onRefreshed={onRefreshed} />,
    );
    const seen: string[] = [];
    for (const step of steps) {
      rerender(<SynchronizationCard id="i1" health={health(step)} onRefreshed={onRefreshed} />);
      await vi.advanceTimersByTimeAsync(3500);
      seen.push(document.body.textContent ?? "");
    }

    expect(seen[1]).toMatch(/Requesting the full list/i);
    expect(seen[1]).toMatch(/happens automatically the first time/i);
    expect(seen[2]).toMatch(/Waiting for the PMS/i);
    expect(seen[3]).toMatch(/120/);
    expect(seen[4]).toMatch(/1,847/);
    expect(seen[5]).toMatch(/Publishing/i);
    expect(seen[6]).toMatch(/Complete/i);
    expect(seen[6]).toMatch(/461/);

    const afterComplete = onRefreshed.mock.calls.length;
    await vi.advanceTimersByTimeAsync(3000);
    expect(onRefreshed.mock.calls.length).toBe(afterComplete);
  });

  it("keeps exactly one timer when the rate changes", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();
    const { rerender } = render(
      <SynchronizationCard id="i1" health={health({ sync_stage: "COMPLETE" })} onRefreshed={onRefreshed} />,
    );
    // Catch overlapping polling loops through their doubled request rate.
    rerender(<SynchronizationCard id="i1" health={health({ sync_stage: "RECEIVING" })} onRefreshed={onRefreshed} />);
    await vi.advanceTimersByTimeAsync(3100);
    rerender(<SynchronizationCard id="i1" health={health({ sync_stage: "COMPLETE" })} onRefreshed={onRefreshed} />);

    onRefreshed.mockClear();
    await vi.advanceTimersByTimeAsync(10100);
    expect(onRefreshed.mock.calls.length).toBe(1);
  });
});

// Published sync is not ready until materialization completes; room sign-in remains closed until then.
describe("materialization readiness", () => {
  it("shows Applying guest list while COMPLETE but not yet materialized", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "COMPLETE", materialization_ready: false, in_house_stays: 461 })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getAllByText(/Applying the guest list/i).length).toBeGreaterThan(0);
    expect(screen.getByText(/Room sign-in resumes automatically/i)).toBeInTheDocument();
    expect(screen.queryByText(/^Complete$/)).toBeNull();
  });

  it("shows Complete once materialization is ready", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "COMPLETE", materialization_ready: true, in_house_stays: 595 })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getAllByText(/Complete/i).length).toBeGreaterThan(0);
    expect(screen.queryByText(/Applying the guest list/i)).toBeNull();
  });

  it("keeps polling while applying, because the guest list is still changing", async () => {
    vi.useFakeTimers();
    const onRefreshed = vi.fn();
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "COMPLETE", materialization_ready: false })}
        onRefreshed={onRefreshed}
      />,
    );
    await vi.advanceTimersByTimeAsync(7000);
    expect(onRefreshed.mock.calls.length).toBeGreaterThanOrEqual(2); // the fast 3s cadence
  });

  it("shows the live in-house figure only, never a stamped one", () => {
    render(
      <SynchronizationCard
        id="i1"
        health={health({ sync_stage: "COMPLETE", materialization_ready: true, in_house_stays: 595 })}
        onRefreshed={() => {}}
      />,
    );
    expect(screen.getByText(/595/)).toBeInTheDocument();
    expect(screen.queryByText(/461/)).toBeNull();
  });
});
