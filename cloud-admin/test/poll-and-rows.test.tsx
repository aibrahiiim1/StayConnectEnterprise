import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { usePoll } from "@/lib/use-poll";
import { keepUnchanged } from "@/lib/keep-unchanged";

// Performance audit D8-L1: a tick while the previous call is still pending is skipped.
describe("usePoll", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("keeps at most one call in flight", async () => {
    let calls = 0;
    let resolve: (() => void) | null = null;
    function Poller() {
      usePoll(() => {
        calls++;
        return new Promise<void>((r) => { resolve = r; });
      }, 10_000);
      return null;
    }
    render(<Poller />);
    await act(async () => { vi.advanceTimersByTime(10_000); });
    expect(calls).toBe(1);
    await act(async () => { vi.advanceTimersByTime(10_000); });
    await act(async () => { vi.advanceTimersByTime(10_000); });
    expect(calls).toBe(1); // the slow answer is still pending: both ticks skipped
    await act(async () => { resolve?.(); });
    await act(async () => { vi.advanceTimersByTime(10_000); });
    expect(calls).toBe(2);
  });

  it("a callback that returns nothing polls on every tick, as before", async () => {
    let calls = 0;
    function Poller() {
      usePoll(() => { calls++; }, 10_000);
      return null;
    }
    render(<Poller />);
    await act(async () => { vi.advanceTimersByTime(30_000); });
    expect(calls).toBe(3);
  });
});

// Performance audit D8-M2: unchanged rows keep their identity, so memoised rows do not re-render.
describe("keepUnchanged", () => {
  const a = { id: "a", v: 1 };
  const b = { id: "b", v: 1 };

  it("returns the previous array when nothing changed", () => {
    const prev = [a, b];
    expect(keepUnchanged(prev, [{ id: "a", v: 1 }, { id: "b", v: 1 }])).toBe(prev);
  });

  it("reuses unchanged rows and takes changed ones", () => {
    const prev = [a, b];
    const changed = { id: "b", v: 2 };
    const out = keepUnchanged(prev, [{ id: "a", v: 1 }, changed]);
    expect(out).not.toBe(prev);
    expect(out[0]).toBe(a);
    expect(out[1]).toBe(changed);
  });

  it("follows a new order and new rows", () => {
    const prev = [a, b];
    const c = { id: "c", v: 1 };
    const out = keepUnchanged(prev, [{ id: "b", v: 1 }, c, { id: "a", v: 1 }]);
    expect(out).toEqual([b, c, a]);
    expect(out[0]).toBe(b);
    expect(out[2]).toBe(a);
  });

  it("uses next as is on the first load", () => {
    const next = [a];
    expect(keepUnchanged(null, next)).toBe(next);
  });
});
