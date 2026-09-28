// PRICE AND HOW CLIENTS GET IT — the package form's price section.
//
// The server is authoritative on every rule here; these tests hold the client to the same rules so the operator
// is told before a round trip, and hold the form to the one property that matters most on Edit: a save hands
// the stored price, methods and room-charge posting codes back unchanged.

import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PackageForm, type PlanOption } from "@/app/(app)/internet-packages/package-form";
import { acquisitionToForm } from "@/app/(app)/internet-packages/form-mapping";
import {
  buildAcquisition, parsePrice, formatMajor, pctToBp, bpToPct, describePublishRefusal, moduleAvailability,
  methodsForPriceChange, newAcquisitionForm, type ModulesReport, type RoomChargeInterface,
} from "@/lib/commerce-form";

const plans: PlanOption[] = [{ plan_id: "plan-gold", code: "GOLD", name: "Gold", current_revision_id: "rev-gold-4" }];

const mod = (id: string, licensed: boolean, enabled: boolean, ready = true, readiness: string[] = []) =>
  ({ id, label: id, licensed, enabled, ready, readiness, reasons: [] });
const ALL_ON: ModulesReport = { modules: {
  paid_access: mod("paid_access", true, true), card_payment: mod("card_payment", true, true),
  room_charge: mod("room_charge", true, true),
} };
const IFACES: RoomChargeInterface[] = [
  { pms_interface_id: "if-1", display_label: "Protel front office", financial_base_currency: "EGP", ready: true, reason: null },
  { pms_interface_id: "if-2", display_label: "Second PMS", financial_base_currency: "USD", ready: false, reason: "NOT_ONBOARDED" },
];

const initial = {
  code: "PAID", name: "Paid", planID: "plan-gold", planRevisionID: "rev-gold-3",
  rules: [], tiers: [{ order: 10 }], duration: { end_mode: "MANUAL_END" as const },
};

