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

  // A filter changed on page 2 fires the old page's request and then the reset's page-1 request. The answers can
  // come back in any order; the screen must show the answer to the question the controls now ask.
  it("ignores an older response that arrives after the newer one", async () => {
    const pending: { path: string; headers: any; resolve: (v: any) => void }[] = [];
    let staysCalls = 0;
    get.mockImplementation((path: string, headers?: any) => {
      if (typeof path !== "string" || !path.startsWith("/pms-stays")) return Promise.resolve({});
      if (staysCalls++ === 0) return Promise.resolve(pageOf(1, 50, 403)); // the first load answers at once
      return new Promise((resolve) => pending.push({ path, headers, resolve }));
    });
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    expect(await screen.findByText("Showing 1–50 of 403")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: /Next/ })); // page 2, left pending
    await userEvent.type(screen.getByRole("searchbox", { name: "Search stays" }), "Andersen");
    await waitFor(() => expect(pending.some((p) => p.headers && p.path.includes("page=1"))).toBe(true));

    // The race exists: the old offset was asked WITH the new search before the reset asked page 1.
    expect(pending.some((p) => p.headers && p.path.includes("page=2"))).toBe(true);
    // The newest question (search, page 1) answers FIRST...
    const newest = pending[pending.length - 1];
    expect(newest.path).toContain("page=1");
    newest.resolve({ ...pageOf(1, 50, 1), data: [stay(7)] });
    expect(await screen.findByText("Showing 1–1 of 1")).toBeTruthy();

    // ...then every older one arrives late. None may replace it.
    for (const p of pending.slice(0, -1)) p.resolve(pageOf(2, 50, 403));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByText("Showing 1–1 of 1")).toBeTruthy();
    expect(screen.queryByText(/of 403/)).toBeNull();
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
  async function renderPicker(initial: string[] = []) {
    const { TravelAgentPicker } = await import("@/app/(app)/internet-packages/travel-agent-picker");
    const seen: string[][] = [];
    function Host() {
      const [v, setV] = useState<string[]>(initial);
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
    expect(seen[seen.length - 1]).toEqual(["Sunny Tours"]);

    await userEvent.clear(box);
    await userEvent.click(screen.getByRole("option", { name: /Blue Sea/ }));
    expect(seen[seen.length - 1]).toEqual(["Sunny Tours", "Blue Sea"]);

    await userEvent.click(screen.getByRole("button", { name: "Remove Sunny Tours" }));
    expect(seen[seen.length - 1]).toEqual(["Blue Sea"]);
  });

  // A PMS may name an agent with a comma in it. Chosen from the list, it is ONE agent: one chip, one entry.
  it("keeps a PMS-provided name with a comma as one agent", async () => {
    get.mockResolvedValue({ data: [
      { name: "Sun Tours, Ltd", in_house: 4, stays: 6 },
      { name: "Sun Tours", in_house: 1, stays: 1 },
    ] });
    const seen = await renderPicker();
    const box = screen.getByRole("combobox", { name: "travel agents" });
    await userEvent.click(box);
    await userEvent.click(await screen.findByRole("option", { name: /Sun Tours, Ltd/ }));
    expect(seen[seen.length - 1]).toEqual(["Sun Tours, Ltd"]);
    expect(screen.getByRole("button", { name: "Remove Sun Tours, Ltd" })).toBeTruthy();
    // The similarly named agent is still a separate, unselected choice.
    expect(screen.getByRole("option", { name: /^Sun Tours1 in house$/ }).getAttribute("aria-selected")).toBe("false");

    await userEvent.click(screen.getByRole("button", { name: "Remove Sun Tours, Ltd" }));
    expect(seen[seen.length - 1]).toEqual([]);
  });

  it("adds a typed agent the PMS has not sent yet as one name, commas included", async () => {
    get.mockResolvedValue({ data: [] });
    const seen = await renderPicker();
    const box = screen.getByRole("combobox", { name: "travel agents" });
    await userEvent.type(box, "New Agent{Enter}");
    expect(seen[seen.length - 1]).toEqual(["New Agent"]);

    await userEvent.type(box, "Coral Travel, Cairo{Enter}");
    expect(seen[seen.length - 1]).toEqual(["New Agent", "Coral Travel, Cairo"]);
  });

  it("shows a saved comma name as one chip and does not add it twice", async () => {
    get.mockResolvedValue({ data: [{ name: "Sun Tours, Ltd", in_house: 4, stays: 6 }] });
    await renderPicker(["Sun Tours, Ltd"]);
    expect(screen.getByRole("button", { name: "Remove Sun Tours, Ltd" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove Sun Tours" })).toBeNull();
    const box = screen.getByRole("combobox", { name: "travel agents" });
    await userEvent.type(box, "sun tours, ltd");
    expect(screen.queryByRole("option", { name: /Add/ })).toBeNull();
  });
});

// THE RULE A PACKAGE STORES. Loading a saved package and saving it again must give back the same agents: a name
// with a comma used to be joined into text on load and split into two agents on the next save.
describe("Travel-agent rule round trip", () => {
  it("serializes each name whole, trimmed, once", async () => {
    const { serializeRule } = await import("@/lib/commerce-form");
    expect(serializeRule({ type: "TRAVEL_AGENT", travel_agents: [" Sun Tours, Ltd ", "TUI", "tui", ""] }))
      .toEqual({ type: "TRAVEL_AGENT", value: { travel_agents: ["Sun Tours, Ltd", "TUI"] } });
  });

  it("load then save returns the stored list unchanged", async () => {
    const { serializeRule } = await import("@/lib/commerce-form");
    const { rulesToForm } = await import("@/app/(app)/internet-packages/form-mapping");
    const stored = { type: "TRAVEL_AGENT", value: { travel_agents: ["Sun Tours, Ltd", "Blue Sea"] } };
    const form = rulesToForm([stored] as any);
    expect(form).toEqual([{ type: "TRAVEL_AGENT", travel_agents: ["Sun Tours, Ltd", "Blue Sea"] }]);
    expect(serializeRule(form[0])).toEqual(stored);
    // Go's capitalised spelling loads the same way.
    expect(rulesToForm([{ Type: "TRAVEL_AGENT", Value: stored.value }] as any)).toEqual(form);
  });
});
