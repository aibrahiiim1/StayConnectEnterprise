import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { api, BACKGROUND_HEADER } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";

// Timer requests carry X-OneGate-Background so idle sessions expire even with a screen open.

type Sent = { method: string; url: string; headers: Record<string, string>; body: unknown };

function recordFetch(answer: (s: Sent) => { status?: number; body: unknown } = () => ({ body: {} })) {
  const sent: Sent[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const s: Sent = {
      method: (init?.method ?? "GET").toUpperCase(),
      url: String(input),
      headers: { ...((init?.headers as Record<string, string>) ?? {}) },
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    sent.push(s);
    const a = answer(s);
    const status = a.status ?? 200;
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: { get: () => "application/json" },
      json: async () => a.body,
      text: async () => JSON.stringify(a.body),
    } as unknown as Response;
  }));
  return sent;
}

function Poller() {
  usePoll((opts) => { void api.get("/v1/poll", opts).catch(() => undefined); }, 30_000);
  return null;
}

describe("background requests", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("the header name matches the appliance's", () => {
    expect(BACKGROUND_HEADER).toBe("X-OneGate-Background");
  });

  it("usePoll's timer-driven requests carry X-OneGate-Background: 1", async () => {
    const sent = recordFetch();
    render(<Poller />);
    expect(sent).toHaveLength(0);
    await act(async () => { vi.advanceTimersByTime(30_000); });
    await act(async () => { vi.advanceTimersByTime(30_000); });
    expect(sent).toHaveLength(2);
    for (const s of sent) expect(s.headers["X-OneGate-Background"]).toBe("1");
  });

  it("a hidden tab coming back refreshes in the background too", async () => {
    const sent = recordFetch();
    render(<Poller />);
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
    expect(sent).toHaveLength(1);
    expect(sent[0].headers["X-OneGate-Background"]).toBe("1");
  });

  it("a request the person caused does not carry it", async () => {
    const sent = recordFetch();
    await api.get("/v1/x");
    await api.post("/v1/y", { a: 1 });
    expect(sent.map((s) => s.headers["X-OneGate-Background"])).toEqual([undefined, undefined]);
    expect(sent[1].headers["Content-Type"]).toBe("application/json");
  });
});
