import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn() } };
});

beforeEach(() => get.mockReset());
afterEach(() => vi.resetModules());

async function stayTable(): Promise<HTMLElement> {
  return waitFor(() => {
    const t = document.querySelector("table");
    if (!t) throw new Error("no table yet");
    return t as HTMLElement;
  });
}

const A = "11111111-1111-4111-8111-111111111111";
const B = "22222222-2222-4222-8222-222222222222";

const stay = (id: string, iface: string, label: string, res: string, room: string) => ({
  id, pms_interface_id: iface, pms_interface_label: label, external_reservation_id: res, room,
  status: "IN_HOUSE", lifecycle_version: 1, posting_allowed: false, occupants: 1,
});

const both = [stay("s1", A, "Protel Hotel", "RES-A", "101"), stay("s2", B, "Aqua Club", "RES-B", "101")];
const byInterface = [
  { pms_interface_id: B, label: "Aqua Club", lifecycle_state: "ACTIVE", total: 1, in_house: 1 },
  { pms_interface_id: A, label: "Protel Hotel", lifecycle_state: "ACTIVE", total: 1, in_house: 1 },
];

function serve() {
  get.mockImplementation((path?: string) => {
    if (typeof path !== "string") return Promise.resolve({});
    if (path.startsWith("/pms-interfaces")) {
      return Promise.resolve({ interfaces: [
        { id: A, display_label: "Protel Hotel", lifecycle_state: "ACTIVE", published: true },
        { id: B, display_label: "Aqua Club", lifecycle_state: "ACTIVE", published: true },
      ] });
    }
    if (path.startsWith("/pms-stays")) {
      const only = new URL("http://x" + path).searchParams.get("pms_interface_id");
      const data = only ? both.filter((s) => s.pms_interface_id === only) : both;
      return Promise.resolve({ data, meta: { has_more: false }, page: 1, page_size: 50,
        summary: { total: data.length, with_internet: 0, devices_online: 0, arriving: 0, vip: 0 }, by_interface: byInterface });
    }
    return Promise.resolve({});
  });
}

describe("Stays across several PMS connections", () => {
  it("shows the same room number at two connections as two rooms, each naming its connection", async () => {
    serve();
    const { resetPmsScopeCache } = await import("@/lib/pms-scope");
    resetPmsScopeCache();
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    const table = await stayTable();
    await waitFor(() => expect(within(table).getByText("Aqua Club")).toBeTruthy());
    expect(within(table).getAllByText("101")).toHaveLength(2);
    expect(within(table).getByText("Protel Hotel")).toBeTruthy();
    expect(screen.getByRole("radio", { name: /All connections/ })).toBeTruthy();
  });

  it("filters to one connection and keeps every connection's count in the tabs", async () => {
    serve();
    const { resetPmsScopeCache } = await import("@/lib/pms-scope");
    resetPmsScopeCache();
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    const tab = await screen.findByRole("radio", { name: /Aqua Club/ });
    expect(screen.getByRole("radio", { name: /All connections/ })).toBeTruthy();
    await userEvent.click(tab);
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringContaining(`pms_interface_id=${B}`), undefined));
    await waitFor(() => expect(within(document.querySelector("table") as HTMLElement).queryByText("Protel Hotel")).toBeNull());
    expect(screen.getByRole("radio", { name: /Protel Hotel/ })).toBeTruthy();
  });

  it("does not clutter a single-PMS site with connection names", async () => {
    get.mockImplementation((path?: string) => {
    if (typeof path !== "string") return Promise.resolve({});
      if (path.startsWith("/pms-interfaces")) return Promise.resolve({ interfaces: [{ id: A, display_label: "Protel Hotel", lifecycle_state: "ACTIVE", published: true }] });
      if (path.startsWith("/pms-stays")) {
        return Promise.resolve({ data: [stay("s1", A, "Protel Hotel", "RES-A", "101")], meta: { has_more: false }, page: 1, page_size: 50,
          summary: { total: 1, with_internet: 0, devices_online: 0, arriving: 0, vip: 0 },
          by_interface: [{ pms_interface_id: A, label: "Protel Hotel", lifecycle_state: "ACTIVE", total: 1, in_house: 1 }] });
      }
      return Promise.resolve({});
    });
    const { resetPmsScopeCache } = await import("@/lib/pms-scope");
    resetPmsScopeCache();
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    const table = await stayTable();
    expect(within(table).queryByText("Protel Hotel")).toBeNull();
    expect(screen.queryByRole("radio", { name: /All connections/ })).toBeNull();
  });
});
