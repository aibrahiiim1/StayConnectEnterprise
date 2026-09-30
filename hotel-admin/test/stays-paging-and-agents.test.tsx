import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

// STAYS ARE PAGED ON THE SERVER, AND TRAVEL AGENTS ARE CHOSEN FROM WHAT THE PMS SENT.
//
// The Stays screen stopped at 200 rows and counted only those, so a hotel with 403 in house saw 200. The
// travel-agent condition was a free-text box, so a misspelt agent silently matched nobody.

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn() } };
});

beforeEach(() => get.mockReset());
afterEach(() => vi.resetModules());

const stay = (i: number) => ({
  id: "s" + i, pms_interface_id: "i1", external_reservation_id: "R" + i, room: String(1000 + i),
  status: "IN_HOUSE", lifecycle_version: 1, posting_allowed: false, occupants: 1,
  vip: i === 1, travel_agent: i === 1 ? "Sunny Tours" : null,
});

function pageOf(page: number, size: number, total: number) {
  const from = (page - 1) * size;
  const n = Math.max(0, Math.min(size, total - from));
  return {
    data: Array.from({ length: n }, (_, k) => stay(from + k + 1)),
    meta: { has_more: from + n < total },
    page, page_size: size,
    summary: { total, with_internet: 0, devices_online: 0, arriving: 0, vip: 7 },
  };
}

describe("Stays page — server paging", () => {
  it("counts every stay, pages through them, and searches without putting the text in the URL", async () => {
    get.mockImplementation((path: string) => {
      const u = new URL("http://x" + path);
      return Promise.resolve(pageOf(Number(u.searchParams.get("page")), Number(u.searchParams.get("page_size")), 403));
    });
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);

    expect(await screen.findByText("Showing 1–50 of 403")).toBeTruthy();
    expect(screen.getByText("403")).toBeTruthy(); // the counter is the property, not the page
    expect(screen.getByText("Agent: Sunny Tours")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–100 of 403")).toBeTruthy();
    expect(get).toHaveBeenLastCalledWith(expect.stringContaining("page=2"), undefined);

    await userEvent.type(screen.getByRole("searchbox", { name: "Search stays" }), "Andersen");
    await waitFor(() => {
      const [path, headers] = get.mock.calls[get.mock.calls.length - 1];
      expect(headers).toEqual({ "X-Stay-Search": "Andersen" });
      expect(path).not.toContain("Andersen");
      expect(path).toContain("page=1"); // a new search starts at the first page
    });
  });

  it("filters to VIP guests on the server", async () => {
    get.mockResolvedValue(pageOf(1, 50, 7));
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    await screen.findByText("Showing 1–7 of 7");
    await userEvent.click(screen.getByRole("checkbox", { name: "VIP only" }));
    await waitFor(() => expect(get).toHaveBeenLastCalledWith(expect.stringContaining("vip=true"), undefined));
  });
});

describe("Travel-agent picker", () => {
  async function renderPicker(initial = "") {
    const { TravelAgentPicker } = await import("@/app/(app)/internet-packages/travel-agent-picker");
    const seen: string[] = [];
    function Host() {
      const [v, setV] = useState(initial);
      return <TravelAgentPicker label="travel agents" value={v} onChange={(x) => { seen.push(x); setV(x); }} />;
    }
    render(<Host />);
    return seen;
  }

  it("lists the agents the PMS named, searchable, and stores the chosen names", async () => {
    get.mockResolvedValue({ data: [
      { name: "Blue Sea", in_house: 3, stays: 9 },
      { name: "Sunny Tours", in_house: 12, stays: 40 },
      { name: "TUI", in_house: 0, stays: 2 },
    ] });
    const seen = await renderPicker();
    expect(get).toHaveBeenCalledWith("/pms-stays/travel-agents");

    const box = screen.getByRole("combobox", { name: "travel agents" });
    await userEvent.click(box);
    expect(await screen.findByText("12 in house")).toBeTruthy();

    await userEvent.type(box, "sun");
    expect(screen.queryByText("Blue Sea")).toBeNull();
    await userEvent.click(screen.getByRole("option", { name: /Sunny Tours/ }));
    expect(seen[seen.length - 1]).toBe("Sunny Tours");

    await userEvent.clear(box);
    await userEvent.click(screen.getByRole("option", { name: /Blue Sea/ }));
    expect(seen[seen.length - 1]).toBe("Sunny Tours, Blue Sea");

    await userEvent.click(screen.getByRole("button", { name: "Remove Sunny Tours" }));
    expect(seen[seen.length - 1]).toBe("Blue Sea");
  });

  it("can add an agent the PMS has not sent yet, but never a name with a comma", async () => {
    get.mockResolvedValue({ data: [] });
    const seen = await renderPicker();
    const box = screen.getByRole("combobox", { name: "travel agents" });
    await userEvent.type(box, "New Agent{Enter}");
    expect(seen[seen.length - 1]).toBe("New Agent");

    await userEvent.type(box, "A, B");
    expect(screen.queryByRole("option", { name: /Add/ })).toBeNull();
  });
});
