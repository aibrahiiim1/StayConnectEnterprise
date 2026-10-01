import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// CLIENT ACCOUNTS: paged, searched and counted on the server, newest first; and the generated-password format,
// whose bounds come from the appliance rather than from this bundle.

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a), post: vi.fn(), patch: vi.fn(), del: vi.fn() },
  };
});

beforeEach(() => {
  get.mockReset();
  put.mockReset();
});
afterEach(() => {
  cleanup();
  vi.resetModules();
});

const account = (i: number) => ({
  id: "a" + i, username: `user-${String(i).padStart(3, "0")}`, enabled: true, login_count: 0, active_devices: 0,
  created_at: new Date(Date.UTC(2026, 8, 1) + i * 60_000).toISOString(),
});

// The server answers newest first: account 130 is the newest.
function pageOf(page: number, size: number, total: number) {
  const from = (page - 1) * size;
  const n = Math.max(0, Math.min(size, total - from));
  return {
    data: Array.from({ length: n }, (_, k) => account(total - from - k)),
    meta: { has_more: from + n < total },
    authority: "iam_v2",
    page, page_size: size,
    summary: { total, enabled: total - 3, disabled: 3, locked: 2, devices_online: 11 },
  };
}

const FORMAT = {
  password_style: "mixed", password_length: 14, config_version: 0,
  limits: {
    entropy_floor_bits: 40, max_length: 32, default_style: "mixed", default_length: 14,
    styles: [
      { key: "mixed", label: "Upper and lower case letters and digits", alphabet: "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789", min_length: 7, max_length: 32, bits_per_char: 5.781 },
      { key: "upper_digits", label: "Upper case letters and digits", alphabet: "ABCDEFGHJKMNPQRSTUVWXYZ23456789", min_length: 9, max_length: 32, bits_per_char: 4.954 },
      { key: "lower_digits", label: "Lower case letters and digits", alphabet: "abcdefghijkmnpqrstuvwxyz23456789", min_length: 8, max_length: 32, bits_per_char: 5 },
      { key: "digits", label: "Digits only", alphabet: "23456789", min_length: 14, max_length: 32, bits_per_char: 3 },
    ],
  },
};

function wire(roles: string[], accounts: (path: string, headers?: any) => any = (path) => {
  const u = new URL("http://x" + path);
  return Promise.resolve(pageOf(Number(u.searchParams.get("page")), Number(u.searchParams.get("page_size")), 130));
}) {
  get.mockImplementation((path: string, headers?: any) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/guest-accounts/portal") return Promise.resolve({ enabled: true });
    if (path === "/account-password-settings/") return Promise.resolve(FORMAT);
    if (path === "/account-password-settings/changes") {
      return Promise.resolve({ changes: [{
        changed_at: "2026-09-30T10:00:00Z", changed_by: "it@hotel.test", change_reason: "keypad kiosk",
        old_password_style: null, old_password_length: null, new_password_style: "mixed", new_password_length: 14,
        new_config_version: 1,
      }] });
    }
    if (typeof path === "string" && path.startsWith("/guest-accounts?")) return accounts(path, headers);
    return Promise.resolve({});
  });
}

const accountCalls = () => get.mock.calls.filter(([p]) => typeof p === "string" && p.startsWith("/guest-accounts?"));

