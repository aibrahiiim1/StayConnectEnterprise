// HOTEL → ROOM CHARGE.
//
// Pinned here: each interface's readiness reads in words, only a FIAS interface is offered the approval and only
// to a site administrator, the approval sends exactly what edged requires (expected_revision_id from the row,
// no exponent), a revision conflict reloads the list, and the posting-withheld note appears when the room_charge
// module reports PMS_POSTING_NOT_AUTHORISED.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
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

const FIAS = {
  pms_interface_id: "if-fias", display_label: "Protel main", connector_kind: "protel-fias", lifecycle_state: "ACTIVE",
  current_revision_id: "rev-7", folio_identity_strategy: null, financial_base_currency: null,
  financial_base_currency_exponent: null, ready: false, reason: "NOT_ONBOARDED",
};
const OTHER = {
  pms_interface_id: "if-mews", display_label: "Mews spa", connector_kind: "mews", lifecycle_state: "ACTIVE",
  current_revision_id: "rev-1", folio_identity_strategy: null, financial_base_currency: null,
  financial_base_currency_exponent: null, ready: false, reason: "CONNECTOR_NOT_FINANCIAL",
};
const STRATEGIES = ["GLOBALLY_UNIQUE", "UNIQUE_PER_STAY", "REUSED_SEQUENTIAL"];

function routes(roles: string[], readiness: string[] = []) {
  g.mockImplementation((path: string) => {
    switch (path) {
      case "/auth/whoami": return Promise.resolve({ roles });
      case "/pms-financial-onboarding": return Promise.resolve({ interfaces: [FIAS, OTHER], strategies: STRATEGIES });
      case "/modules":
        return Promise.resolve({ site_type: "hotel", modules: { room_charge: { id: "room_charge", readiness } } });
    }
    return Promise.reject(new Error(`unexpected GET ${path}`));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("Hotel → Room charge", () => {
  it("reads each interface's readiness in words and offers approval only for FIAS", async () => {
    routes(["site_admin"]);
    render(<RoomChargePage />);
    expect(await screen.findByText("Protel main")).toBeInTheDocument();
    expect(screen.getByText("Not approved for room charge yet")).toBeInTheDocument();
    expect(screen.getByText("Room charge is supported on FIAS interfaces only")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /approve for room charge/i })).toHaveLength(1);
    expect(screen.getByText("FIAS only")).toBeInTheDocument();
  });

  it("sends the approval with the row's revision, the strategy, the currency, attestation, reason and password", async () => {
    routes(["site_admin"]);
    post.mockResolvedValue({ revision_id: "rev-8" });
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: /approve for room charge: protel main/i }));
    fireEvent.click(await screen.findByRole("radio", { name: /reused sequentially/i }));
    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText(/^Attestation/), { target: { value: "Folio numbers restart every night; observed on 3 stays." } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable room charge" } });
    const submit = screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)!;
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith("/pms-financial-onboarding/if-fias", {
      expected_revision_id: "rev-7",
      folio_identity_strategy: "REUSED_SEQUENTIAL",
      currency: "USD",
      attestation: "Folio numbers restart every night; observed on 3 stays.",
      reason: "Enable room charge",
      password: "pw",
    });
    // Reloaded after the approval.
    await waitFor(() => expect(g.mock.calls.filter((c) => c[0] === "/pms-financial-onboarding")).toHaveLength(2));
  });

  it("keeps the approve button disabled for an attestation shorter than 20 characters", async () => {
    routes(["site_admin"]);
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: /approve for room charge: protel main/i }));
    fireEvent.click(await screen.findByRole("radio", { name: /globally unique/i }));
    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "EUR" } });
    fireEvent.change(screen.getByLabelText(/^Attestation/), { target: { value: "too short" } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    expect(screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)).toBeDisabled();
  });

  it("reloads and explains a revision conflict", async () => {
    routes(["site_admin"]);
    post.mockRejectedValue(new ApiError(409, { error: "revision_conflict" }));
    render(<RoomChargePage />);
    fireEvent.click(await screen.findByRole("button", { name: /approve for room charge: protel main/i }));
    fireEvent.click(await screen.findByRole("radio", { name: /unique per stay/i }));
    fireEvent.change(screen.getByLabelText(/^Base currency/), { target: { value: "EUR" } });
    fireEvent.change(screen.getByLabelText(/^Attestation/), { target: { value: "Folio numbers are unique within a stay." } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "Enable" } });
    fireEvent.change(screen.getByLabelText(/^Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Approve for room charge" }).at(-1)!);
    expect(await screen.findByText(/changed since the page was loaded/i)).toBeInTheDocument();
    await waitFor(() => expect(g.mock.calls.filter((c) => c[0] === "/pms-financial-onboarding")).toHaveLength(2));
  });

  it("is read-only for a role other than site_admin", async () => {
    routes(["onboarding_reader"]);
    render(<RoomChargePage />);
    expect(await screen.findByText("Protel main")).toBeInTheDocument();
    expect(screen.getByText(/site administrator decision/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
  });

  it("says room charge is not offered when posting is not authorised on this appliance", async () => {
    routes(["site_admin"], ["PMS_POSTING_NOT_AUTHORISED", "NO_ONBOARDED_INTERFACE"]);
    render(<RoomChargePage />);
    expect(await screen.findByText(/room charge is not offered on this appliance/i)).toBeInTheDocument();
    expect(screen.getByText(/nothing is posted to the PMS/i)).toBeInTheDocument();
    expect(screen.getByText("No PMS interface is approved for room charge")).toBeInTheDocument();
  });

  it("tells a role without read access so", async () => {
    routes(["voucher_operator"]);
    render(<RoomChargePage />);
    expect(await screen.findByText(/not available to your role/i)).toBeInTheDocument();
    expect(g).not.toHaveBeenCalledWith("/pms-financial-onboarding");
  });
});