describe("price section logic", () => {
  it("converts the typed price with the currency's exponent, without rounding", () => {
    expect(parsePrice("12.5", 2).minor).toBe(1250);
    expect(parsePrice("1.5", 3).minor).toBe(1500);
    expect(parsePrice("500", 0).minor).toBe(500);
    expect(parsePrice("", 2).minor).toBe(0);
    expect(parsePrice("0.005", 2).error).toMatch(/2 decimal places/);
    expect(parsePrice("1.5", 0).error).toMatch(/whole number/);
    expect(parsePrice("-3", 2).error).toBeTruthy();
    expect(formatMajor(1250, 2)).toBe("12.50");
    expect(formatMajor(1500, 3)).toBe("1.500");
    expect(formatMajor(5, 2)).toBe("0.05");
  });

  it("converts tax percent to basis points and back", () => {
    expect(pctToBp("14").bp).toBe(1400);
    expect(pctToBp("12.5").bp).toBe(1250);
    expect(pctToBp("100.01").error).toBeTruthy();
    expect(bpToPct(1400)).toBe("14");
    expect(bpToPct(1250)).toBe("12.5");
    expect(bpToPct(1234)).toBe("12.34");
  });

  it("mirrors the server's method rules", () => {
    const f = newAcquisitionForm();
    expect(buildAcquisition(f).fields).toEqual({
      price_minor: 0, currency: "USD", currency_exponent: 2, acquisition_methods: ["NOT_REQUIRED"],
    });
    expect(buildAcquisition({ ...f, methods: [] }).error).toMatch(/Free or Voucher/);
    expect(buildAcquisition({ ...f, methods: ["ONLINE_PAYMENT"] }).error).toMatch(/need a price/);
    expect(buildAcquisition({ ...f, price: "10", methods: ["NOT_REQUIRED"] }).error).toMatch(/cannot also be free/);
    expect(buildAcquisition({ ...f, price: "10", methods: [] }).error).toMatch(/Voucher, Card payment or Room charge/);
    expect(buildAcquisition({ ...f, price: "10", currency: "KWD", methods: ["PREPAID", "ONLINE_PAYMENT"] }).fields)
      .toEqual({ price_minor: 10000, currency: "KWD", currency_exponent: 3, acquisition_methods: ["PREPAID", "ONLINE_PAYMENT"] });
  });

  it("requires a valid mapping for Room charge, and sends none without it", () => {
    const f = { ...newAcquisitionForm(), price: "100", currency: "EGP", methods: ["PMS_POSTING" as const] };
    expect(buildAcquisition(f).error).toMatch(/at least one PMS interface/);
    const row = { pms_interface_id: "if-1", posting_code: "WIFI", tax_code: "", tax_rate_pct: "" };
    expect(buildAcquisition({ ...f, room_charge: [{ ...row, posting_code: "A|B" }] }).error).toMatch(/without \|/);
    expect(buildAcquisition({ ...f, room_charge: [{ ...row, posting_code: "X".repeat(21) }] }).error).toMatch(/1 to 20/);
    expect(buildAcquisition({ ...f, room_charge: [row, row] }).error).toMatch(/one row per interface/);
    expect(buildAcquisition({ ...f, room_charge: [{ ...row, tax_rate_pct: "abc" }] }).error).toMatch(/tax rate/);
    expect(buildAcquisition({ ...f, room_charge: [{ ...row, tax_code: "VAT", tax_rate_pct: "14" }] }).fields?.room_charge_mappings)
      .toEqual([{ pms_interface_id: "if-1", posting_code: "WIFI", tax_code: "VAT", tax_rate_bp: 1400 }]);
    // Rows kept in the form while Room charge is unticked are not sent: the server forbids them.
    expect(buildAcquisition({ ...f, methods: ["PREPAID"], room_charge: [row] }).fields)
      .not.toHaveProperty("room_charge_mappings");
  });

  it("follows the price across zero", () => {
    expect(methodsForPriceChange(["NOT_REQUIRED", "PREPAID"], false, true)).toEqual(["PREPAID"]);
    expect(methodsForPriceChange(["PREPAID", "ONLINE_PAYMENT", "PMS_POSTING"], true, false)).toEqual(["PREPAID"]);
    expect(methodsForPriceChange(["PREPAID"], true, true)).toEqual(["PREPAID"]);
  });

  it("fails closed on module state", () => {
    expect(moduleAvailability("card_payment", "error").selectable).toBe(false);
    expect(moduleAvailability("card_payment", null).selectable).toBe(false);
    expect(moduleAvailability("card_payment", { modules: {} })).toEqual({ selectable: false, reason: "Not licensed" });
    expect(moduleAvailability("card_payment", { modules: { card_payment: mod("card_payment", true, false) } }).reason)
      .toBe("Switched off in System › Modules");
    expect(moduleAvailability("card_payment", { modules: { card_payment: mod("card_payment", true, true, false, ["No active card account yet"]) } }))
      .toEqual({ selectable: true, notReady: ["No active card account yet"] });
  });

  it("reads the server's price refusals in words", () => {
    expect(describePublishRefusal("invalid_currency: currency exponent must be 3 for KWD"))
      .toBe("Currency: Currency exponent must be 3 for KWD.");
    expect(describePublishRefusal("invalid_room_charge_mapping: one mapping per PMS interface"))
      .toBe("Room charge: One mapping per PMS interface.");
    expect(describePublishRefusal("something else")).toBeNull();
  });

  it("loads a stored price section, and an old free revision as Free", () => {
    expect(acquisitionToForm({ price_minor: 0, currency: "USD", currency_exponent: 2, settlement_methods: null }))
      .toEqual({ price: "0", currency: "USD", currency_exponent: 2, methods: ["NOT_REQUIRED"], room_charge: [] });
    expect(acquisitionToForm({})).toMatchObject({ price: "0", methods: ["NOT_REQUIRED"] });
    expect(acquisitionToForm({
      price_minor: 15000, currency: "EGP", currency_exponent: 2, settlement_methods: ["PMS_POSTING"],
      room_charge_mappings: [{ pms_interface_id: "if-1", posting_code: "WIFI", tax_rate_bp: 1400 }],
    })).toEqual({
      price: "150.00", currency: "EGP", currency_exponent: 2, methods: ["PMS_POSTING"],
      room_charge: [{ pms_interface_id: "if-1", posting_code: "WIFI", tax_code: "", tax_rate_pct: "14" }],
    });
  });
});

