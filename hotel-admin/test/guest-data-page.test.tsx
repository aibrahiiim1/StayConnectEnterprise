import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react";

// GDPR C3 and C4: find a guest, export their data or erase them -- each with a reason and the password, a refusal
// that says why in plain words, and the erasure reference to give the guest.

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

const account = {
  kind: "account", id: "11111111-1111-4111-8111-111111111111", label: "guest@example.com",
  detail: "Ada Lovelace", status: "disabled", last_activity: "2026-01-01T10:00:00Z",
};

beforeEach(() => {
  get.mockReset(); post.mockReset();
  get.mockImplementation((path: string) => {
    if (path === "/guest-data-rights/erasures") return Promise.resolve({ erasures: [] });
    if (path === "/guest-data-rights/search") return Promise.resolve({ matches: [account] });
    return Promise.resolve({});
  });
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-data/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  fireEvent.change(screen.getByLabelText("Search"), { target: { value: "guest@example" } });
  fireEvent.click(screen.getByRole("button", { name: /search/i }));
  await screen.findByText("guest@example.com");
}

function fillDialog(dialog: HTMLElement, typed?: string) {
  fireEvent.change(within(dialog).getByLabelText(/reason/i), { target: { value: "written request" } });
  fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
  if (typed) fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: typed } });
}

describe("Guest data requests", () => {
  it("searches with the typed term", async () => {
    await renderPage();
    // The search travels in a header, never in the URL (GDPR final check).
    expect(get).toHaveBeenCalledWith("/guest-data-rights/search", { "X-Guest-Data-Search": "guest%40example" });
    expect(screen.getByText("Client account")).toBeTruthy();
  });

  it("exports the chosen record with a reason and the password", async () => {
    post.mockResolvedValue({ format: "stayconnect.guest-data-export/2" });
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /export/i }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog);
    fireEvent.click(within(dialog).getByRole("button", { name: /download export/i }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/guest-data-rights/export",
      { kind: "account", id: account.id, reason: "written request", password: "pw" }));
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
  });

  it("explains a refused erasure in plain words and erases nothing", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(409, { error: "guest_data_live", live: ["pms_open", "online"], message: "x" }));
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^erase /i }));
    const dialog = await screen.findByRole("dialog");
    const go = within(dialog).getByRole("button", { name: /erase guest/i }) as HTMLButtonElement;
    fillDialog(dialog);
    expect(go.disabled).toBe(true); // ERASE not typed yet
    fillDialog(dialog, "ERASE");
    fireEvent.click(go);
    await within(dialog).findByText(/the PMS still shows this stay as reserved or in-house/);
    expect(within(dialog).getByText(/device is online right now on this record/)).toBeTruthy();
  });

  it("shows the erasure reference to give the guest", async () => {
    post.mockResolvedValue({ erasure_ref: "ER-0A1B2C3D4E5F", counts: { client_accounts: 1, devices: 2 } });
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^erase /i }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog, "ERASE");
    fireEvent.click(within(dialog).getByRole("button", { name: /erase guest/i }));
    await screen.findByText("ER-0A1B2C3D4E5F");
    expect(post).toHaveBeenCalledWith("/guest-data-rights/erase",
      { kind: "account", id: account.id, reason: "written request", password: "pw" });
    expect(screen.queryByText("guest@example.com")).toBeNull();
  });
});
