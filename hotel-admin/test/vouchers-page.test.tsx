import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// THE VOUCHER SCREEN — what an operator sees and what it sends.
//
// Every assertion here is about a defect the Product Owner reported or a security property the redesign had
// to keep: step-up dialogs that rendered off-screen after a 200-row table, an "expired" filter that could
// never match, a batch export labelled with the wrong number, a code format that saved on a dropdown change,
// raw ISO timestamps, and a raw UUID box when the package list failed.

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      put: (...a: any[]) => put(...a),
    },
  };
});

import { VouchersView } from "@/components/vouchers/vouchers-view";
import { effectiveStatus, formatPrice, canCancel } from "@/lib/api/vouchers";

const DAY = 86_400_000;
const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
const now = Date.now();

const PKG = "11111111-1111-4111-8111-111111111111";
const BATCH = "22222222-2222-4222-8222-222222222222";

function row(id: string, last4: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    code_last4: last4,
    state: "UNUSED",
    package_revision_id: PKG,
    package_name: "One day",
    package_revision_no: 2,
    batch_id: BATCH,
    created_at: iso(now - 3 * DAY),
    redemption_valid_from: null,
    redemption_valid_until: null,
    notes: null,
    issued_by: "33333333-3333-4333-8333-333333333333",
    issued_by_label: "Front desk",
    ...extra,
  };
}

// No effective_state on the expired / future rows on purpose: the screen must derive it from the window
// when the server does not send it, with the same rule the authenticator uses.
const ROWS = [
  row("a0000000-0000-4000-8000-000000000001", "AV11", { effective_state: "available" }),
  row("a0000000-0000-4000-8000-000000000002", "EX22", { redemption_valid_until: iso(now - DAY) }),
  row("a0000000-0000-4000-8000-000000000003", "NY33", { redemption_valid_from: iso(now + 2 * DAY) }),
  row("a0000000-0000-4000-8000-000000000004", "US44", { state: "REDEEMED", effective_state: "redeemed" }),
];

const SUMMARY = {
  unused: 60, redeemed: 30, revoked: 5, redemption_expired: 0,
  available: 50, expired_unused: 7, not_yet_valid: 3, total: 95, issued_last_7_days: 12, batches: 2,
};

const BATCH_ROW = {
  batch_id: BATCH, package_revision_id: PKG, package_name: "One day", package_revision_no: 2,
  count: 120, redeemed: 30, cancelled: 5, available: 70, expired: 12, not_yet_valid: 3, unused: 85,
  created_at: iso(now - 3 * DAY), issued_by: "33333333-3333-4333-8333-333333333333", issued_by_label: "Front desk",
  notes: "Conference", redemption_valid_from: null, redemption_valid_until: iso(now + 30 * DAY),
};

const GRANTABLE = [
  { id: PKG, package_code: "DAY", revision_no: 2, package_type: "ONE_DAY", name: "One day", price_minor: 1500, currency: "EUR", currency_exponent: 2 },
];

function wire(over: Partial<Record<string, () => Promise<unknown>>> = {}) {
  get.mockImplementation((raw?: unknown) => {
    const path = typeof raw === "string" ? raw : "";
    for (const [prefix, fn] of Object.entries(over)) if (path.startsWith(prefix)) return fn!();
    if (path.startsWith("/vouchers/summary")) return Promise.resolve(SUMMARY);
    if (path.startsWith("/vouchers/?")) return Promise.resolve({ vouchers: ROWS, total: 95 });
    if (path.startsWith("/vouchers/batches")) return Promise.resolve({ batches: [BATCH_ROW], total: 1, unbatched_vouchers: 0 });
    if (path.startsWith("/vouchers/grantable")) return Promise.resolve({ revisions: GRANTABLE });
    if (/^\/vouchers\/[^/]+\/history$/.test(path))
      return Promise.resolve({ voucher_id: "x", entitlements: [], redemption_available: true, cancelled: null, cancellation_available: true });
    if (path === "/voucher-codes/reveals")
      return Promise.resolve({
        reveals: [
          { revealed_at: iso(now - 3_600_000), action: "EXPORT", voucher_id: null, voucher_count: 120, operator_label: "desk@hotel", reason: "reprint", selection: JSON.stringify({ batch_id: BATCH, state: "" }) },
        ],
      });
    if (path === "/voucher-code-settings/") return Promise.resolve({ code_mode: "numbers", code_length: 8, config_version: 2, updated_at: iso(now - 10 * DAY) });
    if (path === "/voucher-code-settings/changes")
      return Promise.resolve({
        changes: [
          { changed_at: iso(now - 10 * DAY), changed_by: "it@hotel", change_reason: "keypad", old_code_mode: "mixed", old_code_length: 6, new_code_mode: "numbers", new_code_length: 8, new_config_version: 2 },
        ],
      });
    if (path === "/voucher-code-settings/key-generations")
      return Promise.resolve({ generations: [{ id: "g1", generation_no: 1, superseded_at: null, supersede_reason: null, vouchers: 95, unused_vouchers: 60, active: true }] });
    return Promise.resolve({});
  });
}

