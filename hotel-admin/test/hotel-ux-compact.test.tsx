import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { formatRelative } from "@/lib/utils";
import type { RoomSignInReadiness } from "@/lib/pms-availability";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as Record<string, unknown>).ResizeObserver = (globalThis as Record<string, unknown>).ResizeObserver ?? ResizeObserverStub;

const pathname = vi.fn(() => "/room-sign-in");
vi.mock("next/navigation", () => ({ usePathname: () => pathname() }));

afterEach(() => cleanup());

describe("relative times", () => {
  it("read a few seconds of clock skew as just now, and a real future time as a date", () => {
    expect(formatRelative(new Date(Date.now() + 20_000).toISOString())).toBe("just now");
    expect(formatRelative(new Date(Date.now() + 3_600_000).toISOString())).not.toMatch(/ago|just now/);
  });
});

describe("room sign-in readiness notice", () => {
  const partial = {
    state: "partial",
    affected: [{ networkId: "n1", guestNetwork: "Lobby", pmsInterface: "Protel", reason: "Protel is not in service" }],
    unchecked: [],
  } as unknown as RoomSignInReadiness;

  it("names the network and the cause on one line, with both fixes", async () => {
    const { RoomSignInReadinessCallout } = await import("@/components/room-sign-in-readiness");
    render(<RoomSignInReadinessCallout readiness={partial} />);
    const notice = screen.getByRole("status");
    expect(notice).toHaveTextContent("Room sign-in is not working on some client networks");
    expect(notice).toHaveTextContent("Lobby: Protel is not in service");
    expect(screen.getByRole("link", { name: /PMS routing/ })).toHaveAttribute("href", "/pms-routing");
    expect(screen.getByRole("link", { name: /PMS connections/ })).toHaveAttribute("href", "/pms-interfaces");
  });

  it("does not link to the page it is shown on", async () => {
    pathname.mockReturnValue("/pms-routing");
    const { RoomSignInReadinessCallout } = await import("@/components/room-sign-in-readiness");
    render(<RoomSignInReadinessCallout readiness={partial} />);
    expect(screen.queryByRole("link", { name: /PMS routing/ })).toBeNull();
    expect(screen.getByRole("link", { name: /PMS connections/ })).toBeInTheDocument();
  });
});
