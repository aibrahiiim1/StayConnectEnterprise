// HOTEL → ROOM CHARGE.
//
// Pinned here: each interface's readiness reads in words, only a FIAS interface is offered the approval and only
// to a site administrator, the approval sends exactly what edged requires (expected_revision_id from the row, the
// fixed RESERVATION posting target, no exponent), a revision conflict reloads the list, and the posting-withheld
// note appears when the room_charge module reports PMS_POSTING_NOT_AUTHORISED.
//
// Phase-0 Amendment A1: there is no folio strategy to choose. The page shows the fixed posting target, asks for
// the vendor's confirmation that reservation numbers are never reused, and lists the vendor-confirmed answer
// meanings (NP/NG/NR/NA/RY) per FIAS interface, which a site administrator confirms or withdraws with evidence,
// a reason and a password.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

// Until the Delivery Owner adds "pms-financial-onboarding" to the role table, a synthetic reader stands in for a
// role that may see the approvals.
vi.mock("@/lib/roles", async (orig) => {
  const actual = await (orig() as Promise<typeof import("@/lib/roles")>);
  return {
    ...actual,
    canRead: (r: string, roles: string[]) => roles.includes("onboarding_reader") || actual.canRead(r, roles),
  };
});

import { api, ApiError } from "@/lib/api";
import RoomChargePage from "@/app/(app)/room-charge/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;

const MEANINGS = [
  { as_status: "NP", effect: "NO_POST_BLOCK", confirmed: false },
  { as_status: "NG", effect: "DATA_SUSPECT_BLOCK", confirmed: false },
  { as_status: "NR", effect: "DATA_SUSPECT_BLOCK", confirmed: false },
  { as_status: "NA", effect: "NO_STAY_BLOCK", confirmed: true, evidence: "Protel support, 2026-09-20, ticket 48213", recorded_at: "2026-09-21T10:00:00Z" },
  { as_status: "RY", effect: "NO_STAY_BLOCK", confirmed: false },
];

const FIAS = {
  pms_interface_id: "if-fias", display_label: "Protel main", connector_kind: "protel-fias", lifecycle_state: "ACTIVE",
  current_revision_id: "rev-7", posting_target_model: "UNSET", financial_base_currency: null,
  financial_base_currency_exponent: null, ready: false, reason: "NOT_ONBOARDED", answer_meanings: MEANINGS,
};
const OTHER = {
  pms_interface_id: "if-mews", display_label: "Mews spa", connector_kind: "mews", lifecycle_state: "ACTIVE",
  current_revision_id: "rev-1", posting_target_model: "UNSET", financial_base_currency: null,
  financial_base_currency_exponent: null, ready: false, reason: "CONNECTOR_NOT_FINANCIAL", answer_meanings: MEANINGS,
};

function routes(roles: string[], readiness: string[] = []) {
  g.mockImplementation((path: string) => {
    switch (path) {
      case "/auth/whoami": return Promise.resolve({ roles });
      case "/pms-financial-onboarding":
        return Promise.resolve({ interfaces: [FIAS, OTHER], posting_target_models: ["RESERVATION"] });
      case "/modules":
        return Promise.resolve({ site_type: "hotel", modules: { room_charge: { id: "room_charge", readiness } } });
    }
    return Promise.reject(new Error(`unexpected GET ${path}`));
  });
}

const ATTESTATION = "Protel support, 2026-09-20, ticket 48213: reservation numbers are never reused.";

async function openApproval() {
  fireEvent.click(await screen.findByRole("button", { name: /approve for room charge: protel main/i }));
  return await screen.findByRole("dialog");
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Hotel → Room charge: who approved", () => {
  const APPROVED = { ...FIAS, approved_at: "2026-09-30T06:07:40Z", approved_by: "2d1fb78c-99c3-45a6-ba9b-e36175c11190",
    posting_target_model: "RESERVATION", ready: true, reason: null };
  function approvedRoutes(operators: "ok" | "denied") {
    g.mockImplementation((path: string) => {
      switch (path) {
        case "/auth/whoami": return Promise.resolve({ roles: ["site_admin"] });
        case "/pms-financial-onboarding": return Promise.resolve({ interfaces: [APPROVED], posting_target_models: ["RESERVATION"] });
        case "/modules": return Promise.resolve({ site_type: "hotel", modules: { room_charge: { id: "room_charge", readiness: [] } } });
        case "/operators":
          return operators === "ok"
            ? Promise.resolve({ data: [{ id: APPROVED.approved_by, email: "fo.manager@hotel.test", display_name: "FO Manager" }] })
            : Promise.reject(new Error("forbidden"));
      }
      return Promise.reject(new Error(`unexpected GET ${path}`));
    });
  }
  it("shows the approving operator's e-mail, never the operator id", async () => {
    approvedRoutes("ok");
    render(<RoomChargePage />);
    expect(await screen.findByText("fo.manager@hotel.test")).toBeInTheDocument();
    expect(screen.queryByText(APPROVED.approved_by)).not.toBeInTheDocument();
  });
  it("says a site administrator approved when the operator directory cannot be read, never the id", async () => {
    approvedRoutes("denied");
    render(<RoomChargePage />);
    expect(await screen.findByText("A site administrator")).toBeInTheDocument();
    expect(screen.queryByText(APPROVED.approved_by)).not.toBeInTheDocument();
  });
});

