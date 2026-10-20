import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react";

// The assisted workflow (migration 0146): open a request that states whose records these are said to be, review what
// the database made of every record, select only what it allows, and confirm with the password, a reason and the
// request reference. What decides a record is the database's; this page must show it faithfully and send exactly
// the selection and the review signature it was shown.

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: any) => <a href={href} {...p}>{children}</a> }));

const ID = "22222222-2222-4222-8222-222222222222";
const REF = "AR-0A1B2C3D4E";
const stay = { kind: "stay", id: "33333333-3333-4333-8333-333333333333", label: "Room 101 · reservation R-1", detail: "Ada Lovelace", status: "CHECKED_OUT", last_activity: null };

const summary = (over: any = {}) => ({
  id: ID, ref: REF, purpose: "ACCESS", status: "OPEN", range_from: "2026-10-10T00:00:00Z", range_to: "2026-10-19T00:00:00Z",
  requester: "Ms Example", created_by: "operator", created_at: "2026-10-20T08:00:00Z", closed_at: null, closed_by: null, result: null, ...over,
});
const detail = (over: any = {}) => ({
  ...summary(), addresses: ["3c:22:fb:0a:11:7e"], verification_method: "ID_CHECKED_IN_PERSON", evidence_note: "card checked", reason: "asked",
  statements: { identity_verified: true, device_shown: true, sole_use: true, private_address_confirmed: false },
  anchors: [{ kind: "stay", id: stay.id, label: stay.label }], events: [{ at: "2026-10-20T08:00:00Z", by: "operator", action: "OPENED", detail: {} }], ...over,
});
const rec = (kind: string, id: string, cls: string, reason: string, over: any = {}) => ({
  kind, id, at: "2026-10-18T10:00:00Z", class: cls, reason, erase_block: null, selectable: cls === "CLAIMABLE" || cls === "ATTRIBUTABLE", detail: null, ...over,
});
const review = (over: any = {}) => ({
  signature: "0123456789abcdef0123456789abcdef", by_class: { CLAIMABLE: 1, CONFLICTING: 1 },
  records: [
    rec("session", "s1", "CLAIMABLE", "access_of_the_stated_context", { detail: { ip: "10.9.9.9", mac_address: "3c:22:fb:0a:11:7e", bytes_down: 2048 } }),
    rec("session", "s2", "CONFLICTING", "device_used_under_another_access"),
  ], ...over,
});

beforeEach(() => {
  get.mockReset(); post.mockReset();
  get.mockImplementation((path: string) => {
    if (path === "/guest-data-rights/assisted") return Promise.resolve({ requests: [summary()] });
    if (path === `/guest-data-rights/assisted/${ID}`) return Promise.resolve(detail());
    if (path === `/guest-data-rights/assisted/${ID}/review`) return Promise.resolve(review());
    if (path === "/guest-data-rights/search") return Promise.resolve({ matches: [stay] });
    return Promise.resolve({});
  });
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-data/assisted/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText(REF);
}

async function openCase() {
  await renderPage();
  fireEvent.click(screen.getByRole("button", { name: /open request/i }));
  await screen.findByText("Records in this range");
}

function fillDialog(dialog: HTMLElement, typed?: string) {
  fireEvent.change(within(dialog).getByLabelText(/reason/i), { target: { value: "written request" } });
  const pw = within(dialog).queryByLabelText(/password/i);
  if (pw) fireEvent.change(pw, { target: { value: "pw" } });
  if (typed) fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: typed } });
}