const ALL = { canIssue: true, canRevealCodes: true, canReadFormat: true, canEditFormat: true };

function renderView(props: Partial<typeof ALL> = {}) {
  const utils = render(<VouchersView {...ALL} {...props} />);
  return { ...utils, user: userEvent.setup({ pointerEventsCheck: 0 }) };
}

const RAW_ISO = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/;

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
});

describe("effective status — what a card IS, not what the row says", () => {
  it("derives expired and not-yet-valid from the validity window", () => {
    expect(effectiveStatus({ state: "UNUSED", redemption_valid_from: null, redemption_valid_until: iso(now - 1000) })).toBe("expired");
    expect(effectiveStatus({ state: "UNUSED", redemption_valid_from: iso(now + DAY), redemption_valid_until: null })).toBe("not_yet_valid");
    expect(effectiveStatus({ state: "UNUSED", redemption_valid_from: iso(now - DAY), redemption_valid_until: iso(now + DAY) })).toBe("available");
    expect(effectiveStatus({ state: "REDEMPTION_EXPIRED", redemption_valid_from: null, redemption_valid_until: null })).toBe("expired");
    // The server's own answer wins when it is present.
    expect(effectiveStatus({ state: "UNUSED", redemption_valid_from: null, redemption_valid_until: null, effective_state: "expired" })).toBe("expired");
    expect(canCancel("expired")).toBe(false);
    expect(canCancel("redeemed")).toBe(false);
    expect(canCancel("cancelled")).toBe(false);
    expect(canCancel("available")).toBe(true);
    expect(canCancel("not_yet_valid")).toBe(true);
  });

  it("writes a price from MINOR units in its currency", () => {
    expect(formatPrice(1500, "EUR", 2)).toMatch(/15\.00/);
    expect(formatPrice(1500, "JPY", 0)).toMatch(/1,?500/);
    expect(formatPrice(0, "EUR", 2)).toBe("Free");
  });

  it("shows the effective status and filter counts, and filters on the server by effective status", async () => {
    wire();
    const { user } = renderView();
    expect(await screen.findByText("•••• EX22")).toBeTruthy();

    const table = screen.getByRole("table");
    expect(within(table).getByText("Expired (never used)")).toBeTruthy();
    expect(within(table).getByText("Not yet valid")).toBeTruthy();
    expect(within(table).getByText("Available")).toBeTruthy();
    expect(within(table).getByText("Used")).toBeTruthy();

    const chips = screen.getByRole("radiogroup", { name: "Status" });
    const expiredChip = within(chips).getByRole("radio", { name: /Expired \(never used\)/ });
    expect(expiredChip.textContent).toContain("7");
    expect(within(chips).getByRole("radio", { name: /^All/ }).textContent).toContain("95");
    expect(within(chips).getByRole("radio", { name: /Not yet valid/ }).textContent).toContain("3");

    await user.click(expiredChip);
    await waitFor(() => expect(get.mock.calls.some((c) => String(c[0]).startsWith("/vouchers/?") && String(c[0]).includes("effective=expired"))).toBe(true));
    // The filter never asks for the stored state nothing writes.
    expect(get.mock.calls.some((c) => String(c[0]).includes("state=REDEMPTION_EXPIRED"))).toBe(false);
  });

  it("searches by the last four characters on the server", async () => {
    wire();
    const { user } = renderView();
    await screen.findByText("•••• AV11");
    await user.type(screen.getByLabelText("Search by the last 4 characters of a code"), "ex22");
    await waitFor(() => expect(get.mock.calls.some((c) => String(c[0]).includes("last4=EX22"))).toBe(true), { timeout: 2000 });
  });
});

