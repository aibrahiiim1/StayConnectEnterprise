import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// DEVICES (migration 0118): the search goes in a header, a device's detail shows its package and place, and every
// action that changes something states what it does and keeps, and needs a reason before it can be confirmed.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

const PKG = { entitlement_id: "e1", status: "ACTIVE", package_name: "Free Internet", free: true, subject_kind: "ACCOUNT",
  subject_label: "test@example.com", slot: "AUTHORIZED", consumed_data_bytes: 1000, places_in_use: 1, max_devices: 1 };
const ROW = { id: "d1", mac: "aa:bb:cc:dd:ee:01", last_ip: "192.168.77.10", online: false, last_seen: new Date().toISOString(), current: PKG };

function wire() {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/device-settings/") return Promise.resolve({ settings: { client_device_changes_per_day: 3 } });
    if (path.startsWith("/client-devices?")) return Promise.resolve({ data: [ROW], meta: { has_more: false } });
    if (path === "/client-devices/d1") return Promise.resolve({ device: ROW, packages: [PKG], sessions: [],
      restriction: { restricted: false }, remembered_signins: 1, free_packages_used: 1, actions: [] });
    return Promise.resolve({});
  });
}
beforeEach(() => { get.mockReset(); post.mockReset(); wire(); });
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/devices/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText("aa:bb:cc:dd:ee:01");
}

describe("Devices", () => {
  it("lists devices with their package and place, and searches in a header", async () => {
    await renderPage();
    const row = screen.getByText("aa:bb:cc:dd:ee:01").closest("tr") as HTMLElement;
    expect(within(row).getByText("Holds a place")).toBeTruthy();
    expect(within(row).getByText("test@example.com")).toBeTruthy();
    await userEvent.type(screen.getByLabelText("Search devices"), "aa:bb");
    await waitFor(() => expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/client-devices\?page=1$/),
      { "X-Device-Search": encodeURIComponent("aa:bb") }));
  });

  it("explains each action, requires a reason, and says the free-package rule still applies", async () => {
    post.mockResolvedValue({ status: "prepared", places_released: 1 });
    await renderPage();
    await userEvent.click(screen.getByRole("button", { name: /manage/i }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Free package already used on this device")).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: /prepare for a sign-in test/i }));
    expect(within(dialog).getByText(/still be refused on it/)).toBeTruthy();
    const confirm = within(dialog).getByRole("button", { name: /^prepare device$/i });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(within(dialog).getByRole("textbox"), "PRE-LIVE test");
    await userEvent.click(confirm);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/client-devices/d1/prepare-test", { reason: "PRE-LIVE test" }));

    await userEvent.click(within(dialog).getByRole("button", { name: /release this place/i }));
    expect(within(dialog).getByText(/remaining time, data and owner do not change/)).toBeTruthy();
  });
});
