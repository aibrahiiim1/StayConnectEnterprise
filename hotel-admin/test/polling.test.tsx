import { describe, it, expect, vi, afterEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

import { usePoll } from "@/lib/use-poll";
import { getSharedHealth } from "@/lib/shared-health";
import { useServerPage } from "@/lib/use-server-page";

// Performance audit D8-M1, D8-L1, D8-L2: polls pause in hidden tabs, never stack, and the health badge and the
// dashboard share one /health answer.

let hidden = false;
Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });

afterEach(() => {
  hidden = false;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("usePoll", () => {
  it("does not poll while the tab is hidden, and refreshes once on return", async () => {
    vi.useFakeTimers();
    const fn = vi.fn(async () => {});
    renderHook(() => usePoll(fn, 1000));
    hidden = true;
    await vi.advanceTimersByTimeAsync(5000);
    expect(fn).not.toHaveBeenCalled();
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("skips a tick while the previous call is still pending", async () => {
    vi.useFakeTimers();
    let release!: () => void;
    const fn = vi.fn(() => new Promise<void>((r) => { release = r; }));
    renderHook(() => usePoll(fn, 1000));
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fn).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(1000);
    expect(fn).toHaveBeenCalledTimes(2);
  });
});

describe("useServerPage", () => {
  it("cancels the superseded request when the question changes", async () => {
    const signals: AbortSignal[] = [];
    vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit = {}) => {
      if (init.signal) signals.push(init.signal);
      return new Promise<Response>(() => {});
    }));
    const { rerender } = renderHook(({ path }) => useServerPage({ path }), { initialProps: { path: "/audit?action=a" } });
    rerender({ path: "/audit?action=b" });
    await vi.waitFor(() => expect(signals.length).toBe(2));
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);
  });
});

describe("shared health", () => {
  it("one request serves callers that ask within a few seconds", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ status: "ok", db: true, scd: true }), {
      status: 200, headers: { "content-type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const [a, b] = await Promise.all([getSharedHealth(), getSharedHealth()]);
    expect(a).toEqual(b);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("no raw polling timers", () => {
  // Only display tickers (no requests) may use setInterval directly; everything that fetches goes through usePoll.
  const allowed = new Set([
    "app/(app)/network/system/page.tsx",
    "components/ui/patterns.tsx",
    "components/guest-signin-restrictions.tsx",
    "components/idle-warning.tsx",
    "lib/use-poll.ts",
  ]);
  const root = join(__dirname, "..");
  const walk = (dir: string): string[] =>
    readdirSync(join(root, dir)).flatMap((n) => {
      const rel = `${dir}/${n}`;
      return statSync(join(root, rel)).isDirectory() ? walk(rel) : /\.tsx?$/.test(n) ? [rel] : [];
    });
  it("lists every setInterval outside usePoll as a display ticker", () => {
    const offenders = ["app", "components", "lib"].flatMap(walk)
      .filter((f) => /setInterval\(/.test(readFileSync(join(root, f), "utf8")) && !allowed.has(f));
    expect(offenders).toEqual([]);
  });
});