describe("PackageForm price section", () => {
  function renderForm(extra: Partial<Parameters<typeof PackageForm>[0]> = {}) {
    const onSave = vi.fn();
    render(<PackageForm mode="edit" plans={plans} initial={initial} onSave={onSave} {...extra} />);
    return onSave;
  }
  const box = (m: string) => screen.getByTestId(`method-check-${m}`) as HTMLInputElement;

  it("a price above 0 unticks Free; back to 0 unticks Card payment and Room charge", () => {
    renderForm({ modules: ALL_ON, roomChargeInterfaces: IFACES });
    fireEvent.click(box("PREPAID"));
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "25" } });
    expect(box("NOT_REQUIRED").checked).toBe(false);
    expect(box("NOT_REQUIRED").disabled).toBe(true);
    fireEvent.click(box("ONLINE_PAYMENT"));
    fireEvent.click(box("PMS_POSTING"));
    expect(box("ONLINE_PAYMENT").checked).toBe(true);
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "0" } });
    expect(box("ONLINE_PAYMENT").checked).toBe(false);
    expect(box("PMS_POSTING").checked).toBe(false);
    expect(box("PREPAID").checked).toBe(true);
  });

  it("a method whose module is not licensed or switched off cannot be ticked, and says why", () => {
    renderForm({
      modules: { modules: {
        paid_access: mod("paid_access", true, true),
        card_payment: mod("card_payment", false, false),
        room_charge: mod("room_charge", true, false),
      } },
      roomChargeInterfaces: IFACES,
    });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "25" } });
    expect(box("ONLINE_PAYMENT").disabled).toBe(true);
    expect(screen.getByTestId("method-ONLINE_PAYMENT").textContent).toMatch(/Not licensed/);
    expect(box("PMS_POSTING").disabled).toBe(true);
    expect(screen.getByTestId("method-PMS_POSTING").textContent).toMatch(/Switched off in System › Modules/);
    expect(box("PREPAID").disabled).toBe(false);
  });

  it("fails closed when the module state cannot be read, and says so", () => {
    renderForm({ modules: "error", roomChargeInterfaces: IFACES });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "25" } });
    expect(box("ONLINE_PAYMENT").disabled).toBe(true);
    expect(box("PMS_POSTING").disabled).toBe(true);
    expect(screen.getByTestId("modules-error-note")).toBeInTheDocument();
    expect(screen.getByTestId("paid-access-note")).toBeInTheDocument();
  });

  it("a selectable method that is not ready says clients will not be offered it yet", () => {
    renderForm({
      modules: { modules: { ...ALL_ON.modules, card_payment: mod("card_payment", true, true, false, ["No active card account yet"]) } },
    });
    expect(screen.getByTestId("method-not-ready-ONLINE_PAYMENT").textContent).toMatch(/No active card account yet/);
  });

  it("Room charge collects a posting code per interface, warns on currency and readiness, and publishes it", () => {
    const onSave = renderForm({ modules: ALL_ON, roomChargeInterfaces: IFACES });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "150" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "EGP" } });
    fireEvent.click(box("PMS_POSTING"));
    // One row opens on the first interface.
    expect((screen.getByTestId("room-charge-interface-0") as HTMLSelectElement).value).toBe("if-1");
    fireEvent.change(screen.getByLabelText("Room charge mapping 1: posting code"), { target: { value: "WIFI" } });
    fireEvent.change(screen.getByLabelText("Room charge mapping 1: tax rate percent (optional)"), { target: { value: "14" } });

    // A second interface: not approved, and posting in another currency.
    fireEvent.click(screen.getByTestId("add-room-charge-mapping"));
    expect((screen.getByTestId("room-charge-interface-1") as HTMLSelectElement).value).toBe("if-2");
    expect(screen.getByTestId("room-charge-not-ready-1").textContent).toMatch(/Approve the interface for room charge/);
    expect(screen.getByTestId("room-charge-currency-1").textContent).toMatch(/posts in USD.*priced in EGP/);
    expect(screen.queryByTestId("room-charge-currency-0")).toBeNull();
    fireEvent.click(screen.getByLabelText("Remove room charge mapping 2"));

    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    const payload = onSave.mock.calls[0][0].payload;
    expect(payload).toMatchObject({
      price_minor: 15000, currency: "EGP", currency_exponent: 2, acquisition_methods: ["PMS_POSTING"],
      room_charge_mappings: [{ pms_interface_id: "if-1", posting_code: "WIFI", tax_rate_bp: 1400 }],
    });
  });

  it("refuses to save an invalid price section with a readable message", () => {
    const onSave = renderForm({ modules: ALL_ON, roomChargeInterfaces: IFACES });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "25" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/Choose how clients pay/);
  });

  it("EDIT hands a stored price section back unchanged, even while its module is switched off", () => {
    const onSave = renderForm({
      initial: { ...initial, acquisition: acquisitionToForm({
        price_minor: 1500, currency: "KWD", currency_exponent: 3, settlement_methods: ["PREPAID", "PMS_POSTING"],
        room_charge_mappings: [{ pms_interface_id: "if-gone", posting_code: "NET", tax_code: "VAT", tax_rate_bp: 500 }],
      }) },
      modules: { modules: { ...ALL_ON.modules, room_charge: mod("room_charge", true, false) } },
      roomChargeInterfaces: "error",
    });
    expect(screen.getByTestId("interfaces-error-note")).toBeInTheDocument();
    // Ticked and switched off: still untickable, so the package can always be saved again.
    expect(box("PMS_POSTING").checked).toBe(true);
    expect(box("PMS_POSTING").disabled).toBe(false);
    fireEvent.change(screen.getByTestId("name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    expect(onSave.mock.calls[0][0].payload).toMatchObject({
      price_minor: 1500, currency: "KWD", currency_exponent: 3, acquisition_methods: ["PREPAID", "PMS_POSTING"],
      room_charge_mappings: [{ pms_interface_id: "if-gone", posting_code: "NET", tax_code: "VAT", tax_rate_bp: 500 }],
    });
  });

  it("never says Cash and offers no refund", () => {
    renderForm({ modules: ALL_ON, roomChargeInterfaces: IFACES });
    expect(document.body.textContent).not.toMatch(/cash|refund/i);
  });
});