describe("Client accounts — server paging", () => {
  it("counts every account, renders newest first, pages, and searches without putting the text in the URL", async () => {
    wire(["site_admin"]);
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);

    expect(await screen.findByText("Showing 1–50 of 130")).toBeTruthy();
    expect(screen.getByText("130")).toBeTruthy(); // the counter is the site, not the page
    expect(screen.getByText("11")).toBeTruthy(); // devices online, from the server summary

    // Newest first, in the order the server sent.
    const names = screen.getAllByText(/^user-\d{3}$/).map((e) => e.textContent);
    expect(names.slice(0, 3)).toEqual(["user-130", "user-129", "user-128"]);
    expect(names).toHaveLength(50);

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–100 of 130")).toBeTruthy();
    expect(accountCalls().at(-1)?.[0]).toContain("page=2");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search client accounts" }), "user-12");
    await waitFor(() => {
      const [path, headers] = accountCalls().at(-1)!;
      expect(headers).toEqual({ "X-Account-Search": "user-12" });
      expect(path).not.toContain("user-12");
      expect(path).toContain("page=1"); // a new search starts at the first page
    });
  });

  it("sends the status filter to the server and starts again at page 1", async () => {
    wire(["site_admin"]);
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    expect(await screen.findByText("Showing 1–50 of 130")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    await screen.findByText("Showing 51–100 of 130");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Filter by status" }), "locked");
    await waitFor(() => {
      const [path] = accountCalls().at(-1)!;
      expect(path).toContain("status=locked");
      expect(path).toContain("page=1");
    });
  });

  it("ignores an older response that arrives after the newer one", async () => {
    const pending: { path: string; headers: any; resolve: (v: any) => void }[] = [];
    let n = 0;
    wire(["site_admin"], (path, headers) => {
      if (n++ === 0) return Promise.resolve(pageOf(1, 50, 130));
      return new Promise((resolve) => pending.push({ path, headers, resolve }));
    });
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    expect(await screen.findByText("Showing 1–50 of 130")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: /Next/ })); // page 2, left pending
    await userEvent.type(screen.getByRole("searchbox", { name: "Search client accounts" }), "zed");
    await waitFor(() => expect(pending.some((p) => p.headers && p.path.includes("page=1"))).toBe(true));

    const newest = pending.filter((p) => p.headers).at(-1)!;
    newest.resolve({ ...pageOf(1, 50, 1), data: [{ ...account(1), username: "zed" }] });
    expect(await screen.findByText("zed")).toBeTruthy();
    // The stale page-2 answer lands last and must change nothing.
    pending.find((p) => p.path.includes("page=2"))!.resolve(pageOf(2, 50, 130));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByText("zed")).toBeTruthy();
    expect(screen.queryByText("Showing 51–100 of 130")).toBeNull();
  });
});

describe("Client accounts — generated-password format", () => {
  it("offers only the lengths the server allows, lifts the length to a style's floor, and requires a reason", async () => {
    wire(["site_admin"]);
    put.mockResolvedValue({ password_style: "digits", password_length: 16, config_version: 1 });
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    const user = userEvent.setup();
    render(<Page />);

    // Current format and its history are on the page.
    expect(await screen.findByText("Upper and lower case letters and digits")).toBeTruthy();
    expect(screen.getByTestId("password-format-example").textContent).toMatch(/^[A-HJ-NP-Za-km-z2-9]{14}$/);
    expect(screen.getByText(/keypad kiosk/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /change format/i }));
    const dialog = await screen.findByRole("dialog");
    const lengths = () =>
      within(dialog).getAllByRole("option").map((o) => Number((o as HTMLOptionElement).value));
    expect(lengths()[0]).toBe(7);
    expect(lengths().at(-1)).toBe(32);

    const select = within(dialog).getByRole("combobox", { name: "Length" }) as HTMLSelectElement;
    await user.selectOptions(select, "8"); // legal for mixed...
    await user.click(within(dialog).getByRole("radio", { name: /digits only/i }));
    await waitFor(() => expect(lengths()[0]).toBe(14));
    expect(lengths()).not.toContain(13);
    await waitFor(() => expect(select.value).toBe("14")); // ...and lifted to the digits floor, never left below it
    await user.selectOptions(select, "16");
    expect(within(dialog).getByTestId("password-format-preview").textContent).toMatch(/^[2-9]{16}$/);

    const save = within(dialog).getByRole("button", { name: "Save format" });
    expect(save).toBeDisabled(); // no reason yet
    await user.type(within(dialog).getByPlaceholderText(/keypad/i), "keypad kiosk");
    expect(save).not.toBeDisabled();
    await user.click(save);
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith("/account-password-settings/", {
        password_style: "digits", password_length: 16, reason: "keypad kiosk",
      }),
    );
  });

  it("shows a read-only role the format without a way to change it", async () => {
    wire(["front_office_operator"]);
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    expect(await screen.findByText("Upper and lower case letters and digits")).toBeTruthy();
    expect(screen.getByText(/can see the format but not change it/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change format/i })).toBeNull();
    // The desk still creates accounts.
    expect(screen.getByRole("button", { name: /add account/i })).toBeTruthy();
  });
});