describe("Hotel → Room charge", () => {
  it("reads each interface's readiness in words and offers approval only for FIAS", async () => {
    routes(["site_admin"]);
    render(<RoomChargePage />);
    expect(await screen.findByText("Protel main")).toBeInTheDocument();
    expect(screen.getByText("Not approved for room charge yet")).toBeInTheDocument();
    expect(screen.getByText("Room charge is supported on FIAS interfaces only")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /approve for room charge/i })).toHaveLength(1);
    expect(screen.getByText("FIAS only")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Posting target" })).toBeInTheDocument();
    expect(screen.getAllByText("Not recorded").length).toBeGreaterThan(0);
    expect(screen.queryByText(/folio identity/i)).not.toBeInTheDocument();
  });

  it("shows the fixed reservation target instead of a folio choice, and sends RESERVATION with the approval", async () => {
    routes(["site_admin"]);
    post.mockResolvedValue({ revision_id: "rev-8" });
    render(<RoomChargePage />);
    const dialog = await openApproval();
    expect(within(dialog).getByText("Reservation (room + reservation number)")).toBeInTheDocument();
    expect(within(dialog).getByText(/protel decides which folio or window/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/room number alone is never sent/i)).toBeInTheDocument();
    expect(within(dialog).queryByRole("radio")).not.toBeInTheDocument();
    expect(within(dialog).getByText(/unique and never reused/i)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText(/^Vendor confirmation/), { target: { value: ATTESTATION } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable room charge" } });
    const submit = screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)!;
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith("/pms-financial-onboarding/if-fias", {
      expected_revision_id: "rev-7",
      posting_target_model: "RESERVATION",
      currency: "USD",
      attestation: ATTESTATION,
      reason: "Enable room charge",
      password: "pw",
    });
    // Reloaded after the approval.
    await waitFor(() => expect(g.mock.calls.filter((c) => c[0] === "/pms-financial-onboarding")).toHaveLength(2));
  });

  it("keeps the approve button disabled for a vendor confirmation shorter than 20 characters", async () => {
    routes(["site_admin"]);
    render(<RoomChargePage />);
    await openApproval();
    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "EUR" } });
    fireEvent.change(screen.getByLabelText(/^Vendor confirmation/), { target: { value: "too short" } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    expect(screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)).toBeDisabled();
  });

  it("reloads and explains a revision conflict", async () => {
    routes(["site_admin"]);
    post.mockRejectedValue(new ApiError(409, { error: "revision_conflict" }));
    render(<RoomChargePage />);
    await openApproval();
    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "EUR" } });
    fireEvent.change(screen.getByLabelText(/^Vendor confirmation/), { target: { value: ATTESTATION } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)!);
    expect(await screen.findByText(/changed since the page was loaded/i)).toBeInTheDocument();
    await waitFor(() => expect(g.mock.calls.filter((c) => c[0] === "/pms-financial-onboarding")).toHaveLength(2));
  });

  it("lists the answer meanings for FIAS interfaces only, with each effect in words and the unknown rule stated", async () => {
    routes(["site_admin"]);
    render(<RoomChargePage />);
    const table = await screen.findByRole("table", { name: "Answer meanings for Protel main" });
    expect(screen.queryByRole("table", { name: /answer meanings for mews spa/i })).not.toBeInTheDocument();
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0].textContent)).toEqual([
      "NPNo-post restriction", "NGGuest not found", "NRRoom not found", "NANight audit in progress", "RYTry again later",
    ]);
    expect(within(rows[0]).getByText(/blocked for the stay until Protel data allows posting again/i)).toBeInTheDocument();
    expect(within(rows[1]).getByText(/marked suspect and a resync is requested/i)).toBeInTheDocument();
    expect(within(rows[3]).getByText(/never retried automatically/i)).toBeInTheDocument();
    expect(within(rows[3]).getByText("Confirmed")).toBeInTheDocument();
    expect(within(rows[3]).getByText(/ticket 48213/)).toBeInTheDocument();
    expect(within(rows[0]).getByText(/not confirmed — unknown/i)).toBeInTheDocument();
    expect(screen.getByText(/UR always, is treated as\s+unknown/i)).toBeInTheDocument();
    expect(screen.getByText(/recording these does not switch posting on or off/i)).toBeInTheDocument();
    // Confirm on the unconfirmed ones, Withdraw on the confirmed one.
    expect(within(rows[0]).getByRole("button", { name: "Confirm NP on Protel main" })).toBeInTheDocument();
    expect(within(rows[3]).getByRole("button", { name: "Withdraw confirmation of NA on Protel main" })).toBeInTheDocument();
  });

  it("confirms an answer meaning with the vendor evidence, a reason and the password", async () => {
    routes(["site_admin"]);
    post.mockResolvedValue({ id: "c1", answer_meanings: MEANINGS });
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm NP on Protel main" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/room charge is blocked for the stay until Protel data allows posting again/i)).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", { name: "Confirm NP" });
    fireEvent.change(within(dialog).getByLabelText(/^Vendor evidence/), { target: { value: "short" } });
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "Vendor answered" } });
    fireEvent.change(within(dialog).getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    // Evidence under 20 characters is refused before it is sent.
    expect(submit).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText(/^Vendor evidence/), {
      target: { value: "Protel support, 2026-09-22, FIAS spec v2.1 section 7.4" },
    });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith("/pms-financial-onboarding/if-fias/answer-confirmations", {
      as_status: "NP",
      action: "CONFIRM",
      evidence: "Protel support, 2026-09-22, FIAS spec v2.1 section 7.4",
      reason: "Vendor answered",
      password: "pw",
    });
    await waitFor(() => expect(g.mock.calls.filter((c) => c[0] === "/pms-financial-onboarding")).toHaveLength(2));
  });

  it("withdraws a confirmation with action WITHDRAW", async () => {
    routes(["site_admin"]);
    post.mockResolvedValue({ id: "c2", answer_meanings: MEANINGS });
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw confirmation of NA on Protel main" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Vendor evidence/), {
      target: { value: "Protel support, 2026-09-25, ticket 48300 retracted" },
    });
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "Vendor retracted" } });
    fireEvent.change(within(dialog).getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Withdraw NA" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][1]).toMatchObject({ as_status: "NA", action: "WITHDRAW" });
  });

  it("is read-only for a role other than site_admin", async () => {
    routes(["onboarding_reader"]);
    render(<RoomChargePage />);
    expect(await screen.findByText("Protel main")).toBeInTheDocument();
    expect(screen.getByText(/site administrator decision/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
    // The answer meanings are visible, but not changeable.
    expect(screen.getByRole("table", { name: "Answer meanings for Protel main" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^confirm np/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /withdraw/i })).not.toBeInTheDocument();
  });

  it("says room charge is not offered when posting is not authorised on this appliance", async () => {
    routes(["site_admin"], ["PMS_POSTING_NOT_AUTHORISED", "NO_ONBOARDED_INTERFACE"]);
    render(<RoomChargePage />);
    expect(await screen.findByText("Room charge is not offered to clients yet")).toBeInTheDocument();
    expect(screen.getByText(/your configuration is saved/i)).toBeInTheDocument();
    expect(screen.getByText(/switch on sending charges to the PMS on this appliance/i)).toBeInTheDocument();
    expect(screen.getByText(/safety switch set when the appliance is installed/i)).toBeInTheDocument();
    expect(screen.getByText("No PMS interface is approved for room charge")).toBeInTheDocument();
  });

  it("tells a role without read access so", async () => {
    routes(["voucher_operator"]);
    render(<RoomChargePage />);
    expect(await screen.findByText(/not available to your role/i)).toBeInTheDocument();
    expect(g).not.toHaveBeenCalledWith("/pms-financial-onboarding");
  });
});

