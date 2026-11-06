import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react";

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
    fireEvent.keyDown(screen.getByRole("button", { name: /^Actions for Client account/ }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Erase/ }));
    const dialog = await screen.findByRole("dialog");
    const go = within(dialog).getByRole("button", { name: /erase guest/i }) as HTMLButtonElement;
    fillDialog(dialog);
    expect(go.disabled).toBe(true); // ERASE not typed yet
    fillDialog(dialog, "ERASE");
    fireEvent.click(go);
    await within(dialog).findByText(/the PMS still shows this stay as reserved or in-house/);
    expect(within(dialog).getByText(/a device is online on access that is not only this guest/)).toBeTruthy();
  });

  it("shows the erasure reference to give the guest", async () => {
    post.mockResolvedValue({ erasure_ref: "ER-0A1B2C3D4E5F", counts: { client_accounts: 1, devices: 2 } });
    await renderPage();
    fireEvent.keyDown(screen.getByRole("button", { name: /^Actions for Client account/ }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Erase/ }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog, "ERASE");
    fireEvent.click(within(dialog).getByRole("button", { name: /erase guest/i }));
    await screen.findByText("ER-0A1B2C3D4E5F");
    expect(post).toHaveBeenCalledWith("/guest-data-rights/erase",
      { kind: "account", id: account.id, reason: "written request", password: "pw" });
    expect(screen.queryByText("guest@example.com")).toBeNull();
  });

  it("says when the erasure ended the guest's own access", async () => {
    post.mockResolvedValue({ erasure_ref: "ER-0A1B2C3D4E60", counts: { client_accounts: 1 }, packages_ended: 1, sessions_ended: 2, devices_withdrawn: 0 });
    await renderPage();
    fireEvent.keyDown(screen.getByRole("button", { name: /^Actions for Client account/ }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Erase/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Their own internet access ends now and those devices are disconnected/)).toBeTruthy();
    fillDialog(dialog, "ERASE");
    fireEvent.click(within(dialog).getByRole("button", { name: /erase guest/i }));
    await screen.findByText(/Their access was ended \(1 package, 2 device sessions\)\. The disconnection is not confirmed yet/);
  });

  it("puts what was anonymised in words and lets the notice be dismissed", async () => {
    post.mockResolvedValue({ erasure_ref: "ER-0A1B2C3D4E61", counts: { client_accounts: 1, sign_in_attempts: 3 } });
    await renderPage();
    fireEvent.keyDown(screen.getByRole("button", { name: /^Actions for Client account/ }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Erase/ }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog, "ERASE");
    fireEvent.click(within(dialog).getByRole("button", { name: /erase guest/i }));
    await screen.findByText(/Anonymised: 1 client account, 3 sign-in attempts\./);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(screen.queryByText(/ER-0A1B2C3D4E61/)).toBeNull());
  });

  it("says a refused password plainly and keeps the results after an export", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValueOnce(new ApiError(401, { error: "reauth_required", message: "Confirm your password. This action needs it." }));
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /export/i }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog);
    fireEvent.click(within(dialog).getByRole("button", { name: /download export/i }));
    await within(dialog).findByText("Your password was not accepted. Nothing was changed.");
    post.mockResolvedValueOnce({ format: "stayconnect.guest-data-export/2" });
    fireEvent.click(within(dialog).getByRole("button", { name: /download export/i }));
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
    expect(screen.getByText("guest@example.com")).toBeTruthy();
  });

  it("shows the record status in words", async () => {
    await renderPage();
    expect(screen.getByText("Disabled")).toBeTruthy();
  });

  it("offers Try again when the erasures cannot be read, instead of saying there are none", async () => {
    const { ApiError } = await import("@/lib/api");
    get.mockImplementation((path: string) => path === "/guest-data-rights/erasures"
      ? Promise.reject(new ApiError(500, { error: "internal", message: "the erasures could not be read" }))
      : Promise.resolve({}));
    const { default: Page } = await import("@/app/(app)/guest-data/page");
    const { ToastProvider } = await import("@/components/ui/toast");
    render(<ToastProvider><Page /></ToastProvider>);
    expect(await screen.findByText("The erasures could not be loaded")).toBeTruthy();
    expect(screen.queryByText("No guest has been erased.")).toBeNull();
    get.mockImplementation(() => Promise.resolve({ erasures: [] }));
    fireEvent.click(screen.getByRole("button", { name: /Try again/ }));
    expect(await screen.findByText("No guest has been erased.")).toBeTruthy();
  });
});

describe("Guest data words", () => {
  it("puts the appliance's refusals, counts and statuses in words", async () => {
    const { ApiError } = await import("@/lib/api");
    const { guestDataError, countWords, statusWords, formatDay, REASON_NAMES_GUEST } = await import("@/lib/guest-data-errors");
    expect(guestDataError(new ApiError(400, { error: "invalid", message: "the reason must not name the guest: leave out their name" }), "x"))
      .toBe(REASON_NAMES_GUEST);
    expect(guestDataError(new ApiError(500, { error: "internal", message: "the export failed" }), "Fallback.")).toBe("Fallback.");
    expect(guestDataError(new ApiError(409, { error: "guest_data_live", live: ["mystery"], message: "x" }), "x", "Nothing was erased"))
      .toBe("Nothing was erased: the guest's records are still in use.");
    expect(guestDataError(new ApiError(409, { error: "register_refused", code: "closed", message: "This request is already answered." }), "x"))
      .toBe("This request is already answered. Nothing was changed.");
    expect(countWords({ sessions: 1, devices: 2, sign_in_passes: 0 })).toBe("1 internet session, 2 devices");
    expect(countWords({})).toBe("");
    expect(statusWords("IN_HOUSE")).toBe("In-house");
    expect(statusWords("CHECKED_OUT")).toBe("Checked out");
    expect(formatDay("2026-11-01")).not.toContain("2026-11-01");
  });
});