describe("step-up dialogs are real modals", () => {
  it("opens the card sheet and the cancel confirmation in a portal, as dialogs", async () => {
    wire();
    const { user, container } = renderView();
    await user.click(await screen.findByText("•••• AV11"));

    const sheet = await screen.findByRole("dialog", { name: /AV11/ });
    expect(container.contains(sheet)).toBe(false); // portalled, not after the table
    await user.click(within(sheet).getByRole("button", { name: /Cancel card/ }));

    const confirm = await screen.findByRole("dialog", { name: /Cancel card •••• AV11/ });
    expect(container.contains(confirm)).toBe(false);
    expect(within(confirm).getByLabelText(/Reason/)).toBeTruthy();
    const pw = within(confirm).getByLabelText(/password/i) as HTMLInputElement;
    expect(pw.type).toBe("password");

    await user.type(within(confirm).getByLabelText(/Reason/), "Card reported lost");
    await user.type(pw, "secret");
    post.mockResolvedValueOnce({ voucher_id: ROWS[0].id, state: "REVOKED" });
    await user.click(within(confirm).getByRole("button", { name: "Cancel the card" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/vouchers/${ROWS[0].id}/revoke`, { password: "secret", reason: "Card reported lost" }));
  });

  it("does not offer to cancel an expired or a used card", async () => {
    wire();
    const { user } = renderView();
    await user.click(await screen.findByText("•••• EX22"));
    let sheet = await screen.findByRole("dialog", { name: /EX22/ });
    expect((within(sheet).getByRole("button", { name: /Cancel card/ }) as HTMLButtonElement).disabled).toBe(true);
    // One Escape closes the sheet: its first focusable element must not be a tooltip that swallows it.
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /EX22/ })).toBeNull());

    await user.click(screen.getByText("•••• US44"));
    sheet = await screen.findByRole("dialog", { name: /US44/ });
    expect((within(sheet).getByRole("button", { name: /Cancel card/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("reveals a code only through the step-up, and drops it when the sheet closes", async () => {
    wire();
    const { user } = renderView();
    await user.click(await screen.findByText("•••• AV11"));
    const sheet = await screen.findByRole("dialog", { name: /AV11/ });
    await user.click(within(sheet).getByRole("button", { name: /Show full code/ }));
    const confirm = await screen.findByRole("dialog", { name: /Show the full code/ });
    await user.type(within(confirm).getByLabelText(/Reason/), "Guest at desk");
    await user.type(within(confirm).getByLabelText(/password/i), "pw");
    post.mockResolvedValueOnce({ voucher_id: ROWS[0].id, code: "4827AV11" });
    await user.click(within(confirm).getByRole("button", { name: "Show code" }));
    expect(post).toHaveBeenCalledWith(`/voucher-codes/${ROWS[0].id}/reveal`, { password: "pw", reason: "Guest at desk" });
    expect(await screen.findByTestId("revealed-code")).toHaveTextContent("4827AV11");

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("revealed-code")).toBeNull());
    await user.click(screen.getByText("•••• AV11"));
    await screen.findByRole("dialog", { name: /AV11/ });
    expect(screen.queryByText("4827AV11")).toBeNull();
  });

  it("refuses a too-short reason before sending anything", async () => {
    wire();
    const { user } = renderView();
    await user.click(await screen.findByText("•••• AV11"));
    const sheet = await screen.findByRole("dialog", { name: /AV11/ });
    await user.click(within(sheet).getByRole("button", { name: /Show full code/ }));
    const confirm = await screen.findByRole("dialog", { name: /Show the full code/ });
    await user.type(within(confirm).getByLabelText(/Reason/), "no");
    await user.type(within(confirm).getByLabelText(/password/i), "pw");
    await user.click(within(confirm).getByRole("button", { name: "Show code" }));
    expect(await within(confirm).findByText(/at least 4 characters/)).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
  });
});

describe("issuing vouchers", () => {
  it("sends exactly the fields the API takes, then shows the codes once and drops them on close", async () => {
    wire();
    const { user } = renderView();
    await user.click(await screen.findByRole("button", { name: /Issue vouchers/ }));
    const dlg = await screen.findByRole("dialog", { name: "Issue vouchers" });

    // Price is written from minor units with its currency.
    expect(within(dlg).getByText(/15\.00/)).toBeTruthy();
    await user.click(within(dlg).getByText("One day"));
    await user.click(within(dlg).getByRole("button", { name: "Next" }));

    const count = within(dlg).getByLabelText(/How many cards/) as HTMLInputElement;
    await user.clear(count);
    await user.type(count, "25");
    const until = within(dlg).getByLabelText(/Valid until/) as HTMLInputElement;
    const untilLocal = new Date(now + 7 * DAY);
    const pad = (n: number) => String(n).padStart(2, "0");
    const local = `${untilLocal.getFullYear()}-${pad(untilLocal.getMonth() + 1)}-${pad(untilLocal.getDate())}T10:00`;
    await user.type(until, local);
    await user.type(within(dlg).getByLabelText(/Note/), "Conference desk");
    await user.click(within(dlg).getByRole("button", { name: "Review" }));

    post.mockResolvedValueOnce({ count: 25, batch_id: BATCH, codes: ["48273962", "K7PX4R9M"] });
    await user.click(within(dlg).getByRole("button", { name: "Issue 25 vouchers" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/vouchers/issue");
    expect(body).toEqual({
      package_revision_id: PKG,
      count: 25,
      note: "Conference desk",
      valid_until: new Date(local).toISOString(),
    });
    expect("valid_from" in body).toBe(false);

    const codes = await screen.findAllByTestId("held-code");
    expect(codes.map((c) => c.textContent)).toEqual(["48273962", "K7PX4R9M"]);
    expect(screen.getByText(/not be shown again/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Copy all/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Download CSV/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Print cards/ })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByTestId("held-code")).toBeNull());
    await user.click(screen.getByRole("button", { name: /Issue vouchers/ }));
    await screen.findByRole("dialog", { name: "Issue vouchers" });
    expect(screen.queryByText("48273962")).toBeNull();
  });

  it("never asks for a raw package id when the package list fails; it offers a retry", async () => {
    let fail = true;
    wire({ "/vouchers/grantable": () => (fail ? Promise.reject(new Error("package list unavailable")) : Promise.resolve({ revisions: GRANTABLE })) });
    const { user } = renderView();
    await user.click(await screen.findByRole("button", { name: /Issue vouchers/ }));
    const dlg = await screen.findByRole("dialog", { name: "Issue vouchers" });
    expect(await within(dlg).findByText(/package list unavailable/)).toBeTruthy();
    expect(within(dlg).queryAllByRole("textbox")).toHaveLength(0);
    expect(document.querySelector('input[placeholder*="id" i]')).toBeNull();

    fail = false;
    await user.click(within(dlg).getByRole("button", { name: /Try again/ }));
    expect(await within(dlg).findByText("One day")).toBeTruthy();
  });
});

describe("batches", () => {
  it("states the true number of codes an export will recover, and sends the selection", async () => {
    wire();
    const { user } = renderView();
    await screen.findByText("•••• AV11");
    await user.click(screen.getByRole("tab", { name: "Batches" }));
    await user.click(await screen.findByText("120"));
    const sheet = await screen.findByRole("dialog", { name: /Batch|·/ });
    await user.click(within(sheet).getByRole("button", { name: /Export codes/ }));

    const confirm = await screen.findByRole("dialog", { name: /Export the codes/ });
    expect(within(confirm).getByText(/120 codes will be recovered/)).toBeTruthy();
    await user.click(within(confirm).getByRole("radio", { name: /Not used yet/ }));
    expect(within(confirm).getByText(/85 codes will be recovered/)).toBeTruthy();

    await user.type(within(confirm).getByLabelText(/Reason/), "Reprinting");
    await user.type(within(confirm).getByLabelText(/password/i), "pw");
    post.mockResolvedValueOnce({ batch_id: BATCH, count: 2, vouchers: [{ id: "v1", code: "48273962" }, { id: "v2", code: "K7PX4R9M" }] });
    await user.click(within(confirm).getByRole("button", { name: "Export 85 codes" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/voucher-codes/export", { password: "pw", reason: "Reprinting", batch_id: BATCH, state: "UNUSED" }),
    );
    expect((await screen.findAllByTestId("held-code")).length).toBe(2);
  });
});

describe("code format", () => {
  it("changes only after a preview, a reason and a confirmation — never on a dropdown change", async () => {
    wire();
    put.mockResolvedValue({ code_mode: "mixed", code_length: 8, config_version: 3 });
    const { user } = renderView();
    await screen.findByText("•••• AV11");
    await user.click(screen.getByRole("tab", { name: "Code security" }));
    await user.click(await screen.findByRole("button", { name: /Change format/ }));
    const dlg = await screen.findByRole("dialog", { name: /Change the code format/ });

    await user.click(within(dlg).getByText("Letters and digits"));
    await user.selectOptions(within(dlg).getByLabelText("Length"), "7");
    expect(put).not.toHaveBeenCalled();
    expect(within(dlg).getByTestId("format-preview")).toHaveTextContent("K7PX4R9");

    const review = within(dlg).getByRole("button", { name: "Review change" }) as HTMLButtonElement;
    expect(review.disabled).toBe(true);
    await user.type(within(dlg).getByLabelText(/Reason for the change/), "Harder to guess");
    await user.click(review);
    expect(put).not.toHaveBeenCalled();

    const confirm = await screen.findByRole("dialog", { name: /Confirm the new code format/ });
    await user.click(within(confirm).getByRole("button", { name: "Apply to new batches" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith("/voucher-code-settings/", { code_mode: "mixed", code_length: 7, reason: "Harder to guess" }),
    );
  });

  it("shows the format history", async () => {
    wire();
    const { user } = renderView();
    await screen.findByText("•••• AV11");
    await user.click(screen.getByRole("tab", { name: "Code security" }));
    expect(await screen.findByText("6 letters and digits → 8 digits")).toBeTruthy();
    expect(screen.getByText(/keypad/)).toBeTruthy();
  });
});

describe("no raw timestamps anywhere", () => {
  it("renders every tab without an ISO timestamp in the visible text", async () => {
    wire();
    const { user } = renderView();
    await screen.findByText("•••• AV11");
    expect(document.body.textContent).not.toMatch(RAW_ISO);

    await user.click(screen.getByRole("tab", { name: "Batches" }));
    await screen.findByText("120");
    expect(document.body.textContent).not.toMatch(RAW_ISO);

    await user.click(screen.getByRole("tab", { name: "Access log" }));
    await screen.findByText("reprint");
    expect(document.body.textContent).not.toMatch(RAW_ISO);

    await user.click(screen.getByRole("tab", { name: "Code security" }));
    await screen.findByText("Generation 1");
    expect(document.body.textContent).not.toMatch(RAW_ISO);
  });
});

describe("permissions decide what is offered", () => {
  it("a read-only role sees no issue, reveal, export or cancel controls, and never asks for the reveal log", async () => {
    wire();
    const { user } = renderView({ canIssue: false, canRevealCodes: false, canReadFormat: false, canEditFormat: false });
    await user.click(await screen.findByText("•••• AV11"));
    const sheet = await screen.findByRole("dialog", { name: /AV11/ });
    expect(within(sheet).queryByRole("button", { name: /Show full code/ })).toBeNull();
    expect(within(sheet).queryByRole("button", { name: /Cancel card/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Issue vouchers/ })).toBeNull();
    expect(screen.queryByRole("tab", { name: "Access log" })).toBeNull();
    expect(get.mock.calls.some((c) => String(c[0]) === "/voucher-codes/reveals")).toBe(false);
  });
});