describe("Assisted guest data request", () => {
  it("lists the requests and says a device address identifies nobody", async () => {
    await renderPage();
    expect(screen.getByText(/does not identify a person/i)).toBeTruthy();
    expect(screen.getByText("Hand over a copy of the records")).toBeTruthy();
  });

  it("opens a request only when the requester is verified and the records are tied to a reservation or account", async () => {
    post.mockResolvedValue({ id: ID, ref: REF });
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /new request/i }));
    const submit = await screen.findByRole("button", { name: /^open request$/i }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    // A device address alone is not enough.
    fireEvent.change(screen.getByLabelText(/device addresses/i), { target: { value: "3C:22:FB:0A:11:7E" } });
    expect(screen.getByText(/name the reservation, guest or account/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Search for the guest"), { target: { value: "lovelace" } });
    fireEvent.click(screen.getByRole("button", { name: /^search$/i }));
    fireEvent.click(await screen.findByRole("button", { name: /use hotel stay/i }));
    fireEvent.change(screen.getByLabelText(/who is asking/i), { target: { value: "Ms Example" } });
    fireEvent.change(screen.getByLabelText(/evidence you saw/i), { target: { value: "identity card checked against their face" } });
    fireEvent.change(screen.getByLabelText(/^reason/i), { target: { value: "written request received" } });
    expect(submit.disabled).toBe(true); // not yet verified
    fireEvent.click(screen.getByLabelText(/i have verified who the requester is/i));
    fireEvent.click(screen.getByLabelText(/showed me the device/i));
    fireEvent.click(screen.getByLabelText(/nobody else used this device/i));
    expect(submit.disabled).toBe(false);
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/guest-data-rights/assisted");
    expect(body).toMatchObject({
      purpose: "ACCESS", addresses: ["3c:22:fb:0a:11:7e"], anchors: [{ kind: "stay", id: stay.id }],
      requester: "Ms Example", verification_method: "ID_CHECKED_IN_PERSON",
      statements: { identity_verified: true, device_shown: true, sole_use: true, private_address_confirmed: false },
      reason: "written request received",
    });
    await screen.findByText("Records in this range");
  });

  it("asks for the private-address confirmation only for a private address, and refuses a group address", async () => {
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /new request/i }));
    await screen.findByLabelText(/device addresses/i);
    expect(screen.queryByLabelText(/private \(randomised\) address/i)).toBeNull();
    fireEvent.change(screen.getByLabelText(/device addresses/i), { target: { value: "2a:11:22:33:44:55" } });
    expect(screen.getByLabelText(/private \(randomised\) address/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/device addresses/i), { target: { value: "01:00:5e:00:00:01" } });
    expect(screen.getByText(/names no single device/i)).toBeTruthy();
  });

  it("shows every record with its class and lets only the selectable ones be chosen", async () => {
    await openCase();
    expect(screen.getByText("Established on your statements")).toBeTruthy();
    expect(screen.getByText("Conflict — left alone")).toBeTruthy();
    expect(screen.getByText(/also used under another reservation/i)).toBeTruthy();
    expect(screen.getByText(/10\.9\.9\.9/)).toBeTruthy(); // what it would disclose is shown for review
    const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes.map((b) => b.disabled)).toEqual([false, true]);
    const go = screen.getByRole("button", { name: /download 0 selected/i }) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
  });

  it("hands over exactly the selection with the review signature and the request reference", async () => {
    post.mockResolvedValue({ format: "stayconnect.assisted-guest-data-export/1" });
    await openCase();
    fireEvent.click(screen.getByRole("button", { name: /select all that can be selected/i }));
    fireEvent.click(screen.getByRole("button", { name: /download 1 selected record/i }));
    const dialog = await screen.findByRole("dialog");
    const go = within(dialog).getByRole("button", { name: /download records/i }) as HTMLButtonElement;
    fillDialog(dialog);
    expect(go.disabled).toBe(true); // the reference is not typed yet
    fillDialog(dialog, REF);
    fireEvent.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/guest-data-rights/assisted/${ID}/export`, {
      password: "pw", reason: "written request", confirm: REF, signature: "0123456789abcdef0123456789abcdef",
      selected: [{ kind: "session", id: "s1" }],
    }));
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
  });

  it("reloads the review and says so when the records changed after it was shown", async () => {
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(409, { error: "assisted_refused", code: "changed", message: "x" }));
    await openCase();
    fireEvent.click(screen.getByRole("button", { name: /select all that can be selected/i }));
    fireEvent.click(screen.getByRole("button", { name: /download 1 selected record/i }));
    const dialog = await screen.findByRole("dialog");
    fillDialog(dialog, REF);
    const reviewsBefore = get.mock.calls.filter((c) => String(c[0]).endsWith("/review")).length;
    fireEvent.click(within(dialog).getByRole("button", { name: /download records/i }));
    await within(dialog).findByText(/the records changed after you reviewed them/i);
    await waitFor(() => expect(get.mock.calls.filter((c) => String(c[0]).endsWith("/review")).length).toBe(reviewsBefore + 1));
  });

  it("anonymises with the danger confirmation and offers the request file once", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/guest-data-rights/assisted") return Promise.resolve({ requests: [summary({ purpose: "ERASURE" })] });
      if (path === `/guest-data-rights/assisted/${ID}`) return Promise.resolve(detail({ purpose: "ERASURE" }));
      if (path === `/guest-data-rights/assisted/${ID}/review`) return Promise.resolve(review({
        records: [rec("session", "s1", "CLAIMABLE", "access_of_the_stated_context", { erase_block: "recent", selectable: false }),
                  rec("session", "s3", "CLAIMABLE", "access_of_the_stated_context")] }));
      return Promise.resolve({});
    });
    post.mockResolvedValue({ erasure_ref: "ER-0A1B2C3D4E5F", counts: { sessions: 1 }, receipt: { reference: REF } });
    await openCase();
    expect(screen.getByText(/less than a day old/i)).toBeTruthy(); // the one that cannot be erased yet says why
    const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes.map((b) => b.disabled)).toEqual([true, false]);
    fireEvent.click(boxes[1]);
    fireEvent.click(screen.getByRole("button", { name: /anonymise 1 selected record/i }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/cannot be undone/i)).toBeTruthy();
    fillDialog(dialog, REF);
    fireEvent.click(within(dialog).getByRole("button", { name: /anonymise records/i }));
    expect((await screen.findAllByText(/ER-0A1B2C3D4E5F/)).length).toBeGreaterThan(0);
    expect(post).toHaveBeenCalledWith(`/guest-data-rights/assisted/${ID}/erase`,
      { password: "pw", reason: "written request", confirm: REF, signature: "0123456789abcdef0123456789abcdef", selected: [{ kind: "session", id: "s3" }] });
    expect(screen.getByRole("button", { name: /save the request file/i })).toBeTruthy();
  });

  it("shows a closed request without its addresses or notes", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/guest-data-rights/assisted") return Promise.resolve({ requests: [summary({ status: "COMPLETED" })] });
      if (path === `/guest-data-rights/assisted/${ID}`) return Promise.resolve(detail({
        status: "COMPLETED", addresses: null, evidence_note: null, requester: "[removed]", anchors: [],
        closed_at: "2026-10-20T09:00:00Z", closed_by: "operator", result: { exports: 1, records_disclosed: 2 } }));
      return Promise.resolve({});
    });
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /open request/i }));
    await screen.findByText(/the addresses and notes were removed when it closed/i);
    expect(screen.queryByText(/device addresses:/i)).toBeNull();
    expect(screen.queryByText("Records in this range")).toBeNull();
    expect(get.mock.calls.some((c) => String(c[0]).endsWith("/review"))).toBe(false);
  });
});
