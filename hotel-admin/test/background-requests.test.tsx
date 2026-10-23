import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook } from "@testing-library/react";

// Every request a poll makes (even after an await) must carry X-OneGate-Background; an operator's own request must not.

import { api, inBackground, BACKGROUND_HEADER } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";

type Call = { url: string; method: string; headers: Record<string, string> };
let calls: Call[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    calls.push({ url, method: init.method ?? "GET", headers: (init.headers as Record<string, string>) ?? {} });
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  }));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const background = (c: Call) => c.headers[BACKGROUND_HEADER] === "1";

describe("background requests", () => {
  it("usePoll marks every request its callback makes, before and after an await", async () => {
    vi.useFakeTimers();
    renderHook(() =>
      usePoll(async () => {
        await api.get("/sessions");
        await api.get("/sessions/health");
      }, 1000),
    );
    await vi.advanceTimersByTimeAsync(1000);
    expect(calls.map((c) => c.url)).toEqual(["/api/edge/v1/sessions", "/api/edge/v1/sessions/health"]);
    expect(calls.every(background)).toBe(true);
  });

  it("an operator's own request is not marked", async () => {
    await api.get("/sessions");
    await api.post("/sessions/x/disconnect", {});
    expect(calls.some(background)).toBe(false);
  });

  it("only reads are marked, and the mark ends when the poll settles", async () => {
    await inBackground(async () => {
      await api.get("/a");
      await api.post("/b", {});
    });
    await api.get("/c");
    expect(calls.map(background)).toEqual([true, false, false]);
  });

  it("keeps a caller's own headers", async () => {
    await inBackground(() => api.get("/guest-data-rights/search", { "X-Guest-Data-Search": "x" }));
    expect(calls[0].headers).toMatchObject({ "X-Guest-Data-Search": "x", [BACKGROUND_HEADER]: "1" });
  });
});
