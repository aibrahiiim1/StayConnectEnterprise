import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen } from "@testing-library/react";

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a) } };
});

import { WhoamiProvider, useOperatorRoles, useWhoamiSession, WHOAMI_REVALIDATE_MS } from "@/lib/whoami-context";

let renders = 0;
function Roles() {
  const roles = useOperatorRoles();
  renders++;
  return <p>roles: {roles === null ? "unknown" : roles.join(",") || "none"}</p>;
}

function Shell({ onInvalid }: { onInvalid: () => void }) {
  const { me, loading } = useWhoamiSession(onInvalid);
  return <WhoamiProvider value={me}>{loading ? null : <Roles />}</WhoamiProvider>;
}

const who = (roles: string[]) => ({ user_id: "u1", email: "op", roles });

describe("session identity re-validation", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    get.mockReset();
    renders = 0;
  });
  afterEach(() => vi.useRealTimers());

  it("a role granted or revoked meanwhile replaces the shared roles", async () => {
    get.mockResolvedValue(who(["frontdesk"]));
    render(<Shell onInvalid={() => {}} />);
    await act(async () => {});
    expect(screen.getByText("roles: frontdesk")).toBeTruthy();

    get.mockResolvedValue(who(["frontdesk", "site_admin"]));
    await act(async () => {
      vi.advanceTimersByTime(WHOAMI_REVALIDATE_MS);
    });
    expect(screen.getByText("roles: frontdesk,site_admin")).toBeTruthy();

    get.mockResolvedValue(who([]));
    await act(async () => {
      vi.advanceTimersByTime(WHOAMI_REVALIDATE_MS);
    });
    expect(screen.getByText("roles: none")).toBeTruthy();
  });

  it("an unchanged answer re-renders nothing", async () => {
    get.mockResolvedValue(who(["frontdesk"]));
    render(<Shell onInvalid={() => {}} />);
    await act(async () => {});
    const settled = renders;
    get.mockResolvedValue(who(["frontdesk"]));
    await act(async () => {
      vi.advanceTimersByTime(WHOAMI_REVALIDATE_MS * 3);
    });
    expect(get).toHaveBeenCalledTimes(4);
    expect(renders).toBe(settled);
  });

  it("a failed re-validation hands over to the caller's bounce", async () => {
    const onInvalid = vi.fn();
    get.mockResolvedValue(who(["frontdesk"]));
    render(<Shell onInvalid={onInvalid} />);
    await act(async () => {});
    get.mockRejectedValue(new Error("401"));
    await act(async () => {
      vi.advanceTimersByTime(WHOAMI_REVALIDATE_MS);
    });
    expect(onInvalid).toHaveBeenCalledTimes(1);
  });
});
