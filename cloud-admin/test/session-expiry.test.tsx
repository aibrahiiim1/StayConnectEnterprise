import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { mockFetch } from "./helpers";
import { api, isEndedSignIn, setUnauthorizedHandler } from "@/lib/api";
import {
  deadlinesFrom, destinationAfterSignIn, expiredSignInUrl, nextEnding, safeNext, sameDeadlines, type Deadlines,
} from "@/lib/session-expiry";
import { SessionExpiryWarning } from "@/components/session-expiry";

// The return path must stay within this console after either session deadline.

describe("safeNext", () => {
  it("follows a same-origin relative path", () => {
    expect(safeNext("/customers/c1?tab=sites")).toBe("/customers/c1?tab=sites");
    expect(safeNext("/appliances#top")).toBe("/appliances#top");
    expect(safeNext("/system/audit")).toBe("/system/audit");
  });

  it.each([
    ["", "empty"],
    ["customers", "relative without a slash"],
    ["//evil.example", "protocol-relative"],
    ["/\\evil.example", "backslash read as a slash"],
    ["\\\\evil.example", "backslashes"],
    ["/x\\y", "backslash anywhere"],
    ["https://evil.example/", "absolute URL"],
    ["javascript:alert(1)", "script scheme"],
    ["/\t/evil.example", "tab dropped by the URL parser"],
    ["/\n/evil.example", "newline dropped by the URL parser"],
    ["/login", "the sign-in page itself"],
    ["/login?next=/x", "the sign-in page with its own next"],
  ])("refuses %j (%s)", (raw) => {
    expect(safeNext(raw)).toBeNull();
  });

  it("falls back to the Overview", () => {
    expect(destinationAfterSignIn(null)).toBe("/overview");
    expect(destinationAfterSignIn("//evil.example")).toBe("/overview");
    expect(destinationAfterSignIn("/licenses")).toBe("/licenses");
  });

  it("builds the expired sign-in address with a safe next only", () => {
    expect(expiredSignInUrl("/customers/c1?tab=sites")).toBe("/login?next=%2Fcustomers%2Fc1%3Ftab%3Dsites&reason=expired");
    expect(expiredSignInUrl("//evil.example")).toBe("/login?reason=expired");
  });
});

describe("deadlines from whoami", () => {
  it("moves them onto this browser's clock", () => {
    // Server clock drift must not shift the warning.
    const received = Date.parse("2026-10-19T10:00:00Z");
    const d = deadlinesFrom({
      server_time: "2026-10-19T11:00:00Z",
      idle_expires_at: "2026-10-19T11:30:00Z",
      absolute_expires_at: "2026-10-19T19:00:00Z",
    }, received);
    expect(d.idle).toBe(received + 30 * 60_000);
    expect(d.absolute).toBe(received + 8 * 3600_000);
    expect(nextEnding(d)).toEqual({ at: d.idle, kind: "idle" });
  });

  it("an older Central without them warns about nothing", () => {
    expect(nextEnding(deadlinesFrom({}))).toBeNull();
  });

  it("the absolute limit wins a tie, and readings a moment apart are the same", () => {
    expect(nextEnding({ idle: 100, absolute: 100 })?.kind).toBe("absolute");
    expect(sameDeadlines({ idle: 1000, absolute: null }, { idle: 1800, absolute: null })).toBe(true);
    expect(sameDeadlines({ idle: 1000, absolute: null }, { idle: 70_000, absolute: null })).toBe(false);
  });
});