describe("Hotel → Room charge: guest list proof (D48)", () => {
  const WITH_MIRROR = { ...FIAS, approved_at: "2026-09-30T06:07:40Z", approved_by: "op-1", posting_target_model: "RESERVATION",
    ready: true, reason: null,
    financial_mirror: { max_age_seconds: 14400, is_default: true, last_complete_sync_at: new Date(Date.now() - 40 * 60 * 1000).toISOString() } };
  function mirrorRoutes() {
    g.mockImplementation((path: string) => {
      switch (path) {
        case "/auth/whoami": return Promise.resolve({ roles: ["site_admin"] });
        case "/pms-financial-onboarding": return Promise.resolve({ interfaces: [WITH_MIRROR], posting_target_models: ["RESERVATION"] });
        case "/modules": return Promise.resolve({ site_type: "hotel", modules: { room_charge: { id: "room_charge", readiness: [] } } });
      }
      return Promise.reject(new Error(`unexpected GET ${path}`));
    });
  }
  it("shows when Protel last sent the complete guest list, the limit and that a quiet hotel refreshes itself", async () => {
    mirrorRoutes();
    render(<RoomChargePage />);
    const card = await screen.findByTestId("guest-list-proof");
    expect(within(card).getByText("40 min ago")).toBeInTheDocument();
    expect(within(card).getByText("Within the limit")).toBeInTheDocument();
    expect(within(card).getByText(/4 hours/)).toBeInTheDocument();
    expect(within(card).getByText("(default)")).toBeInTheDocument();
    expect(within(card).getByText(/asks Protel for a fresh complete list by itself after 2 hours/)).toBeInTheDocument();
  });
  it("changes the limit in seconds, with a reason and the password", async () => {
    mirrorRoutes();
    post.mockResolvedValue({ max_age_seconds: 7200 });
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: /change the guest list maximum age/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/maximum age in hours/i), { target: { value: "2" } });
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "measured quiet periods" } });
    fireEvent.change(within(dialog).getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(
      "/pms-financial-onboarding/if-fias/financial-mirror-max-age",
      { max_age_seconds: 7200, reason: "measured quiet periods", password: "pw" }));
  });
});