describe("a 401 from the API", () => {
  afterEach(() => setUnauthorizedHandler(null));

  it("is an ended sign-in on an authenticated route, and goes to the registered handler", async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    mockFetch([{ method: "GET", match: "/api/cloud/v1/customers", status: 401, body: { error: "unauthenticated", message: "session expired" } }]);
    await expect(api.get("/cloud/v1/customers")).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("is a wrong password or code on the sign-in and step-up routes, which the form reports", async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    mockFetch([
      { method: "POST", match: "/api/v1/auth/login", status: 401, body: { error: "unauthenticated", message: "invalid credentials" } },
      { method: "POST", match: "/api/v1/auth/login/code", status: 401, body: { error: "wrong_code", message: "no" } },
      { method: "POST", match: "/api/v1/auth/reauth", status: 401, body: { error: "unauthenticated", message: "no" } },
    ]);
    await expect(api.post("/v1/auth/login", {})).rejects.toBeTruthy();
    await expect(api.post("/v1/auth/login/code", {})).rejects.toBeTruthy();
    await expect(api.post("/v1/auth/reauth", {})).rejects.toBeTruthy();
    expect(handler).not.toHaveBeenCalled();
    expect(isEndedSignIn(401, "/v1/auth/whoami")).toBe(true);
    expect(isEndedSignIn(403, "/cloud/v1/team")).toBe(false);
  });
});

describe("the warning before a sign-in ends", () => {
  const MIN = 60_000;
  beforeEach(() => {
    vi.useFakeTimers({ now: Date.parse("2026-10-19T10:00:00Z") });
  });
  afterEach(() => vi.useRealTimers());

  function Harness({ start, extended, onExpired, keepAlive }: {
    start: Deadlines;
    extended?: Deadlines;
    onExpired: () => void;
    keepAlive: () => Promise<unknown>;
  }) {
    const [d, setD] = useState(start);
    return (
      <SessionExpiryWarning
        deadlines={d}
        refresh={async () => d}
        keepAlive={async () => {
          await keepAlive();
          if (extended) setD(extended);
        }}
        onExpired={onExpired}
        onSignOut={() => {}}
      />
    );
  }

  it("asks two minutes before the idle limit, and staying keeps the sign-in", async () => {
    const now = Date.now();
    const keepAlive = vi.fn(async () => ({}));
    const onExpired = vi.fn();
    render(
      <Harness
        start={{ idle: now + 10 * MIN, absolute: now + 8 * 60 * MIN }}
        extended={{ idle: now + 40 * MIN, absolute: now + 8 * 60 * MIN }}
        keepAlive={keepAlive}
        onExpired={onExpired}
      />,
    );
    await act(async () => { await vi.advanceTimersByTimeAsync(7 * MIN); });
    expect(screen.queryByRole("alertdialog")).toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(1 * MIN + 1000); });
    const dialog = screen.getByRole("alertdialog", { name: "Are you still there?" });
    expect(dialog).toHaveAccessibleDescription(/no activity/);
    // Do not announce every countdown tick to screen readers.
    expect(screen.getByText(/^\d:\d\d$/)).toHaveAttribute("aria-hidden", "true");

    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Stay signed in" })); });
    expect(keepAlive).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(5 * MIN); });
    expect(onExpired).not.toHaveBeenCalled();
  });

  it("says plainly when the fixed lifetime is ending, offers no way to extend it, and ends it on time", async () => {
    const now = Date.now();
    const onExpired = vi.fn();
    render(<Harness start={{ idle: now + 30 * MIN, absolute: now + 3 * MIN }} keepAlive={vi.fn()} onExpired={onExpired} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(1 * MIN + 1000); });
    const dialog = screen.getByRole("alertdialog", { name: "Your sign-in is about to end" });
    expect(dialog).toHaveAccessibleDescription(/fixed time/);
    expect(screen.queryByRole("button", { name: "Stay signed in" })).toBeNull();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();

    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "OK" })); });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(onExpired).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(2 * MIN); });
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it("speaks only at one minute and at thirty seconds", async () => {
    const now = Date.now();
    render(<Harness start={{ idle: now + 3 * MIN, absolute: null }} keepAlive={vi.fn()} onExpired={vi.fn()} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(1 * MIN + 1000); });
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("");
    await act(async () => { await vi.advanceTimersByTimeAsync(40_000); });
    expect(status).toHaveTextContent("");
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(status).toHaveTextContent("1 minute left.");
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(status).toHaveTextContent("30 seconds left.");
  });
});
